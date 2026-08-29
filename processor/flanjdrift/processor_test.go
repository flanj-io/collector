package flanjdrift

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// countRecords returns how many records of each flanj.record.type a batch holds.
func countRecords(ld plog.Logs) map[string]int {
	out := map[string]int{}
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				out[otlpattr.RecordType(recs.At(k))]++
			}
		}
	}
	return out
}

// TestSpecInfoEmission: the loaded contracts ride the pipeline as spec_info
// records — all of them on the first batch after Start, none again until the
// refresh interval elapses, then again. This is what lets a store pod behind an
// otlphttp hop render the Contracts tab for a front collector's specs.
func TestSpecInfoEmission(t *testing.T) {
	p := &driftProcessor{
		cfg: &Config{IntegrationID: "acme-payments"},
		specInfos: []specInfoRecord{
			{info: model.SpecInfo{Integration: "acme-payments", Role: model.SpecRoleProvider, Format: "openapi"}, raw: []byte("openapi: 3.0.3\n")},
			{info: model.SpecInfo{Integration: "self", Role: model.SpecRoleSelf, Format: "openapi"}, raw: []byte("openapi: 3.0.3\n")},
		},
	}
	emptyBatch := func() plog.Logs { return plog.NewLogs() }

	// First batch after Start: every spec_info goes out (even with no calls).
	out, err := p.processLogs(context.Background(), emptyBatch())
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 2 {
		t.Fatalf("first batch spec_info records = %d, want 2", got)
	}
	// The records decode back to what was loaded.
	sl := out.ResourceLogs().At(out.ResourceLogs().Len() - 1).ScopeLogs().At(0)
	if sl.Scope().Name() != "flanjdrift" {
		t.Errorf("trailing scope = %q, want flanjdrift", sl.Scope().Name())
	}
	info, raw, err := otlpattr.SpecInfoFromRecord(sl.LogRecords().At(0))
	if err != nil || info.Integration != "acme-payments" || string(raw) != "openapi: 3.0.3\n" {
		t.Errorf("decoded spec_info = %+v raw=%q err=%v", info, raw, err)
	}

	// Immediately after: nothing (rate-limited).
	out, _ = p.processLogs(context.Background(), emptyBatch())
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 0 {
		t.Fatalf("second batch spec_info records = %d, want 0 (within refresh interval)", got)
	}

	// Once the interval has elapsed: emitted again (a fresh store converges).
	p.specEmitMu.Lock()
	p.nextSpecEmit = time.Now().Add(-time.Second)
	p.specEmitMu.Unlock()
	out, _ = p.processLogs(context.Background(), emptyBatch())
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 2 {
		t.Fatalf("post-interval spec_info records = %d, want 2", got)
	}

	// A processor with no specs never emits any.
	none := &driftProcessor{cfg: &Config{}}
	out, _ = none.processLogs(context.Background(), emptyBatch())
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 0 {
		t.Fatalf("spec-less processor emitted %d spec_info records", got)
	}
}

// mcpSnapshotJSON is a two-tool snapshot: get_balance declares an outputSchema
// (amount integer), audit_log does not.
const mcpSnapshotJSON = `{"tools":[` +
	`{"name":"get_balance","description":"Balance.","inputSchema":{"type":"object","properties":{"account_id":{"type":"string"}},"required":["account_id"]},` +
	`"outputSchema":{"type":"object","properties":{"amount":{"type":"integer"},"currency":{"type":"string"}},"required":["amount","currency"]}},` +
	`{"name":"audit_log","description":"Free text.","inputSchema":{"type":"object"}}` +
	`],"serverInfo":{"name":"acme-payments-mcp","version":"3.2.0"},"protocolVersion":"2025-06-18"}`

func mcpSnapshotRecord(ld plog.Logs, snapshotJSON string) {
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	a := lr.Attributes()
	a.PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
	a.PutStr(otlpattr.AttrTransport, otlpattr.TransportMCP)
	a.PutStr(otlpattr.AttrDirection, "client")
	a.PutStr(otlpattr.AttrPeerHost, "mcp.acme.test")
	a.PutStr(otlpattr.AttrEdgeClass, "external")
	a.PutStr(otlpattr.AttrIntegration, "acme-payments")
	a.PutStr(otlpattr.AttrMCPContractSnapshot, snapshotJSON)
	a.PutInt(otlpattr.AttrMCPToolCount, 2)
	a.PutStr(otlpattr.AttrMCPServerName, "acme-payments-mcp")
	a.PutStr(otlpattr.AttrMCPServerVersion, "3.2.0")
}

