package flanjui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/redact"
	"github.com/flanj-io/collector/internal/store"
)

// ─── harness ─────────────────────────────────────────────────────────────────

// mcpSecret is the canary. It is planted in every place a raw body could hide —
// request body, response body, both header maps, the full URL — and no byte of
// any tool answer may contain it. It is deliberately NOT a value the redaction
// floor recognises (no PAN, no email, no bearer shape): a floor hit would make
// this test pass for the wrong reason, and what is asserted here is that the
// agent surface never REACHES for a body, not that the floor would have caught
// it if it had.
const mcpSecret = "CANARY-9f2a-not-a-known-secret-shape"

func newMCPExt(t *testing.T, st store.Store) (*uiExtension, *httptest.Server, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	ext := &uiExtension{
		cfg:       &Config{UIEndpoint: "127.0.0.1:0", IntegrationID: "acme-payments"},
		telemetry: component.TelemetrySettings{Logger: zap.New(core)},
		st:        st,
	}
	ui := httptest.NewServer(ext.routes())
	t.Cleanup(ui.Close)
	return ext, ui, logs
}

// mcpConnect is a real MCP client speaking streamable HTTP to the collector's
// loopback surface — the acceptance path, not a hand-rolled JSON-RPC POST.
func mcpConnect(t *testing.T, ui *httptest.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "flanj-test-agent", Version: "v0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: ui.URL + mcpPath,
		// The server is stateless, so it answers the optional standalone SSE
		// GET with 405. Not opening it keeps the test to the request/response
		// path an agent actually uses for a read-only server.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect to %s%s: %v", ui.URL, mcpPath, err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// callTool calls one tool and returns the result plus its structured content
// re-decoded as a plain map (what a client sees on the wire).
func callTool(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s returned a tool error: %s", name, resultText(res))
	}
	if res.StructuredContent == nil {
		return res, nil
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content of %s: %v", name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content of %s: %v", name, err)
	}
	return res, out
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// resultBytes is EVERY byte a client receives for one tool call — the prose
// content and the structured content together. The leak assertions scan this,
// not just the part a given test happens to read.
func resultBytes(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return resultText(res) + string(raw)
}

