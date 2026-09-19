package drift

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flanj-io/collector/contract/diff"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// listing is one observed tools/list for the test edge.
func listing(t *testing.T, d *MCPDetector, at string, tools ...map[string]any) []model.Finding {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"tools": tools})
	fs, _, _, err := d.LoadSnapshot(otlpattr.ContractSnapshot{Integration: "mcp-acme-test", PeerHost: "mcp.acme.test",
		Direction: "client", ObservedAt: at, SnapshotJSON: string(b)})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func listedTool(name, description string) map[string]any {
	return map[string]any{"name": name, "description": description,
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}}}
}

func rules(fs []model.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Rule+"@"+f.Endpoint)
	}
	return out
}

var metaTools = []map[string]any{listedTool("search_tools", "Search the catalog."), listedTool("call_tool", "Call a catalog tool.")}

// TestCatalogMovedBehindMetaToolsIsOneEvent is the catalog row in the
// collector: a server that stops listing its tools and lists only discovery
// meta-tools is ONE catalog/INFO event — never one BREAKING removal per tool.
func TestCatalogMovedBehindMetaToolsIsOneEvent(t *testing.T) {
	d := NewMCPDetector()
	listing(t, d, "2026-08-24T09:00:00Z", listedTool("get_balance", "Balance."), listedTool("create_refund", "Refund."), listedTool("list_transactions", "List."))
	fs := listing(t, d, "2026-08-25T09:00:00Z", metaTools...)
	if len(fs) != 1 {
		t.Fatalf("findings = %v, want exactly ONE event", rules(fs))
	}
	f := fs[0]
	if f.Rule != diff.RuleCatalogMovedBehindMetaTools || f.ChangeKind != "catalog" || f.Severity != model.SeverityInfo || f.Flaggable() {
		t.Errorf("finding = %+v, want catalog/INFO %s, not flaggable", f, diff.RuleCatalogMovedBehindMetaTools)
	}
	if !strings.Contains(f.Detail, "3 tools are no longer listed directly") || !strings.Contains(f.Actual, "call_tool, search_tools") {
		t.Errorf("detail/actual = %q / %q", f.Detail, f.Actual)
	}
}

// TestShrinkingToOrdinaryToolsStillReportsRemovals is the control: a list that
// loses tools WITHOUT moving behind meta-tools is still a removal per tool.
func TestShrinkingToOrdinaryToolsStillReportsRemovals(t *testing.T) {
	d := NewMCPDetector()
	listing(t, d, "2026-08-24T09:00:00Z", listedTool("get_balance", "Balance."), listedTool("create_refund", "Refund."))
	fs := listing(t, d, "2026-08-25T09:00:00Z", listedTool("get_balance", "Balance."))
	if len(fs) != 1 || fs[0].Rule != diff.RuleOperationRemoved || fs[0].Severity != model.SeverityBreaking {
		t.Fatalf("findings = %v, want one BREAKING removal of create_refund", rules(fs))
	}
}

// TestSeedMovedBehindMetaToolsIsOneEvent: the seed path (a restarted
// collector, a tiered front adopting a sibling's newer listing) rules the
// same way as the observe path.
func TestSeedMovedBehindMetaToolsIsOneEvent(t *testing.T) {
	d := NewMCPDetector()
	listing(t, d, "2026-08-24T09:00:00Z", listedTool("get_balance", "Balance."), listedTool("create_refund", "Refund."))
	b, _ := json.Marshal(map[string]any{"tools": metaTools})
	fs, ok, err := d.Seed(model.SpecInfo{Integration: "mcp-acme-test", Format: model.SpecFormatMCP, PeerHost: "mcp.acme.test",
		LoadedAt: "2026-08-25T09:00:00Z"}, b)
	if err != nil || !ok {
		t.Fatalf("seed: ok=%v err=%v", ok, err)
	}
	if len(fs) != 1 || fs[0].Rule != diff.RuleCatalogMovedBehindMetaTools {
		t.Fatalf("findings = %v, want ONE catalog-moved event", rules(fs))
	}
}

