package flanjdrift

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// The per-call VERDICT stamp (CONTRACTS §2 `flanj.validated`), asserted where
// it is written: every branch of processLogs's per-call path must leave one on
// the call record, saying what this processor did with the call or the first
// gate that stopped it.
//
// BUG (2026-09-07 fix-wave verification; sqlite, postgres — both pods — and
// tiered — both fronts): upload spec-v1 for api.acme.test, drive a drifting
// charge inside the next ~1–4 s, and the Traffic chip read CONFORMING with no
// finding and no drifted flag. The processor's spec cache had not loaded the
// document yet — announced kicks floor at specRefreshFloor, a tiered front
// polls on a 10 s ticker, a front with the wrong store_pod_token never loads it
// at all — while the UI decided `checked` from `spec.loaded_at <= captured_at`,
// a fact about the STORE. The processor never said anything about the call, so
// the reader invented an answer. Now it says.

// callRecords returns the CALL records of a batch, in input order.
func callRecords(ld plog.Logs) []plog.LogRecord {
	var out []plog.LogRecord
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				if otlpattr.RecordType(recs.At(k)) == otlpattr.RecordTypeCall {
					out = append(out, recs.At(k))
				}
			}
		}
	}
	return out
}

// stampOf reads the verdict off a processed call record the way the exporter
// will — through CallFromRecord — so the test sees exactly what the store sees.
func stampOf(t *testing.T, lr plog.LogRecord) model.Validation {
	t.Helper()
	c := otlpattr.CallFromRecord(lr)
	if c.Validated == model.ValidatedUnknown {
		t.Fatalf("call record left the processor with NO verdict stamped (decodes as unknown)")
	}
	return model.Validation{Verdict: c.Validated, Reason: c.ValidatedReason}
}

func processOne(t *testing.T, p *driftProcessor, ld plog.Logs) (model.Validation, map[string]int) {
	t.Helper()
	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	calls := callRecords(out)
	if len(calls) != 1 {
		t.Fatalf("call records after processing = %d, want 1", len(calls))
	}
	return stampOf(t, calls[0]), countRecords(out)
}

// goldenBatchWith returns the golden drifting call with one attribute replaced.
func goldenBatchWith(t *testing.T, key, value string) plog.Logs {
	t.Helper()
	ld := goldenCallBatch(t)
	ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr(key, value)
	return ld
}

// TestStamp_NothingUploaded: the fresh install. Nothing bound anywhere, so the
// call is captured and stamped not-validated / no-contract — the branch that
// used to leave the record silent, and the reader to guess.
func TestStamp_NothingUploaded(t *testing.T) {
	p := processorWith(t, nil)
	v, counts := processOne(t, p, goldenCallBatch(t))
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNoContract {
		t.Errorf("verdict = %+v, want not-validated / no-contract", v)
	}
	if counts[otlpattr.RecordTypeFinding] != 0 {
		t.Errorf("findings = %d, want 0", counts[otlpattr.RecordTypeFinding])
	}
}

// TestStamp_UncoveredHost: a contract is bound — to ANOTHER host. This is the
// timing-window case in disguise: from the processor's side an upload that has
// not reached its cache and an upload for a different host look identical, and
// both are "no contract for this call, right now".
func TestStamp_UncoveredHost(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.globex.test": specV1(t)})
	v, _ := processOne(t, p, goldenCallBatch(t))
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNoContract {
		t.Errorf("verdict = %+v, want not-validated / no-contract", v)
	}
}

// TestStamp_DriftedAndClean: with the contract bound, the golden call drifts
// (amount is a string) and is stamped drifted alongside its finding; the same
// call with a conforming body is stamped CLEAN — validated, nothing found.
func TestStamp_DriftedAndClean(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})

	v, counts := processOne(t, p, goldenCallBatch(t))
	if v.Verdict != model.ValidatedDrifted || v.Reason != "" {
		t.Errorf("drifting call: verdict = %+v, want drifted", v)
	}
	if counts[otlpattr.RecordTypeFinding] != 1 {
		t.Errorf("drifting call: findings = %d, want 1", counts[otlpattr.RecordTypeFinding])
	}

	conforming := goldenBatchWith(t, otlpattr.AttrRespBody,
		`{"id":"ch_1Mox","object":"charge","amount":1200,"currency":"usd","status":"succeeded","created":1755504000,"card":{"last4":"1111","brand":"visa"}}`)
	v, counts = processOne(t, p, conforming)
	if v.Verdict != model.ValidatedClean || v.Reason != "" {
		t.Errorf("conforming call: verdict = %+v, want clean", v)
	}
	if counts[otlpattr.RecordTypeFinding] != 0 {
		t.Errorf("conforming call: findings = %d, want 0", counts[otlpattr.RecordTypeFinding])
	}
}

// TestStamp_NotRoutable: the contract is bound and loaded, but does not
// describe this call — the document cannot route it, so nothing was validated,
// and the stamp says which gate that was rather than "no contract".
func TestStamp_NotRoutable(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})
	ld := goldenBatchWith(t, otlpattr.AttrRoute, "/v1/not-in-the-document")
	ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr(otlpattr.AttrURLFull, "https://api.acme.test/v1/not-in-the-document")
	v, _ := processOne(t, p, ld)
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNotRoutable {
		t.Errorf("verdict = %+v, want not-validated / not-routable", v)
	}
}