// seedTwoEdges builds a collector that has seen a REST provider (one drifting
// call, one clean one) and an MCP server whose contract arrived observed — the
// two shapes the finding surface has to attribute to an edge differently.
func seedTwoEdges(t *testing.T) *fakeStore {
	t.Helper()
	st := newFakeStore()
	st.edges = []model.Edge{
		{PeerHost: "api.acme.com", Direction: "client", Role: "provider", Class: "external",
			FirstSeen: "2026-09-08T09:00:00Z", LastSeen: "2026-09-08T10:00:00Z", CallCount: 2, DriftCount: 1},
		{PeerHost: "mcp.acme.com", Direction: "client", Role: "provider", Class: "external",
			FirstSeen: "2026-09-08T09:30:00Z", LastSeen: "2026-09-08T10:05:00Z", CallCount: 1, DriftCount: 0},
	}
	// The MCP server's contract is the tools/list snapshot it delivered itself,
	// bound to its host — the join a call-less finding is attributed through.
	st.specInfos = []model.SpecInfo{{
		Integration: "acme-mcp", Role: model.SpecRoleProvider, PeerHost: "mcp.acme.com",
		Format: model.SpecFormatMCP, Source: model.SpecSourceObserved,
	}}

	drifting := model.RedactedCall{
		SchemaVersion: model.SchemaVersion, ID: "call-drift", CapturedAt: "2026-09-08T10:00:00Z",
		Integration: "acme-payments", Direction: "client", PeerHost: "api.acme.com", EdgeClass: model.EdgeClassExternal,
		Method: "POST", Route: "/v1/charges", URL: "https://api.acme.com/v1/charges?token=" + mcpSecret,
		StatusCode: 200, DurationMS: 42,
		RequestHeaders:  map[string]string{"authorization": mcpSecret},
		ResponseHeaders: map[string]string{"x-trace": mcpSecret},
		RequestBody:     `{"note":"` + mcpSecret + `"}`,
		ResponseBody:    `{"amount":"` + mcpSecret + `"}`,
		Validated:       model.ValidatedDrifted, Drifted: true,
		Redaction: model.Redaction{Applied: true, Patterns: []string{"pan"}},
	}
	clean := model.RedactedCall{
		SchemaVersion: model.SchemaVersion, ID: "call-clean", CapturedAt: "2026-09-08T10:01:00Z",
		Integration: "acme-payments", Direction: "client", PeerHost: "api.acme.com", EdgeClass: model.EdgeClassExternal,
		Method: "GET", Route: "/v1/charges", StatusCode: 200,
		ResponseBody: `{"amount":"` + mcpSecret + `"}`,
		Validated:    model.ValidatedClean,
	}
	mcpCall := model.RedactedCall{
		SchemaVersion: model.SchemaVersion, ID: "call-mcp", CapturedAt: "2026-09-08T10:05:00Z",
		Integration: "acme-mcp", Direction: "client", PeerHost: "mcp.acme.com", EdgeClass: model.EdgeClassExternal,
		Transport: "mcp", MCPToolName: "create_charge", MCPServerName: "acme-mcp",
		ResponseBody: `{"tool":"` + mcpSecret + `"}`,
		Validated:    model.ValidatedClean,
	}
	for _, c := range []model.RedactedCall{drifting, clean, mcpCall} {
		if err := st.InsertCall(c); err != nil {
			t.Fatal(err)
		}
	}

	// A per-call REST finding: attributed to its edge through its source call.
	rest := model.Finding{
		SchemaVersion: model.SchemaVersion, ID: "finding-rest", Kind: model.KindLiveVsSpec,
		Severity: model.SeverityBreaking, Integration: "acme-payments", Endpoint: "POST /v1/charges",
		FieldPath: model.Ptr("amount"), Location: model.Ptr("$.response.body.amount"),
		Expected: "type=integer", Actual: `type=string ("1200")`, Rule: "type",
		SourceCallID: model.Ptr("call-drift"), DetectedAt: "2026-09-08T10:00:01Z",
		Detail: "Response field `amount` must be an integer.", OccurrenceCount: 3,
		FirstSeen: "2026-09-08T10:00:01Z", LastSeen: "2026-09-08T10:02:00Z",
	}
	rest.Signature = rest.ComputeSignature()
	// A CALL-LESS MCP finding: no source call at all, so it can only reach its
	// edge through the contract bound to that host.
	defChange := model.Finding{
		SchemaVersion: model.SchemaVersion, ID: "finding-mcp", Kind: model.KindDefinitionChange,
		Severity: model.SeverityInfo, Integration: "acme-mcp", Endpoint: "create_charge",
		FieldPath: model.Ptr("currency"), Expected: "the previous definition", Actual: "a new description",
		Rule: model.RuleDescriptionChanged, SpecVersionFrom: model.Ptr("sha-a"), SpecVersionTo: model.Ptr("sha-b"),
		DetectedAt: "2026-09-08T10:06:00Z", Detail: "Tool `create_charge` changed the description of `currency`.",
		SnapshotObservedAt: "2026-09-08T10:05:00Z", SnapshotObservedFrom: "2026-09-08T09:30:00Z",
		OccurrenceCount: 1, FirstSeen: "2026-09-08T10:06:00Z", LastSeen: "2026-09-08T10:06:00Z",
	}
	defChange.Signature = defChange.ComputeSignature()
	for _, f := range []model.Finding{rest, defChange} {
		if err := st.InsertFinding(f); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func findingIDs(t *testing.T, out map[string]any) []string {
	t.Helper()
	rows, _ := out["findings"].([]any)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		m, _ := r.(map[string]any)
		id, _ := m["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// ─── acceptance: connect, list, query ────────────────────────────────────────

// TestMCPClientListsTheReadOnlyToolSet is the first half of the acceptance: a
// real MCP client connects to a running collector and lists what it can do.
// It also pins the boundary — every tool is annotated read-only, and NO tool
// exists for a mutation the browser guard protects (flag, acknowledge, connect,
// contract upload). An agent that could flag would put a person's name on a
// message no person wrote.
func TestMCPClientListsTheReadOnlyToolSet(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))
	sess := mcpConnect(t, ui)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, want := range []string{"drift_summary", "list_edges", "list_findings", "get_finding"} {
		tool, ok := got[want]
		if !ok {
			t.Fatalf("tools/list is missing %q; got %v", want, res.Tools)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description — an agent chooses tools by their description", want)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not annotated read-only", want)
		}
	}
	if len(got) != 4 {
		t.Fatalf("the agent surface is read-only: expected exactly 4 tools, got %d (%v)", len(got), res.Tools)
	}
	for _, banned := range []string{"flag", "create_thread", "acknowledge", "ack_finding", "connect", "upload_contract"} {
		if _, ok := got[banned]; ok {
			t.Errorf("mutating tool %q is exposed on the agent surface", banned)
		}
	}
}

// TestMCPFindingRowsAreTheUIRows is the other half of the acceptance: what the
// agent gets for a finding is BYTE-IDENTICAL to what the browser gets from
// GET /api/findings. It is asserted against the live REST route rather than a
// fixture on purpose — a fixture would freeze today's shape and let the two
// surfaces drift apart under it.
func TestMCPFindingRowsAreTheUIRows(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))

	resp, err := http.Get(ui.URL + "/api/findings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rest struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rest); err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, row := range rest.Findings {
		id, _ := row["id"].(string)
		raw, _ := json.Marshal(row)
		byID[id] = string(raw)
	}
	if len(byID) != 2 {
		t.Fatalf("expected 2 rows from /api/findings, got %d", len(byID))
	}

	sess := mcpConnect(t, ui)
	_, out := callTool(t, sess, "list_findings", map[string]any{"include_acknowledged": true})
	rows, _ := out["findings"].([]any)
	if len(rows) != len(byID) {
		t.Fatalf("agent surface returned %d finding(s), the UI %d", len(rows), len(byID))
	}
	for _, r := range rows {
		row, _ := r.(map[string]any)
		id, _ := row["id"].(string)
		want, ok := byID[id]
		if !ok {
			t.Fatalf("agent surface returned a finding %q the UI does not have", id)
		}
		raw, _ := json.Marshal(row)
		if string(raw) != want {
			t.Errorf("finding %s differs between the surfaces:\n  agent: %s\n  ui:    %s", id, raw, want)
		}
	}
}

