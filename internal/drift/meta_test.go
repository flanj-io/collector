package drift

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// A meta-gated server's surface: tools/list lists ONLY the search tool and
// the dispatcher.
func metaSurface(t *testing.T, d *MCPDetector) {
	t.Helper()
	doc := `{"tools":[
	  {"name":"search_tools","description":"Search the catalog.","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}},
	  {"name":"call_tool","description":"Call a catalog tool.","inputSchema":{"type":"object","properties":{"name":{"type":"string"},"arguments":{"type":"object"}}}}]}`
	if _, _, _, err := d.LoadSnapshot(otlpattr.ContractSnapshot{Integration: "acme-payments", PeerHost: "mcp.acme.test",
		Direction: "client", ObservedAt: "2026-08-24T09:00:00.000Z", SnapshotJSON: doc}); err != nil {
		t.Fatal(err)
	}
}

// searchDef is one catalog tool as a search result lists it.
func searchDef(name, param, amountType string) map[string]any {
	return map[string]any{
		"name": name, "description": name + " does a thing.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{param: map[string]any{"type": "string"}}, "required": []any{param}},
		"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"amount": map[string]any{"type": amountType}},
			"required": []any{"amount"}},
	}
}

func searchCall(t *testing.T, id string, defs ...map[string]any) model.RedactedCall {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"tools": defs, "truncated": false})
	return mcpCall(id, "search_tools", `{"query":""}`, string(b))
}

func dispatchCall(id, inner, innerArgs, result string) model.RedactedCall {
	return mcpCall(id, "call_tool", `{"name":"`+inner+`","arguments":`+innerArgs+`}`, result)
}

// TestDispatchedCallsAreAttributedToTheInnerTool: an agent that
// searches, then calls two tools through the dispatcher, gets findings on
// THOSE tools — never on call_tool — with the dispatcher kept as evidence.
func TestDispatchedCallsAreAttributedToTheInnerTool(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	if fs, _ := d.JudgeCall(searchCall(t, "s1", searchDef("get_balance", "account_id", "number"), searchDef("create_refund", "charge_id", "number"))); len(fs) != 0 {
		t.Fatalf("first sight of a search result is a baseline, never a finding: %+v", fs)
	}
	// get_balance returns amount as a STRING against its search-learned
	// outputSchema: an output_mismatch on get_balance, via call_tool.
	fs, v := d.JudgeCall(dispatchCall("c1", "get_balance", `{"account_id":"a1"}`, `{"amount":"12.00"}`))
	if len(fs) != 1 || fs[0].Kind != model.KindOutputMismatch || fs[0].Endpoint != "get_balance" || fs[0].ViaDispatch != "call_tool" {
		t.Fatalf("findings = %+v, want one output_mismatch on get_balance via call_tool", fs)
	}
	if !strings.Contains(fs[0].Signature, "|get_balance|") {
		t.Errorf("signature %q is not keyed to the inner tool", fs[0].Signature)
	}
	if v.Verdict != model.ValidatedDrifted {
		t.Errorf("verdict = %+v, want drifted", v)
	}
	// create_refund conforms: validated clean, attributed, no finding.
	fs, v = d.JudgeCall(dispatchCall("c2", "create_refund", `{"charge_id":"ch_1"}`, `{"amount":1200}`))
	if len(fs) != 0 || v.Verdict != model.ValidatedClean {
		t.Errorf("create_refund = %+v / %+v, want clean", fs, v)
	}
}

// TestUnsearchedInnerNameStaysOnTheDispatcher: an inner name
// no recorded search result returned is NEVER re-attributed. The call is
// judged as the dispatcher it literally was.
func TestUnsearchedInnerNameStaysOnTheDispatcher(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	d.JudgeCall(searchCall(t, "s1", searchDef("get_balance", "account_id", "number")))
	fs, v := d.JudgeCall(dispatchCall("c1", "wire_money", `{"to":"x"}`, `{"amount":"not a number"}`))
	for _, f := range fs {
		if f.Endpoint != "call_tool" || f.ViaDispatch != "" {
			t.Errorf("finding %+v was re-attributed off the dispatcher", f)
		}
	}
	// call_tool declares no outputSchema: it is judged as call_tool and says so.
	if v.Reason != model.NotValidatedNoOutputContract {
		t.Errorf("verdict = %+v, want not-validated/no output contract on call_tool itself", v)
	}
}

// TestSearchRedefinitionIsADefinitionChangeOnThatTool: a tool re-observed in a
// later search with a different definition fires on THAT tool, marked
// search_result / partial; a later search that omits a tool raises nothing.
func TestSearchRedefinitionIsADefinitionChangeOnThatTool(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	d.JudgeCall(searchCall(t, "s1", searchDef("get_balance", "account_id", "number"), searchDef("create_refund", "charge_id", "number")))
	// get_balance's amount became a string; create_refund is not in this page.
	fs, _ := d.JudgeCall(searchCall(t, "s2", searchDef("get_balance", "account_id", "string")))
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want exactly the one get_balance change and nothing for the omitted create_refund", fs)
	}
	f := fs[0]
	if f.Kind != model.KindDefinitionChange || f.Endpoint != "get_balance" || f.Rule != "output-property-type-changed" ||
		f.Source != "search_result" || f.Completeness != "partial" || f.ChangeKind != "output" || f.Severity != model.SeverityBreaking {
		t.Errorf("finding = %+v", f)
	}
	// And a renamed parameter behind search is an input rename on that tool.
	fs, _ = d.JudgeCall(searchCall(t, "s3", searchDef("get_balance", "accountId", "string")))
	if len(fs) != 1 || fs[0].Rule != "input-property-renamed" || fs[0].Endpoint != "get_balance" || fs[0].Severity != model.SeverityInfo {
		t.Errorf("rename = %+v, want one input/INFO rename on get_balance", fs)
	}
}

// TestOperatorAdapterConfig: operator config adds a server's own meta-tool
// names for one peer host, alongside the baked ones.
func TestOperatorAdapterConfig(t *testing.T) {
	d := NewMCPDetector()
	d.SetMetaAdapters(map[string]MetaAdapter{"mcp.acme.test": {
		SearchTools:   []string{"find_capabilities"},
		DispatchTools: []DispatchTool{{Name: "run", NameArg: "op", ArgsArg: "input"}},
	}})
	metaSurface(t, d)
	b, _ := json.Marshal(map[string]any{"results": []any{searchDef("get_balance", "account_id", "number")}})
	d.JudgeCall(mcpCall("s1", "find_capabilities", `{}`, string(b)))
	fs, _ := d.JudgeCall(mcpCall("c1", "run", `{"op":"get_balance","input":{"account_id":"a"}}`, `{"amount":"x"}`))
	if len(fs) != 1 || fs[0].Endpoint != "get_balance" || fs[0].ViaDispatch != "run" {
		t.Fatalf("findings = %+v, want one on get_balance via run", fs)
	}
	// Another host does not get acme's adapter: the same search there teaches
	// nothing (find_capabilities is not a search tool on that host), so the
	// same dispatch stays unattributed.
	otherSearch := mcpCall("s2", "find_capabilities", `{}`, string(b))
	otherSearch.PeerHost = "mcp.other.test"
	d.JudgeCall(otherSearch)
	other := mcpCall("c2", "run", `{"op":"get_balance","input":{}}`, `{"amount":"x"}`)
	other.PeerHost = "mcp.other.test"
	if fs, _ := d.JudgeCall(other); len(fs) != 0 {
		t.Errorf("another host used acme's adapter: %+v", fs)
	}
}
