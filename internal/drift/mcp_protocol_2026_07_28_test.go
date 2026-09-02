package drift

import (
	"encoding/json"
	"testing"

	"github.com/flanj-io/collector/contract"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// The Step 0 audit battery: MCP protocol revision 2026-07-28 against the v0.5
// detector, which was built in August against the older stateful model. Every
// case here fails on the pre-audit code.

// snapshotOf builds a contract_snapshot record for one edge from a tools/list
// document, without going through an OTLP fixture.
func snapshotOf(doc string) otlpattr.ContractSnapshot {
	return otlpattr.ContractSnapshot{
		Integration:  "acme-tools",
		Direction:    "client",
		PeerHost:     "mcp.acme.test",
		EdgeClass:    model.EdgeClassExternal,
		ServerName:   "acme-tools-mcp",
		SnapshotJSON: doc,
		ObservedAt:   "2026-09-02T10:00:00.000Z",
	}
}

// mcpCallOn builds one captured MCP tools/call record.
func mcpCallOn(tool, responseBody string) model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion:       model.SchemaVersion,
		ID:                  "call_1",
		Integration:         "acme-tools",
		Direction:           "client",
		PeerHost:            "mcp.acme.test",
		EdgeClass:           model.EdgeClassExternal,
		Method:              "tools/call",
		Route:               "/" + tool,
		Transport:           otlpattr.TransportMCP,
		MCPToolName:         tool,
		RequestBody:         `{"account_id":"acct_1"}`,
		RequestContentType:  "application/json",
		ResponseBody:        responseBody,
		ResponseContentType: "application/json",
	}
}

// balanceTools declares get_balance with a required amount:number — the shape
// every guard below is measured against.
const balanceTools = `{"tools":[{"name":"get_balance",
  "inputSchema":{"type":"object","properties":{"account_id":{"type":"string"}},"required":["account_id"]},
  "outputSchema":{"type":"object","properties":{"amount":{"type":"number"},"currency":{"type":"string"}},"required":["amount","currency"]}}]}`

func loadedDetector(t *testing.T, doc string) *MCPDetector {
	t.Helper()
	d := NewMCPDetector()
	if _, _, _, err := d.LoadSnapshot(snapshotOf(doc)); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	return d
}

// ─── resultType: input_required is normal traffic, never evidence ────────────

// An interactive tool answering "I need more input" returns a payload that is
// partial BY DESIGN. Judging it against outputSchema reports every field the
// server has not filled in yet — a finding manufactured out of the tool working
// exactly as intended.
func TestInputRequiredResultProducesNoOutputMismatch(t *testing.T) {
	d := loadedDetector(t, balanceTools)

	call := mcpCallOn("get_balance", `{"currency":"usd"}`) // no `amount` yet
	if got := d.DetectCall(call); len(got) == 0 {
		t.Fatal("precondition failed: a partial result should violate the schema when NOT input_required")
	}

	call.MCPResultType = model.MCPResultTypeInputRequired
	if got := d.DetectCall(call); len(got) != 0 {
		t.Fatalf("input_required must produce no findings, got %d: %+v", len(got), got[0])
	}
}

// The other half: an interactive tool is DESIGNED to be called without the
// arguments it will go on to ask for, so validating those arguments against
// inputSchema.required accuses the client of being stale for doing what the
// tool asked.
func TestInputRequiredResultProducesNoStaleClientOnArguments(t *testing.T) {
	d := loadedDetector(t, balanceTools)

	call := mcpCallOn("get_balance", `{"amount":1,"currency":"usd"}`)
	call.RequestBody = `{}` // account_id not supplied yet
	if got := d.DetectCall(call); len(got) == 0 {
		t.Fatal("precondition failed: missing required args should be stale_client when NOT input_required")
	}

	call.MCPResultType = model.MCPResultTypeInputRequired
	if got := d.DetectCall(call); len(got) != 0 {
		t.Fatalf("input_required must produce no argument findings, got %d: %+v", len(got), got[0])
	}
}

// Calling a tool the current catalog does not declare is a fact about the TOOL
// NAME, not the payload — it stays true whatever the result type. The guard
// must not swallow it.
func TestInputRequiredStillReportsAToolThatIsNotListed(t *testing.T) {
	d := loadedDetector(t, balanceTools)

	call := mcpCallOn("get_account_balance", `{}`) // renamed away server-side
	call.MCPResultType = model.MCPResultTypeInputRequired
	got := d.DetectCall(call)
	if len(got) != 1 {
		t.Fatalf("want 1 stale_client finding, got %d", len(got))
	}
	if got[0].Kind != model.KindStaleClient || got[0].Rule != RuleToolNotListed {
		t.Fatalf("want %s/%s, got %s/%s", model.KindStaleClient, RuleToolNotListed, got[0].Kind, got[0].Rule)
	}
}

// An empty result type is an OLDER server saying nothing — it must keep being
// validated, or the guard would silently switch detection off for most of the
// installed base.
func TestAbsentResultTypeIsStillValidated(t *testing.T) {
	d := loadedDetector(t, balanceTools)

	call := mcpCallOn("get_balance", `{"amount":"1200","currency":"usd"}`)
	call.MCPResultType = ""
	got := d.DetectCall(call)
	if len(got) != 1 || got[0].Kind != model.KindOutputMismatch {
		t.Fatalf("want one output_mismatch, got %+v", got)
	}
}