// TestMCPListFindingsByEdge covers the query the brief names — open findings on
// ONE edge — including the case that is easy to lose: a call-less MCP
// definition_change has no source call and therefore no peer host of its own,
// and reaches its edge only through the contract bound to that host.
func TestMCPListFindingsByEdge(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))
	sess := mcpConnect(t, ui)

	_, rest := callTool(t, sess, "list_findings", map[string]any{"edge": "api.acme.com"})
	ids := findingIDs(t, rest)
	if len(ids) != 1 || ids[0] != "finding-rest" {
		t.Fatalf("api.acme.com should hold exactly the REST finding, got %v", ids)
	}

	_, mcpEdge := callTool(t, sess, "list_findings", map[string]any{"edge": "mcp.acme.com"})
	ids = findingIDs(t, mcpEdge)
	if len(ids) != 1 || ids[0] != "finding-mcp" {
		t.Fatalf("a call-less definition_change must still reach its edge through its contract binding; got %v", ids)
	}

	// Filters compose with the edge, and never invent a row.
	_, filtered := callTool(t, sess, "list_findings", map[string]any{"edge": "api.acme.com", "severity": "info"})
	if ids := findingIDs(t, filtered); len(ids) != 0 {
		t.Fatalf("severity filter should have excluded the breaking finding, got %v", ids)
	}
}

// TestMCPUnknownEdgeIsNotAnAllClear: asking about a host this collector has
// never observed must not answer "no findings" — that reads as an all-clear
// about a dependency nothing ever watched. It names the edge as unknown and
// lists the ones it does know.
func TestMCPUnknownEdgeIsNotAnAllClear(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))
	sess := mcpConnect(t, ui)

	res, out := callTool(t, sess, "list_findings", map[string]any{"edge": "api.never-called.example"})
	note, _ := out["note"].(string)
	if !strings.Contains(note, "has not observed") {
		t.Fatalf("note should say the edge was never observed; got %q", note)
	}
	known, _ := out["known_edges"].([]any)
	if len(known) != 2 {
		t.Fatalf("the answer should list the edges it does know; got %v", known)
	}
	if txt := resultText(res); !strings.Contains(txt, "api.acme.com") {
		t.Errorf("the prose answer should name the known edges; got %q", txt)
	}
	if res.IsError {
		t.Error("an unknown edge is a fact about this collector, not a tool error")
	}
}

// ─── honesty ─────────────────────────────────────────────────────────────────

