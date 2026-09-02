package otlpattr

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
)

// The revision-2026-07-28 attributes round-trip, and — critically — are ABSENT
// rather than defaulted when the server sent none. An empty MCPResultType means
// "an older server said nothing", never "complete"; defaulting it would let
// pre-revision traffic claim a guarantee it never made.
func TestMCPRevisionAttributesRoundTrip(t *testing.T) {
	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(AttrTransport, TransportMCP)
	lr.Attributes().PutStr(AttrMCPToolName, "get_balance")
	lr.Attributes().PutStr(AttrMCPResultType, "input_required")
	lr.Attributes().PutStr(AttrMCPTaskID, "task_9")
	lr.Attributes().PutStr(AttrCorrTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")
	lr.Attributes().PutStr(AttrCorrSpanID, "00f067aa0ba902b7")

	call := CallFromRecord(lr)
	if call.MCPResultType != "input_required" {
		t.Fatalf("resultType: got %q", call.MCPResultType)
	}
	if call.MCPTaskID != "task_9" {
		t.Fatalf("taskId: got %q", call.MCPTaskID)
	}
	// The MCP path carried no trace id at all before the revision documented the
	// `_meta` convention.
	if call.Correlation.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || call.Correlation.SpanID != "00f067aa0ba902b7" {
		t.Fatalf("trace context: %+v", call.Correlation)
	}
}

func TestMCPRevisionAttributesAbsentStayEmpty(t *testing.T) {
	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(AttrTransport, TransportMCP)
	lr.Attributes().PutStr(AttrMCPToolName, "get_balance")

	call := CallFromRecord(lr)
	if call.MCPResultType != "" {
		t.Fatalf("an older server's record must leave resultType empty, got %q", call.MCPResultType)
	}
	if call.MCPTaskID != "" {
		t.Fatalf("taskId must stay empty, got %q", call.MCPTaskID)
	}
}

func TestContractSnapshotCarriesCatalogCacheDirectives(t *testing.T) {
	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(AttrMCPContractSnapshot, `{"tools":[]}`)
	lr.Attributes().PutInt(AttrMCPCatalogTTLMs, 60000)
	lr.Attributes().PutStr(AttrMCPCatalogCacheScope, "session")

	snap, err := ContractSnapshotFromRecord(lr)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.CatalogTTLMs != 60000 || snap.CatalogCacheScope != "session" {
		t.Fatalf("cache directives lost: ttl=%d scope=%q", snap.CatalogTTLMs, snap.CatalogCacheScope)
	}
}
