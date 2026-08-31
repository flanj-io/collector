package flanjdrift

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// goldenCallBatch is the vendored golden OTLP call: one drifting POST
// /v1/charges to api.acme.test whose response carries `amount` as a string
// where spec-v1 declares integer. Ingesting it must deterministically produce
// the live-vs-spec Finding (CLAUDE.md, "Contract").
func goldenCallBatch(t *testing.T) plog.Logs {
	t.Helper()
	b, err := os.ReadFile("../../contracts/golden-otlp-call.json")
	if err != nil {
		t.Fatalf("read golden call: %v", err)
	}
	ld, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(b)
	if err != nil {
		t.Fatalf("unmarshal golden call: %v", err)
	}
	return ld
}

// processorWith returns a processor whose spec cache holds the given
// host->document bindings, as if they had been uploaded in the UI and picked up
// by a refresh.
func processorWith(t *testing.T, bindings map[string][]byte) *driftProcessor {
	t.Helper()
	src := newFakeSource()
	for host, doc := range bindings {
		src.put("int-"+host, host, "v1", doc)
	}
	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("seed cache: %v", errs)
	}
	return &driftProcessor{
		cfg:   &Config{},
		mcp:   drift.NewMCPDetector(),
		specs: c,
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// TestUploadedContractDetectsDrift is the oracle for the whole slice: the same
// golden call that used to drift against a config-mounted `spec_path` now
// drifts against a contract that arrived from the store. If this passes, upload
// actually validates traffic.
func TestUploadedContractDetectsDrift(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})

	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 1 {
		t.Fatalf("findings = %d, want exactly 1 live-vs-spec finding", got)
	}
}

// TestContractBindingScopesDetection: a contract bound to one host must never
// validate another host's traffic. The old model had an OPTIONAL `peer_host`,
// so an unscoped spec validated EVERY outbound call against one document;
// upload makes binding mandatory, and this is that promise under test.
func TestContractBindingScopesDetection(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.globex.test": specV1(t)})

	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 0 {
		t.Fatalf("findings = %d, want 0 — a contract bound to api.globex.test validated api.acme.test traffic", got)
	}
}

// TestUncoveredHostIsCapturedNotValidated: with no contract for the host, the
// call passes through untouched and produces no finding — captured, not
// validated. It still gets its call id stamped, which is what makes front->store
// retries idempotent and ties findings to calls across the tiered hop.
func TestUncoveredHostIsCapturedNotValidated(t *testing.T) {
	p := processorWith(t, nil)

	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	counts := countRecords(out)
	if counts[otlpattr.RecordTypeFinding] != 0 {
		t.Errorf("findings = %d, want 0 for an uncovered host", counts[otlpattr.RecordTypeFinding])
	}
	if counts[otlpattr.RecordTypeCall] != 1 {
		t.Errorf("calls = %d, want the call still captured", counts[otlpattr.RecordTypeCall])
	}

	lr := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	if id, ok := lr.Attributes().Get(otlpattr.AttrCallID); !ok || id.Str() == "" {
		t.Error("call id not stamped on an uncovered call — front->store retries stop being idempotent")
	}
}

// TestUncoveredHostAsksForARefresh: a call for a host with no cached contract
// kicks the refresh loop, so a contract uploaded moments ago starts validating
// without waiting out the full tick. The kick must never block the pipeline.
func TestUncoveredHostAsksForARefresh(t *testing.T) {
	p := processorWith(t, nil)

	if _, err := p.processLogs(context.Background(), goldenCallBatch(t)); err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	select {
	case <-p.kick:
	default:
		t.Fatal("an uncovered host did not ask for a refresh")
	}

	// A second batch with the kick channel already full must still not block.
	p.kickRefresh()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.processLogs(context.Background(), goldenCallBatch(t))
	}()
	<-done
}