// TestMCPEmptyCollectorAnswersHonestly is the brief's third acceptance: a
// collector with no findings answers honestly rather than fabricating or
// erroring. A fresh install has no calls at all, so "no drift" would be a claim
// about evidence that does not exist.
func TestMCPEmptyCollectorAnswersHonestly(t *testing.T) {
	_, ui, _ := newMCPExt(t, newFakeStore())
	sess := mcpConnect(t, ui)

	res, summary := callTool(t, sess, "drift_summary", nil)
	if res.IsError {
		t.Fatal("an empty collector must not error")
	}
	headline, _ := summary["headline"].(string)
	if !strings.Contains(headline, "Nothing validated yet") {
		t.Errorf("empty collector headline should be the neutral state, got %q", headline)
	}
	if strings.Contains(headline, "No open drift findings.") {
		t.Errorf("empty collector must not read as an all-clear: %q", headline)
	}
	if txt := resultText(res); !strings.Contains(txt, "No external dependencies have been observed yet") {
		t.Errorf("prose should say no dependencies have been observed; got %q", txt)
	}

	res, findings := callTool(t, sess, "list_findings", nil)
	if res.IsError {
		t.Fatal("an empty finding list must not error")
	}
	if rows, _ := findings["findings"].([]any); len(rows) != 0 {
		t.Fatalf("an empty collector must not fabricate findings: %v", rows)
	}
	note, _ := findings["note"].(string)
	if !strings.Contains(note, "nothing has been checked") {
		t.Errorf("note should say nothing has been checked, got %q", note)
	}

	res, edges := callTool(t, sess, "list_edges", nil)
	if rows, _ := edges["edges"].([]any); len(rows) != 0 {
		t.Fatalf("an empty collector has no edges: %v", rows)
	}
	if note, _ := edges["note"].(string); !strings.Contains(note, "discovered from traffic") {
		t.Errorf("list_edges should explain the empty list, got %q", note)
	}
}

