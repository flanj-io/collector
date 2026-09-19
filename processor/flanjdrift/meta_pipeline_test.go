package flanjdrift

import (
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// A meta-gated server: tools/list carries only the search tool and the
// dispatcher.
const metaSnapshotJSON = `{"tools":[
  {"name":"search_tools","description":"Search the catalog.","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}},
  {"name":"call_tool","description":"Call a catalog tool.","inputSchema":{"type":"object","properties":{"name":{"type":"string"},"arguments":{"type":"object"}}}}]}`

const searchResultJSON = `{"tools":[{"name":"get_balance","description":"Balance.",
  "inputSchema":{"type":"object","properties":{"account_id":{"type":"string"}},"required":["account_id"]},
  "outputSchema":{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"]}}]}`

// metaCall appends one tools/call record with an explicit request body.
func metaCall(ld plog.Logs, toolName, reqBody, respBody string) plog.LogRecord {
	mcpCallRecord(ld, toolName, respBody)
	rl := ld.ResourceLogs()
	lr := rl.At(rl.Len() - 1).ScopeLogs().At(0).LogRecords().At(0)
	lr.Attributes().PutStr(otlpattr.AttrReqBody, reqBody)
	return lr
}

func str(lr plog.LogRecord, k string) string {
	v, ok := lr.Attributes().Get(k)
	if !ok {
		return ""
	}
	return v.Str()
}

// TestDispatchedCallIsStoredAsTheInnerTool goes through the
// processor: a search result becomes a contract row of its own, and a
// dispatcher call to a tool that search returned leaves the processor re-keyed
// to that tool — tool name and route — with the dispatcher kept as
// via_dispatch and the request body untouched. A call to a name no search
// returned is left exactly as it arrived.
func TestDispatchedCallIsStoredAsTheInnerTool(t *testing.T) {
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}
	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, metaSnapshotJSON)
	metaCall(ld, "search_tools", `{"query":"balance"}`, searchResultJSON)
	inner := metaCall(ld, "call_tool", `{"name":"get_balance","arguments":{"account_id":"a1"}}`, `{"amount":1200}`)
	stray := metaCall(ld, "call_tool", `{"name":"get_history","arguments":{}}`, `{"ok":true}`)

	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatal(err)
	}
	if got := str(inner, otlpattr.AttrMCPToolName); got != "get_balance" {
		t.Errorf("stored tool name = %q, want get_balance", got)
	}
	if got := str(inner, otlpattr.AttrRoute); got != "/get_balance" {
		t.Errorf("stored route = %q, want /get_balance", got)
	}
	if got := str(inner, otlpattr.AttrMCPViaDispatch); got != "call_tool" {
		t.Errorf("via_dispatch = %q, want call_tool", got)
	}
	if got := str(inner, otlpattr.AttrReqBody); got != `{"name":"get_balance","arguments":{"account_id":"a1"}}` {
		t.Errorf("request body = %q, want the literal dispatcher call", got)
	}
	if call := otlpattr.CallFromRecord(inner); call.ViaDispatch != "call_tool" || call.MCPToolName != "get_balance" {
		t.Errorf("reconstructed call = tool %q via %q", call.MCPToolName, call.ViaDispatch)
	}
	if str(stray, otlpattr.AttrMCPToolName) != "call_tool" || str(stray, otlpattr.AttrMCPViaDispatch) != "" {
		t.Errorf("unsearched call re-keyed: tool %q via %q", str(stray, otlpattr.AttrMCPToolName), str(stray, otlpattr.AttrMCPViaDispatch))
	}

	var rows []model.SpecInfo
	sl := out.ResourceLogs().At(out.ResourceLogs().Len() - 1).ScopeLogs().At(0)
	for k := 0; k < sl.LogRecords().Len(); k++ {
		if lr := sl.LogRecords().At(k); otlpattr.RecordType(lr) == otlpattr.RecordTypeSpecInfo {
			info, _, _ := otlpattr.SpecInfoFromRecord(lr)
			rows = append(rows, info)
		}
	}
	var search *model.SpecInfo
	for i := range rows {
		if rows[i].Source == model.SpecSourceSearchResult {
			search = &rows[i]
		}
	}
	if len(rows) != 2 || search == nil || search.Integration != "acme-payments:search" || search.Endpoints != 1 {
		t.Fatalf("spec_info rows = %+v, want the tools/list row and ONE search row with 1 tool", rows)
	}
}

// TestRestartSeedsTheSearchCatalog: the search row is persisted, and a
// collector that restarts against the same store re-attributes a dispatcher
// call before it has seen a single search — the catalog is no longer one
// process's memory.
func TestRestartSeedsTheSearchCatalog(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	first := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector(), st: st}
	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, metaSnapshotJSON)
	metaCall(ld, "search_tools", `{"query":"balance"}`, searchResultJSON)
	if _, err := first.processLogs(context.Background(), ld); err != nil {
		t.Fatal(err)
	}

	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector(), specs: newSpecCache(),
		kick: make(chan struct{}, 1), done: make(chan struct{})}
	host := extHost{exts: map[component.ID]component.Component{component.MustNewID("flanjstore"): &storeExtStub{st: st}}}
	if err := p.start(context.Background(), host); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = p.shutdown(context.Background()) })

	ld = plog.NewLogs()
	inner := metaCall(ld, "call_tool", `{"name":"get_balance","arguments":{"account_id":"a1"}}`, `{"amount":1200}`)
	if _, err := p.processLogs(context.Background(), ld); err != nil {
		t.Fatal(err)
	}
	if got := str(inner, otlpattr.AttrMCPToolName); got != "get_balance" {
		t.Errorf("after a restart: stored tool = %q, want get_balance (seeded from the search row)", got)
	}
}