// TestSeedReportsOnlyTheRuledSet: adopting a newer listing reports what the
// observe path reports — additive changes are not findings. The seed
// path classified without that filter until 2026-09-18.
func TestSeedReportsOnlyTheRuledSet(t *testing.T) {
	d := NewMCPDetector()
	listing(t, d, "2026-08-24T09:00:00Z", listedTool("get_balance", "Balance."))
	b, _ := json.Marshal(map[string]any{"tools": []map[string]any{listedTool("get_balance", "Balance."), listedTool("get_statement", "Statement.")}})
	fs, ok, err := d.Seed(model.SpecInfo{Integration: "mcp-acme-test", Format: model.SpecFormatMCP, PeerHost: "mcp.acme.test",
		LoadedAt: "2026-08-25T09:00:00Z"}, b)
	if err != nil || !ok {
		t.Fatalf("seed: ok=%v err=%v", ok, err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %v, want none: a new tool is additive", rules(fs))
	}
}

// TestTrivialWordingIsNotAFinding is the wording rule in the collector: a
// description that differs only in whitespace, case or punctuation is nothing.
func TestTrivialWordingIsNotAFinding(t *testing.T) {
	d := NewMCPDetector()
	listing(t, d, "2026-08-24T09:00:00Z", listedTool("create_refund", "Refund a captured charge — in full or in part."))
	if fs := listing(t, d, "2026-08-25T09:00:00Z", listedTool("create_refund", "refund a captured charge - in full, or in part")); len(fs) != 0 {
		t.Fatalf("findings = %v, want none for a punctuation/case-only edit", rules(fs))
	}
	fs := listing(t, d, "2026-08-26T09:00:00Z", listedTool("create_refund", "Refund a captured charge, in full only."))
	if len(fs) != 1 || fs[0].Rule != diff.RuleDescriptionChanged || fs[0].Severity != model.SeverityWarning {
		t.Fatalf("findings = %v, want one wording/WARNING for a real rewording", rules(fs))
	}
}

// TestDispatchedCallIsReKeyed: the judgement names the inner
// tool and the dispatcher, which is what the processor stamps on the STORED
// call; an unsearched inner name leaves the call on the dispatcher.
func TestDispatchedCallIsReKeyed(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	d.Judge(searchCall(t, "s1", searchDef("get_balance", "account_id", "number")))
	j := d.Judge(dispatchCall("c1", "get_balance", `{"account_id":"a"}`, `{"amount":10}`))
	if j.InnerTool != "get_balance" || j.ViaDispatch != "call_tool" {
		t.Errorf("searched: inner=%q via=%q, want get_balance via call_tool", j.InnerTool, j.ViaDispatch)
	}
	j = d.Judge(dispatchCall("c2", "never_searched", `{}`, `{"ok":true}`))
	if j.InnerTool != "" || j.ViaDispatch != "" {
		t.Errorf("unsearched: inner=%q via=%q, want the call left on the dispatcher", j.InnerTool, j.ViaDispatch)
	}
}

// TestSearchCatalogIsAContractRow: the tools a search returned
// are a contract row of their own — `<integration>:search`, source
// search_result — emitted when, and only when, the learned catalog changes. A
// later search that omits a tool leaves it in the catalog.
func TestSearchCatalogIsAContractRow(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	s1 := searchCall(t, "s1", searchDef("get_balance", "account_id", "number"), searchDef("create_refund", "charge_id", "number"))
	j := d.Judge(s1)
	if j.SearchSpec == nil {
		t.Fatal("first search: no contract row")
	}
	info := j.SearchSpec.Info
	if info.Integration != "mcp-acme-test:search" || info.Source != model.SpecSourceSearchResult || info.Format != model.SpecFormatMCP ||
		info.Endpoints != 2 || info.PeerHost != "mcp.acme.test" || info.LoadedAt != s1.CapturedAt {
		t.Errorf("row = %+v", info)
	}
	if raw := string(j.SearchSpec.Raw); strings.Index(raw, "create_refund") > strings.Index(raw, "get_balance") {
		t.Errorf("document not sorted by name: %s", raw)
	}
	if j = d.Judge(searchCall(t, "s2", searchDef("get_balance", "account_id", "number"))); j.SearchSpec != nil {
		t.Errorf("a re-observation that changes nothing emitted a row: %+v", j.SearchSpec.Info)
	}
	s3 := searchCall(t, "s3", searchDef("get_statement", "account_id", "number"))
	s3.CapturedAt = "2026-08-24T11:00:00.000Z"
	j = d.Judge(s3)
	if j.SearchSpec == nil || j.SearchSpec.Info.Endpoints != 3 || j.SearchSpec.Info.LoadedAt != s3.CapturedAt {
		t.Fatalf("a newly searched tool: row = %+v, want 3 tools stamped at the new sighting", j.SearchSpec)
	}
}

// TestSeededSearchCatalogSurvivesARestart: a collector that restarts (or a
// front that never saw the search) seeds the catalog from the store row, and
// a dispatcher call to a searched tool is re-attributed at once.
func TestSeededSearchCatalogSurvivesARestart(t *testing.T) {
	d1 := NewMCPDetector()
	metaSurface(t, d1)
	row := d1.Judge(searchCall(t, "s1", searchDef("get_balance", "account_id", "number"))).SearchSpec
	if row == nil {
		t.Fatal("no row to seed from")
	}
	d2 := NewMCPDetector()
	metaSurface(t, d2)
	if ok, err := d2.SeedSearched(row.Info, row.Raw); err != nil || !ok {
		t.Fatalf("SeedSearched: ok=%v err=%v", ok, err)
	}
	if j := d2.Judge(dispatchCall("c1", "get_balance", `{"account_id":"a"}`, `{"amount":10}`)); j.InnerTool != "get_balance" {
		t.Errorf("after the seed: inner=%q, want get_balance", j.InnerTool)
	}
}

// TestSearchedToolCalledDirectlyIsNotStale: a tool the complete
// listing does not declare but a search returned is judged against that
// definition — never a stale_client for using what the server said.
func TestSearchedToolCalledDirectlyIsNotStale(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	d.Judge(searchCall(t, "s1", searchDef("get_balance", "account_id", "number")))
	j := d.Judge(mcpCall("c1", "get_balance", `{"account_id":"a"}`, `{"amount":"10"}`))
	for _, f := range j.Findings {
		if f.Kind == model.KindStaleClient {
			t.Fatalf("stale_client on a searched tool: %+v", f)
		}
	}
	if len(j.Findings) != 1 || j.Findings[0].Kind != model.KindOutputMismatch || j.Findings[0].Source != "search_result" {
		t.Errorf("findings = %+v, want the output_mismatch against the searched definition", j.Findings)
	}
}

// TestToolsetEnabledListingIsTheSessions: a listing observed
// right after a toolset-enable call is that session's catalog. It is compared
// tool by tool, its new tools are judged rather than called stale, and it does
// not replace the baseline — so the next plain listing removes nothing.
func TestToolsetEnabledListingIsTheSessions(t *testing.T) {
	base := []map[string]any{listedTool("get_balance", "Balance."), listedTool("enable_toolset", "Enable a toolset.")}
	expanded := append(append([]map[string]any{}, base...), listedTool("admin_refund", "Refund anything."))

	d := NewMCPDetector()
	listing(t, d, "2026-08-24T09:00:00Z", base...)
	enable := mcpCall("e1", "enable_toolset", `{"toolset":"admin"}`, `{"ok":true}`)
	enable.CapturedAt = "2026-08-24T10:00:00Z"
	d.Judge(enable)
	if fs := listing(t, d, "2026-08-24T10:00:05Z", expanded...); len(fs) != 0 {
		t.Fatalf("enabled listing: findings = %v, want none", rules(fs))
	}
	j := d.Judge(mcpCall("c1", "admin_refund", `{"id":"x"}`, `{"ok":true}`))
	for _, f := range j.Findings {
		if f.Kind == model.KindStaleClient {
			t.Fatalf("a call to the enabled toolset read as stale: %+v", f)
		}
	}
	if fs := listing(t, d, "2026-08-24T12:00:00Z", base...); len(fs) != 0 {
		t.Fatalf("the next plain listing: findings = %v, want no removal of the session's toolset", rules(fs))
	}

	// A definition that moved on a tool BOTH lists carry is still a change.
	d.Judge(enable)
	moved := []map[string]any{listedTool("get_balance", "Balance for one account only."), listedTool("enable_toolset", "Enable a toolset.")}
	fs := listing(t, d, "2026-08-24T10:00:30Z", moved...)
	if len(fs) != 1 || fs[0].Endpoint != "get_balance" || fs[0].Source != "toolset_enable" || fs[0].Completeness != "partial" {
		t.Fatalf("enabled listing with a moved definition: %+v", fs)
	}

	// Control: without the enable call the same pair of listings is a removal.
	c := NewMCPDetector()
	listing(t, c, "2026-08-24T09:00:00Z", base...)
	listing(t, c, "2026-08-24T10:00:05Z", expanded...)
	if fs := listing(t, c, "2026-08-24T12:00:00Z", base...); len(fs) != 1 || fs[0].Rule != diff.RuleOperationRemoved {
		t.Fatalf("control: findings = %v, want the removal the enable window prevents", rules(fs))
	}
}