// TestMCPNoFindingsWithNothingValidatedIsNotAnAllClear is the false-green case:
// there IS traffic, there are NO findings, and nothing was ever checked because
// no contract was bound. "No drift detected" here is exactly the lie the UI
// headline was fixed for; the agent surface must not reintroduce it.
func TestMCPNoFindingsWithNothingValidatedIsNotAnAllClear(t *testing.T) {
	st := newFakeStore()
	st.edges = []model.Edge{{PeerHost: "api.acme.com", Direction: "client", Role: "provider", Class: "external", CallCount: 2}}
	for _, id := range []string{"c1", "c2"} {
		if err := st.InsertCall(model.RedactedCall{
			SchemaVersion: model.SchemaVersion, ID: id, Integration: "acme-payments", Direction: "client",
			PeerHost: "api.acme.com", EdgeClass: model.EdgeClassExternal, Method: "GET", Route: "/v1/charges",
			Validated: model.ValidatedNot, ValidatedReason: model.NotValidatedNoContract,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, ui, _ := newMCPExt(t, st)
	sess := mcpConnect(t, ui)

	_, summary := callTool(t, sess, "drift_summary", nil)
	headline, _ := summary["headline"].(string)
	if !strings.Contains(headline, "Nothing validated yet") {
		t.Fatalf("traffic with nothing validated must read as the neutral state, got %q", headline)
	}

	_, findings := callTool(t, sess, "list_findings", map[string]any{"edge": "api.acme.com"})
	note, _ := findings["note"].(string)
	if !strings.Contains(note, "not an all-clear") {
		t.Fatalf("note must refuse the all-clear reading, got %q", note)
	}
	if !strings.Contains(note, model.NotValidatedNoContract) {
		t.Errorf("note should name WHY nothing was validated, got %q", note)
	}
	ev, _ := findings["evidence"].(map[string]any)
	if ev["validated_calls"] != float64(0) || ev["window_calls"] != float64(2) {
		t.Fatalf("evidence should be 0 of 2 validated, got %v", ev)
	}
}

// TestMCPGetFindingNotFoundIsHonest: the store is a rolling window, so an id an
// agent carried over from an earlier turn legitimately stops existing. That is
// a fact to report, not an error to raise and not an empty finding to invent.
func TestMCPGetFindingNotFoundIsHonest(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))
	sess := mcpConnect(t, ui)

	res, out := callTool(t, sess, "get_finding", map[string]any{"id": "finding-that-aged-out"})
	if res.IsError {
		t.Fatal("a missing finding is not a tool error")
	}
	if found, _ := out["found"].(bool); found {
		t.Fatal("found must be false")
	}
	if _, ok := out["finding"]; ok {
		t.Fatalf("no finding must be fabricated: %v", out["finding"])
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "rolling window") {
		t.Errorf("note should explain the window, got %q", note)
	}
}

// ─── the redaction floor on this surface ─────────────────────────────────────

// TestMCPNeverEmitsARawBody is the assertion the brief asks for by name. The
// seeded calls carry a canary in every body, both header maps and the URL
// query; every tool is driven with every argument that could widen an answer,
// and no byte of any response may contain it.
func TestMCPNeverEmitsARawBody(t *testing.T) {
	st := seedTwoEdges(t)
	_, ui, _ := newMCPExt(t, st)
	sess := mcpConnect(t, ui)

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"drift_summary", nil},
		{"list_edges", nil},
		{"list_edges", map[string]any{"direction": "client"}},
		{"list_findings", nil},
		{"list_findings", map[string]any{"include_acknowledged": true, "limit": 500}},
		{"list_findings", map[string]any{"edge": "api.acme.com"}},
		{"list_findings", map[string]any{"edge": "mcp.acme.com"}},
		{"list_findings", map[string]any{"kind": model.KindLiveVsSpec}},
		{"get_finding", map[string]any{"id": "finding-rest"}},
		{"get_finding", map[string]any{"id": "finding-mcp"}},
	}
	for _, c := range calls {
		res, _ := callTool(t, sess, c.tool, c.args)
		if body := resultBytes(t, res); strings.Contains(body, mcpSecret) {
			t.Fatalf("%s(%v) leaked captured content across the agent boundary:\n%s", c.tool, c.args, body)
		}
	}

	// And the evidence summary that DOES ride along on get_finding carries only
	// the allowlisted scalars — no body, no header map, no URL.
	_, out := callTool(t, sess, "get_finding", map[string]any{"id": "finding-rest"})
	sc, ok := out["source_call"].(map[string]any)
	if !ok {
		t.Fatal("get_finding should carry the evidence call summary")
	}
	for _, banned := range []string{
		"request_body", "response_body", "request_headers", "response_headers",
		"url", "request_content_type", "response_content_type", "correlation",
	} {
		if _, present := sc[banned]; present {
			t.Errorf("source_call carries %q — the summary is an allowlist of scalars", banned)
		}
	}
	if sc["route"] != "/v1/charges" || sc["method"] != "POST" {
		t.Errorf("source_call should carry the templated route and method: %v", sc)
	}
	if sc["validated"] != model.ValidatedDrifted {
		t.Errorf("source_call should carry the per-call verdict: %v", sc["validated"])
	}
}