func mcpCallRecord(ld plog.Logs, toolName, respBody string) {
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	a := lr.Attributes()
	a.PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeCall)
	a.PutStr(otlpattr.AttrTransport, otlpattr.TransportMCP)
	a.PutStr(otlpattr.AttrDirection, "client")
	a.PutStr(otlpattr.AttrPeerHost, "mcp.acme.test")
	a.PutStr(otlpattr.AttrEdgeClass, "external")
	a.PutStr(otlpattr.AttrIntegration, "acme-payments")
	a.PutStr(otlpattr.AttrMethod, "tools/call")
	a.PutStr(otlpattr.AttrRoute, "/"+toolName)
	a.PutStr(otlpattr.AttrMCPToolName, toolName)
	a.PutStr(otlpattr.AttrReqBody, `{"account_id":"a1"}`)
	a.PutStr(otlpattr.AttrReqContentType, "application/json")
	a.PutStr(otlpattr.AttrRespBody, respBody)
	a.PutStr(otlpattr.AttrRespContent, "application/json")
}

// TestMCPPipeline drives the MCP loop through the processor exactly as records
// arrive off the wire: contract_snapshot -> spec_info out (self-delivering
// local spec) -> a drifting tools/call -> one output_mismatch finding record;
// an identical re-snapshot emits no definition findings, a changed one does.
// HTTP records keep their pass-through behavior untouched.
func TestMCPPipeline(t *testing.T) {
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}

	// Batch 1: the snapshot + a drifting call (amount string vs integer) + an
	// on-contract call + an HTTP call (no spec loaded -> pass-through).
	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotJSON)
	mcpCallRecord(ld, "get_balance", `{"amount":"1200","currency":"usd"}`)
	mcpCallRecord(ld, "get_balance", `{"amount":1200,"currency":"usd"}`)
	httpLR := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	httpLR.Attributes().PutStr(otlpattr.AttrMethod, "POST")
	httpLR.Attributes().PutStr(otlpattr.AttrRoute, "/v1/charges")

	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	counts := countRecords(out)
	if counts[otlpattr.RecordTypeFinding] != 1 {
		t.Fatalf("finding records = %d, want exactly 1 (the drifting call)", counts[otlpattr.RecordTypeFinding])
	}
	if counts[otlpattr.RecordTypeSpecInfo] != 1 {
		t.Fatalf("spec_info records = %d, want 1 (the observed snapshot)", counts[otlpattr.RecordTypeSpecInfo])
	}
	// Decode the emitted records from the trailing scope.
	sl := out.ResourceLogs().At(out.ResourceLogs().Len() - 1).ScopeLogs().At(0)
	var finding model.Finding
	var info model.SpecInfo
	var raw []byte
	for k := 0; k < sl.LogRecords().Len(); k++ {
		lr := sl.LogRecords().At(k)
		switch otlpattr.RecordType(lr) {
		case otlpattr.RecordTypeFinding:
			finding, _ = otlpattr.FindingFromRecord(lr)
		case otlpattr.RecordTypeSpecInfo:
			info, raw, _ = otlpattr.SpecInfoFromRecord(lr)
		}
	}
	if finding.Kind != model.KindOutputMismatch || finding.Endpoint != "get_balance" || finding.Rule != "type-mismatch" {
		t.Errorf("finding = %+v", finding)
	}
	if finding.SourceCallID == nil || *finding.SourceCallID == "" {
		t.Errorf("finding must reference its stamped source call id")
	}
	if info.Format != model.SpecFormatMCP || info.Integration != "acme-payments" || info.Endpoints != 2 || string(raw) != mcpSnapshotJSON {
		t.Errorf("spec_info = %+v raw=%q", info, raw)
	}

	// Batch 2: the SAME snapshot again -> no findings, but the spec_info
	// upsert still rides (idempotent).
	ld2 := plog.NewLogs()
	mcpSnapshotRecord(ld2, mcpSnapshotJSON)
	out2, _ := p.processLogs(context.Background(), ld2)
	if c := countRecords(out2); c[otlpattr.RecordTypeFinding] != 0 || c[otlpattr.RecordTypeSpecInfo] != 1 {
		t.Fatalf("identical re-snapshot: %v, want 0 findings / 1 spec_info", c)
	}

	// Batch 3: a CHANGED snapshot -> definition_change findings ride the pipe.
	changed := replaceOnce(t, mcpSnapshotJSON, `"amount":{"type":"integer"}`, `"amount":{"type":"string"}`)
	ld3 := plog.NewLogs()
	mcpSnapshotRecord(ld3, changed)
	out3, _ := p.processLogs(context.Background(), ld3)
	if c := countRecords(out3); c[otlpattr.RecordTypeFinding] != 1 {
		t.Fatalf("changed snapshot: %v, want 1 definition_change finding", c)
	}
	sl3 := out3.ResourceLogs().At(out3.ResourceLogs().Len() - 1).ScopeLogs().At(0)
	df, _ := otlpattr.FindingFromRecord(sl3.LogRecords().At(0))
	if df.Kind != model.KindDefinitionChange || df.Severity != model.SeverityBreaking {
		t.Errorf("definition finding = %+v", df)
	}
}

func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	i := strings.Index(s, old)
	if i < 0 {
		t.Fatalf("substring %q not found", old)
	}
	return s[:i] + new + s[i+len(old):]
}