// A future revision's value is not input_required and must not be treated as it.
func TestUnknownResultTypeIsStillValidated(t *testing.T) {
	d := loadedDetector(t, balanceTools)

	call := mcpCallOn("get_balance", `{"amount":"1200","currency":"usd"}`)
	call.MCPResultType = "something_new"
	if got := d.DetectCall(call); len(got) != 1 {
		t.Fatalf("an unknown resultType must not suppress detection, got %d findings", len(got))
	}
}

// ─── Tasks: an envelope is not a result ──────────────────────────────────────

// The Tasks extension returns {task:{taskId,…}} and the payload arrives later
// via tasks/get. Validating the envelope against the tool's outputSchema
// reports the tool's own fields as missing.
func TestTaskEnvelopeProducesNoOutputMismatch(t *testing.T) {
	d := loadedDetector(t, balanceTools)

	call := mcpCallOn("get_balance", `{"taskId":"task_9","status":"working"}`)
	if got := d.DetectCall(call); len(got) == 0 {
		t.Fatal("precondition failed: that body should violate the schema when not a task envelope")
	}

	call.MCPTaskID = "task_9"
	if got := d.DetectCall(call); len(got) != 0 {
		t.Fatalf("a task envelope must produce no output findings, got %d: %+v", len(got), got[0])
	}
}

// ─── Snapshot resilience: one odd tool must not take the catalog with it ─────

// A boolean JSON Schema is legal, and became reachable at the root when the
// revision dropped outputSchema's root-type restriction. Before the fix it made
// FromToolsList error and LoadSnapshot drop the ENTIRE tools/list: the edge kept
// its old contract forever and no definition_change ever fired again.
func TestBooleanOutputSchemaDoesNotDropTheSnapshot(t *testing.T) {
	doc := `{"tools":[
	  {"name":"anything_goes","inputSchema":{"type":"object"},"outputSchema":true},
	  {"name":"get_balance","inputSchema":{"type":"object"},
	   "outputSchema":{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"]}}]}`

	d := NewMCPDetector()
	_, info, raw, err := d.LoadSnapshot(snapshotOf(doc))
	if err != nil {
		t.Fatalf("a boolean schema must not drop the snapshot: %v", err)
	}
	if info.Endpoints != 2 {
		t.Fatalf("want both tools kept, got %d", info.Endpoints)
	}
	if len(raw) == 0 {
		t.Fatal("want the raw document stored")
	}

	// The sibling tool's own contract still works.
	got := d.DetectCall(mcpCallOn("get_balance", `{"amount":"1200"}`))
	if len(got) != 1 || got[0].Kind != model.KindOutputMismatch {
		t.Fatalf("want one output_mismatch on the sibling tool, got %+v", got)
	}
}

// The blast-radius rule: a schema that is not a JSON Schema in any draft
// degrades THAT tool to "no contract declared" and leaves its siblings alone.
func TestUndecodableSchemaDegradesOneToolNotTheCatalog(t *testing.T) {
	doc := `{"tools":[
	  {"name":"weird","inputSchema":{"type":"object"},"outputSchema":[1,2,3]},
	  {"name":"get_balance","inputSchema":{"type":"object"},
	   "outputSchema":{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"]}}]}`

	d := NewMCPDetector()
	_, info, _, err := d.LoadSnapshot(snapshotOf(doc))
	if err != nil {
		t.Fatalf("one undecodable schema must not drop the snapshot: %v", err)
	}
	if info.Endpoints != 2 {
		t.Fatalf("want both tools kept, got %d", info.Endpoints)
	}

	// `weird` now declares nothing, which is the honest state — never a finding.
	if got := d.DetectCall(mcpCallOn("weird", `{"whatever":true}`)); len(got) != 0 {
		t.Fatalf("a tool with no decodable schema must produce no findings, got %+v", got)
	}
	// `get_balance` is untouched.
	if got := d.DetectCall(mcpCallOn("get_balance", `{"amount":"1200"}`)); len(got) != 1 {
		t.Fatalf("want the sibling tool still validated, got %d findings", len(got))
	}
}

// dropUndecodableSchemas is copy-on-write: the caller's slice is never mutated.
func TestDropUndecodableSchemasDoesNotMutateTheInput(t *testing.T) {
	orig := []contract.ToolDef{{Name: "a", OutputSchema: json.RawMessage(`[1]`)}}
	before := string(orig[0].OutputSchema)
	out := dropUndecodableSchemas(orig)
	if string(orig[0].OutputSchema) != before {
		t.Fatalf("input mutated: %s", orig[0].OutputSchema)
	}
	if out[0].OutputSchema != nil {
		t.Fatalf("want the bad schema blanked in the copy, got %s", out[0].OutputSchema)
	}
}

// ─── Catalog cache directives ────────────────────────────────────────────────

// Clients are now told to CACHE catalogs, so the tools/list a snapshot records
// may legitimately be up to ttlMs behind the server. The directives ride the
// record and the stored document.
func TestCatalogCacheDirectivesSurviveTheSnapshot(t *testing.T) {
	snap := snapshotOf(`{"tools":[{"name":"get_balance","inputSchema":{"type":"object"}}],"ttlMs":60000,"cacheScope":"session"}`)
	snap.CatalogTTLMs = 60000
	snap.CatalogCacheScope = "session"

	d := NewMCPDetector()
	_, _, raw, err := d.LoadSnapshot(snap)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var doc struct {
		TTLMs      int    `json:"ttlMs"`
		CacheScope string `json:"cacheScope"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode stored doc: %v", err)
	}
	if doc.TTLMs != 60000 || doc.CacheScope != "session" {
		t.Fatalf("stored document lost the cache directives: %+v", doc)
	}
}