// TestMCPRedactsFindingValuesOnTheWayOut pins the one deliberate difference
// between this surface and the browser's: the free-VALUE fields go through the
// redaction floor once more on the way to an agent, whose next hop may be a
// model provider outside this environment. The floor is idempotent and
// add-only, so clean values (asserted in TestMCPFindingRowsAreTheUIRows) are
// unchanged; a value carrying a recognisable secret is tokenised here and only
// here.
func TestMCPRedactsFindingValuesOnTheWayOut(t *testing.T) {
	const pan = "4111111111111111"
	st := newFakeStore()
	st.edges = []model.Edge{{PeerHost: "api.acme.com", Direction: "client", Role: "provider", Class: "external", CallCount: 1}}
	if err := st.InsertCall(model.RedactedCall{
		SchemaVersion: model.SchemaVersion, ID: "c1", Integration: "acme-payments", Direction: "client",
		PeerHost: "api.acme.com", EdgeClass: model.EdgeClassExternal, Method: "GET", Route: "/v1/charges",
		Validated: model.ValidatedDrifted, Drifted: true,
	}); err != nil {
		t.Fatal(err)
	}
	f := model.Finding{
		SchemaVersion: model.SchemaVersion, ID: "f1", Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
		Integration: "acme-payments", Endpoint: "GET /v1/charges", FieldPath: model.Ptr("card"),
		Expected: "type=integer", Actual: `type=string ("` + pan + `")`, Rule: "type",
		SourceCallID: model.Ptr("c1"), DetectedAt: "2026-09-08T10:00:00Z",
		Detail: "Response field `card` was " + pan + ".",
	}
	f.Signature = f.ComputeSignature()
	if err := st.InsertFinding(f); err != nil {
		t.Fatal(err)
	}
	_, ui, _ := newMCPExt(t, st)

	// The browser's row is untouched — this is a divergence on the agent hop
	// only, and the UI's behaviour is not being changed here.
	resp, err := http.Get(ui.URL + "/api/findings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rest struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rest); err != nil {
		t.Fatal(err)
	}
	if len(rest.Findings) != 1 || !strings.Contains(rest.Findings[0]["actual"].(string), pan) {
		t.Fatalf("precondition: the local UI row should still carry the raw value, got %v", rest.Findings)
	}

	sess := mcpConnect(t, ui)
	res, out := callTool(t, sess, "list_findings", nil)
	if body := resultBytes(t, res); strings.Contains(body, pan) {
		t.Fatalf("a recognisable secret reached the agent surface:\n%s", body)
	}
	rows, _ := out["findings"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %v", rows)
	}
	row, _ := rows[0].(map[string]any)
	actual, _ := row["actual"].(string)
	if !redact.ContainsToken(actual) {
		t.Errorf("actual should carry a redaction token, got %q", actual)
	}
	detail, _ := row["detail"].(string)
	if !redact.ContainsToken(detail) {
		t.Errorf("detail should carry a redaction token, got %q", detail)
	}
	// Structural fields are never run through the floor: they are identifiers
	// the caller correlates on, and a redactor over them could only corrupt.
	if row["endpoint"] != "GET /v1/charges" || row["rule"] != "type" || row["id"] != "f1" {
		t.Errorf("structural fields must pass through untouched: %v", row)
	}
}

// TestRedactFindingValuesIsIdempotent is the property the parity claim rests
// on: running the floor over an already-clean (or already-tokenised) value
// returns it unchanged, so the agent's row and the browser's row are the same
// bytes whenever there is nothing to redact.
func TestRedactFindingValuesIsIdempotent(t *testing.T) {
	row := mcpFindingRow{
		"expected": "type=integer",
		"actual":   `type=string ("1200")`,
		"detail":   "Response field `amount` must be an integer.",
	}
	before := map[string]any{}
	for k, v := range row {
		before[k] = v
	}
	redactFindingValues(row)
	for k, v := range before {
		if row[k] != v {
			t.Errorf("%s changed: %q -> %q", k, v, row[k])
		}
	}
	// Twice over a value that DOES redact must not double-wrap.
	tokenised := mcpFindingRow{"actual": `type=string ("4111111111111111")`}
	redactFindingValues(tokenised)
	once := tokenised["actual"].(string)
	redactFindingValues(tokenised)
	if tokenised["actual"].(string) != once {
		t.Errorf("second pass changed the value: %q -> %q", once, tokenised["actual"])
	}
}

// ─── posture ─────────────────────────────────────────────────────────────────

// TestMCPSurfaceRejectsCrossSiteRequests: the agent surface shares a listener
// with a UI the operator has open in a browser, so a page on another origin
// must not be able to drive it. Non-browser clients send no Sec-Fetch-Site and
// are unaffected — which the rest of this file exercises.
func TestMCPSurfaceRejectsCrossSiteRequests(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req, err := http.NewRequest(http.MethodPost, ui.URL+mcpPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-site POST to %s should be refused, got %d", mcpPath, resp.StatusCode)
	}
}

// TestMCPStoreFailureNeverReachesTheAgent mirrors the read routes' rule: a pgx
// connection error is the DSN in prose. It goes to the log; the agent gets the
// one sentence, as a tool error rather than a protocol error so a model can see
// it and stop.
func TestMCPStoreFailureNeverReachesTheAgent(t *testing.T) {
	_, ui, logs := newMCPExt(t, newBrokenStore())
	sess := mcpConnect(t, ui)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, name := range []string{"drift_summary", "list_edges", "list_findings"} {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name})
		if err != nil {
			t.Fatalf("%s: transport error, expected a tool error: %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s should report the store failure as a tool error", name)
		}
		txt := resultText(res)
		if !strings.Contains(txt, msgStoreUnavailable) {
			t.Errorf("%s should answer with the deck's one sentence, got %q", name, txt)
		}
		if strings.Contains(txt, storeDSNError) {
			t.Errorf("%s handed the raw store error to the agent: %q", name, txt)
		}
	}
	var logged bool
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, storeDSNError) {
			logged = true
		}
	}
	if !logged {
		t.Error("the raw store error should reach the log")
	}
}

