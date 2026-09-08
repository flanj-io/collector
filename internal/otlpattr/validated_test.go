package otlpattr

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/model"
)

// TestCallFromRecord_AbsentVerdictIsUnknown: a call record no drift processor
// stamped decodes to the EXPLICIT unknown — never to "" (which the store
// reserves for rows that predate the stamp) and never, on any path, to clean.
// This is what lets a store pod tell "an older front sent this" from "this row
// is from before verdicts existed", and lets the UI refuse CONFORMING for both.
func TestCallFromRecord_AbsentVerdictIsUnknown(t *testing.T) {
	for _, fixture := range []string{"golden-otlp-call.json", "golden-otlp-server-call.json", "golden-otlp-mcp-call.json"} {
		call := CallFromRecord(recordFromFixture(t, fixture))
		if call.Validated != model.ValidatedUnknown {
			t.Errorf("%s: validated = %q, want %q for a record carrying no verdict", fixture, call.Validated, model.ValidatedUnknown)
		}
		if call.ValidatedReason != "" {
			t.Errorf("%s: validated_reason = %q, want empty", fixture, call.ValidatedReason)
		}
	}
}

// TestStampValidated_RoundTrip: every verdict the processor can stamp comes
// back off the record through CallFromRecord, with the reason present exactly
// when the verdict is not-validated.
func TestStampValidated_RoundTrip(t *testing.T) {
	cases := []model.Validation{
		{Verdict: model.ValidatedClean},
		{Verdict: model.ValidatedDrifted},
		model.NotValidated(model.NotValidatedNoContract),
		model.NotValidated(model.NotValidatedNotRoutable),
		model.NotValidated(model.NotValidatedNoOutputContract),
		model.NotValidated(model.NotValidatedErrorResult),
	}
	for _, v := range cases {
		lr := recordFromFixture(t, "golden-otlp-call.json")
		StampValidated(lr, v)
		got := CallFromRecord(lr)
		if got.Validated != v.Verdict || got.ValidatedReason != v.Reason {
			t.Errorf("stamp %+v round-tripped as validated=%q reason=%q", v, got.Validated, got.ValidatedReason)
		}
		if _, ok := lr.Attributes().Get(AttrValidatedReason); ok != (v.Verdict == model.ValidatedNot) {
			t.Errorf("stamp %+v: %s present=%v, want present only on not-validated", v, AttrValidatedReason, ok)
		}
	}
}

// TestStampValidated_ReplacesAStaleReason: stamping a validated verdict over an
// earlier not-validated one must remove the earlier reason — a record must never
// read "clean, because no-contract".
func TestStampValidated_ReplacesAStaleReason(t *testing.T) {
	lr := plog.NewLogRecord()
	StampValidated(lr, model.NotValidated(model.NotValidatedNoContract))
	StampValidated(lr, model.Validation{Verdict: model.ValidatedClean})
	if v, ok := lr.Attributes().Get(AttrValidatedReason); ok {
		t.Fatalf("stale reason %q survived a clean stamp", v.Str())
	}
	got := CallFromRecord(lr)
	if got.Validated != model.ValidatedClean || got.ValidatedReason != "" {
		t.Errorf("after re-stamp: validated=%q reason=%q, want clean with no reason", got.Validated, got.ValidatedReason)
	}
}