// TestStamp_ResponseTheContractDoesNotDescribe: the contract is bound and routes
// the call, but kin-openapi refuses the RESPONSE before any schema comparison —
// an undeclared media type (the sdk #24 session's problem+json case), or a body
// it cannot decode. No finding is produced for those, and the stamp must not
// read that as clean.
func TestStamp_ResponseTheContractDoesNotDescribe(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})

	problem := goldenBatchWith(t, otlpattr.AttrRespContent, "application/problem+json")
	problem.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr(otlpattr.AttrRespBody,
		`{"type":"about:blank","title":"Bad Gateway","status":502}`)
	v, counts := processOne(t, p, problem)
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedMediaTypeUndeclared {
		t.Errorf("problem+json under an application/json contract: verdict = %+v, want not-validated / media-type-undeclared", v)
	}
	if counts[otlpattr.RecordTypeFinding] != 0 {
		t.Errorf("problem+json: findings = %d, want 0", counts[otlpattr.RecordTypeFinding])
	}

	garbage := goldenBatchWith(t, otlpattr.AttrRespBody, `<html>upstream error</html>`)
	v, _ = processOne(t, p, garbage)
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedBodyNotDecodable {
		t.Errorf("undecodable body: verdict = %+v, want not-validated / body-not-decodable", v)
	}
}

// TestStamp_Inbound: the self contract governs server-direction calls. Absent,
// an inbound call is not-validated / no-contract even while provider contracts
// are loaded; present, the same drifting body is drifted and a conforming one
// is clean.
func TestStamp_Inbound(t *testing.T) {
	inbound := func(body string) plog.Logs {
		ld := goldenBatchWith(t, otlpattr.AttrDirection, "server")
		a := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes()
		a.PutStr(otlpattr.AttrPeerHost, "partner.acme.test")
		a.PutStr(otlpattr.AttrRespBody, body)
		return ld
	}
	drifting := `{"id":"ch_1Mox","object":"charge","amount":"1200","currency":"usd","status":"succeeded","created":1755504000,"card":{"last4":"1111","brand":"visa"}}`
	conforming := `{"id":"ch_1Mox","object":"charge","amount":1200,"currency":"usd","status":"succeeded","created":1755504000,"card":{"last4":"1111","brand":"visa"}}`

	// Provider contracts loaded, no self contract: the outbound cache is not
	// empty, so this reaches the inbound branch and must still say no-contract.
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})
	v, _ := processOne(t, p, inbound(drifting))
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNoContract {
		t.Errorf("inbound without a self contract: verdict = %+v, want not-validated / no-contract", v)
	}

	self, err := drift.LoadSpecData(specV1(t))
	if err != nil {
		t.Fatalf("load self spec: %v", err)
	}
	p.selfDoc = self
	v, counts := processOne(t, p, inbound(drifting))
	if v.Verdict != model.ValidatedDrifted || counts[otlpattr.RecordTypeFinding] != 1 {
		t.Errorf("inbound drifting: verdict = %+v findings = %d, want drifted / 1", v, counts[otlpattr.RecordTypeFinding])
	}
	v, counts = processOne(t, p, inbound(conforming))
	if v.Verdict != model.ValidatedClean || counts[otlpattr.RecordTypeFinding] != 0 {
		t.Errorf("inbound conforming: verdict = %+v findings = %d, want clean / 0", v, counts[otlpattr.RecordTypeFinding])
	}
}

// TestStamp_MCP: the MCP path stamps too — off the same detector verdict the
// finding came from. Three calls in one batch, three different answers, and a
// fourth before any snapshot exists.
func TestStamp_MCP(t *testing.T) {
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}

	// Before any tools/list: nothing to judge against.
	pre := plog.NewLogs()
	mcpCallRecord(pre, "get_balance", `{"amount":1200,"currency":"usd"}`)
	v, _ := processOne(t, p, pre)
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNoContract {
		t.Errorf("MCP call before a snapshot: verdict = %+v, want not-validated / no-contract", v)
	}

	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotJSON)
	mcpCallRecord(ld, "get_balance", `{"amount":"1200","currency":"usd"}`) // drifts
	mcpCallRecord(ld, "get_balance", `{"amount":1200,"currency":"usd"}`)   // clean
	mcpCallRecord(ld, "audit_log", `{"anything":"goes"}`)                  // no outputSchema
	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	calls := callRecords(out)
	if len(calls) != 3 {
		t.Fatalf("call records = %d, want 3", len(calls))
	}
	want := []model.Validation{
		{Verdict: model.ValidatedDrifted},
		{Verdict: model.ValidatedClean},
		model.NotValidated(model.NotValidatedNoOutputContract),
	}
	for i, w := range want {
		if got := stampOf(t, calls[i]); got != w {
			t.Errorf("MCP call %d: verdict = %+v, want %+v", i, got, w)
		}
	}
	if n := countRecords(out)[otlpattr.RecordTypeFinding]; n != 1 {
		t.Errorf("findings = %d, want 1 (the drifting call)", n)
	}
}

// TestStamp_EveryCallLeavesWithAVerdict: the golden call through a processor
// whose cache is empty AND whose MCP detector is nil — the most pass-through
// shape there is — still carries a verdict out. Silence is the bug.
func TestStamp_EveryCallLeavesWithAVerdict(t *testing.T) {
	p := &driftProcessor{cfg: &Config{}, specs: newSpecCache(), kick: make(chan struct{}, 1), done: make(chan struct{})}
	v, _ := processOne(t, p, goldenCallBatch(t))
	if v.Verdict != model.ValidatedNot {
		t.Errorf("verdict = %+v, want not-validated", v)
	}
	mcp := plog.NewLogs()
	mcpCallRecord(mcp, "get_balance", `{}`)
	v, _ = processOne(t, p, mcp)
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNoContract {
		t.Errorf("MCP call through a detector-less processor: verdict = %+v, want not-validated / no-contract", v)
	}
}