// ackFinding drives the local acknowledge route the way the SPA does, so these
// tests exercise the real ack join rather than hand-writing the KV.
func ackFinding(t *testing.T, ui *httptest.Server, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ui.URL+"/api/findings/"+id+"/ack", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flanj-UI", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack %s: %d", id, resp.StatusCode)
	}
}

// TestMCPAcknowledgedCountIsScopedToTheQuery: the note tells the caller how many
// findings were hidden from THIS answer, so the count has to be taken after the
// scope filters. Counting acks first reported "1 acknowledged finding excluded"
// on an edge that had none — an invitation to re-query for something that was
// never there.
func TestMCPAcknowledgedCountIsScopedToTheQuery(t *testing.T) {
	_, ui, _ := newMCPExt(t, seedTwoEdges(t))
	ackFinding(t, ui, "finding-mcp") // an informational definition_change on mcp.acme.com
	sess := mcpConnect(t, ui)

	_, other := callTool(t, sess, "list_findings", map[string]any{"edge": "api.acme.com"})
	if n, _ := other["acknowledged_excluded"].(float64); n != 0 {
		t.Errorf("api.acme.com has no acknowledged findings; reported %v", n)
	}
	if note, _ := other["note"].(string); strings.Contains(note, "acknowledged") {
		t.Errorf("note should not mention acknowledgements for an edge that has none: %q", note)
	}

	_, acked := callTool(t, sess, "list_findings", map[string]any{"edge": "mcp.acme.com"})
	if n, _ := acked["acknowledged_excluded"].(float64); n != 1 {
		t.Fatalf("mcp.acme.com should report its one acknowledged finding excluded; got %v", n)
	}
	if ids := findingIDs(t, acked); len(ids) != 0 {
		t.Fatalf("an acknowledged finding is not open; got %v", ids)
	}
	_, included := callTool(t, sess, "list_findings", map[string]any{"edge": "mcp.acme.com", "include_acknowledged": true})
	if ids := findingIDs(t, included); len(ids) != 1 || ids[0] != "finding-mcp" {
		t.Fatalf("include_acknowledged should bring it back; got %v", ids)
	}
}

// TestMCPUnattributedFindingsAreNotSilentlyDropped: a call-less finding whose
// integration has no contract bound to a host reaches no edge line. It is still
// in the total, so without saying so the per-edge counts sum to less than the
// headline the agent just read.
func TestMCPUnattributedFindingsAreNotSilentlyDropped(t *testing.T) {
	st := seedTwoEdges(t)
	orphan := model.Finding{
		SchemaVersion: model.SchemaVersion, ID: "finding-orphan", Kind: model.KindVersionDiff,
		Severity: model.SeverityBreaking, Integration: "some-provider-with-no-contract-row",
		Endpoint: "POST /v1/things", Expected: "spec 1.0.0", Actual: "spec 2.0.0", Rule: "removed-endpoint",
		DetectedAt: "2026-09-08T10:07:00Z", Detail: "The endpoint was removed in 2.0.0.",
	}
	orphan.Signature = orphan.ComputeSignature()
	if err := st.InsertFinding(orphan); err != nil {
		t.Fatal(err)
	}
	_, ui, _ := newMCPExt(t, st)
	sess := mcpConnect(t, ui)

	res, out := callTool(t, sess, "drift_summary", nil)
	if n, _ := out["open_findings_total"].(float64); n != 3 {
		t.Fatalf("expected 3 open findings in total, got %v", n)
	}
	if n, _ := out["unattributed_open_findings"].(float64); n != 1 {
		t.Fatalf("the orphan should be reported as unattributed, got %v", n)
	}
	txt := resultText(res)
	if !strings.Contains(txt, "not attributable to any edge") {
		t.Errorf("prose must account for the finding no edge line shows: %q", txt)
	}
	// And it is reachable: an unfiltered list still returns it.
	_, all := callTool(t, sess, "list_findings", nil)
	if !hasID(findingIDs(t, all), "finding-orphan") {
		t.Error("an unattributed finding must still be listed without an edge filter")
	}
}
