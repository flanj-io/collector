package drift

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/vinifera-io/collector/contract/diff"
	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
	"github.com/vinifera-io/collector/internal/redact"
)

// loadFixtureRecord parses the first log record of an OTLP/HTTP-JSON fixture
// with the same attribute mapping the collector uses at runtime (the shape
// convention shared with loadGoldenCall in drift_test.go).
func loadFixtureRecord(t *testing.T, name string) plog.LogRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractsDir(), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue *string `json:"stringValue"`
							IntValue    *string `json:"intValue"`
							BoolValue   *bool   `json:"boolValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	lr := plog.NewLogRecord()
	for _, a := range payload.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes {
		switch {
		case a.Value.StringValue != nil:
			lr.Attributes().PutStr(a.Key, *a.Value.StringValue)
		case a.Value.IntValue != nil:
			var n int64
			for _, c := range *a.Value.IntValue {
				n = n*10 + int64(c-'0')
			}
			lr.Attributes().PutInt(a.Key, n)
		case a.Value.BoolValue != nil:
			lr.Attributes().PutBool(a.Key, *a.Value.BoolValue)
		}
	}
	return lr
}

// goldenSnapshot decodes contracts/golden-otlp-mcp-snapshot.json.
func goldenSnapshot(t *testing.T) otlpattr.ContractSnapshot {
	t.Helper()
	snap, err := otlpattr.ContractSnapshotFromRecord(loadFixtureRecord(t, "golden-otlp-mcp-snapshot.json"))
	if err != nil {
		t.Fatalf("snapshot from golden record: %v", err)
	}
	return snap
}

// goldenMCPCall decodes contracts/golden-otlp-mcp-call.json.
func goldenMCPCall(t *testing.T) model.RedactedCall {
	t.Helper()
	call := otlpattr.CallFromRecord(loadFixtureRecord(t, "golden-otlp-mcp-call.json"))
	if call.ID == "" {
		call.ID = "call_mcp_1"
	}
	return call
}

// mutateSnapshotJSON decodes a snapshot document, lets fn edit it, and
// re-encodes it — how the drift-injection variants are built from the golden.
func mutateSnapshotJSON(t *testing.T, base string, fn func(doc map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(base), &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encode snapshot: %v", err)
	}
	return string(out)
}

// tool returns the tools[i] entry named name.
func tool(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	for _, e := range doc["tools"].([]any) {
		m := e.(map[string]any)
		if m["name"] == name {
			return m
		}
	}
	t.Fatalf("tool %q not in snapshot", name)
	return nil
}

// mcpCall builds an MCP tools/call RedactedCall against the golden edge.
func mcpCall(id, toolName, reqBody, respBody string) model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion: 1, ID: id, CapturedAt: "2026-08-24T10:00:00.000Z",
		Integration: "acme-payments", Direction: "client", PeerHost: "mcp.acme.test", EdgeClass: "external",
		Method: "tools/call", Route: "/" + toolName, URL: "mcp://mcp.acme.test/" + toolName,
		RequestBody: reqBody, RequestContentType: "application/json",
		ResponseBody: respBody, ResponseContentType: "application/json",
		Transport: "mcp", MCPToolName: toolName,
		Redaction: model.Redaction{Applied: false, Patterns: []string{}},
	}
}

// TestMCPGolden_OutputMismatch is the flagship Step C test: the golden
// snapshot declares create_refund's outputSchema (refund.amount integer), the
// golden call returns refund.amount as the STRING "1200" — exactly one
// output_mismatch, reproducible from the stored call, riding the same
// signature convention as HTTP drift.
func TestMCPGolden_OutputMismatch(t *testing.T) {
	d := NewMCPDetector()
	snap := goldenSnapshot(t)
	findings, info, raw, err := d.LoadSnapshot(snap)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("first snapshot must yield no findings, got %d", len(findings))
	}
	if info.Format != model.SpecFormatMCP || info.Integration != "acme-payments" || info.PeerHost != "mcp.acme.test" ||
		info.Title != "acme-payments-mcp" || info.Version != "3.2.0" || info.Endpoints != 3 {
		t.Errorf("spec info = %+v", info)
	}
	if string(raw) != snap.SnapshotJSON {
		t.Errorf("raw doc must be the snapshot verbatim")
	}

	call := goldenMCPCall(t)
	fs := d.DetectCall(call)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want exactly 1: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Kind != model.KindOutputMismatch || f.Severity != model.SeverityBreaking {
		t.Errorf("kind/severity = %s/%s", f.Kind, f.Severity)
	}
	if f.Endpoint != "create_refund" {
		t.Errorf("endpoint = %q, want the tool name", f.Endpoint)
	}
	if f.Rule != "type-mismatch" || f.FieldPath == nil || *f.FieldPath != "refund.amount" {
		t.Errorf("rule/field = %q %v", f.Rule, f.FieldPath)
	}
	if f.Location == nil || *f.Location != "$.response.structuredContent.refund.amount" {
		t.Errorf("location = %v", f.Location)
	}
	if f.Expected != "type=integer" || !strings.Contains(f.Actual, `type=string ("1200")`) {
		t.Errorf("expected/actual = %q / %q", f.Expected, f.Actual)
	}
	if f.SourceCallID == nil || *f.SourceCallID != call.ID {
		t.Errorf("source_call_id = %v, want the representative call %q", f.SourceCallID, call.ID)
	}
	wantSig := "acme-payments|create_refund|output_mismatch|type-mismatch|refund.amount"
	if f.Signature != wantSig {
		t.Errorf("signature = %q, want %q", f.Signature, wantSig)
	}
	if f.OccurrenceCount != 1 || f.FirstSeen == "" || f.LastSeen == "" {
		t.Errorf("occurrence seed = %d %q %q", f.OccurrenceCount, f.FirstSeen, f.LastSeen)
	}
	if !f.Flaggable() {
		t.Errorf("output_mismatch must be flaggable")
	}
	// snapshot_observed_at (additive, optional — CONTRACTS §4): the CURRENT
	// snapshot the call was validated against, under the frozen wire key.
	if f.SnapshotObservedAt != snap.ObservedAt {
		t.Errorf("snapshot_observed_at = %q, want the current snapshot's ObservedAt %q", f.SnapshotObservedAt, snap.ObservedAt)
	}
	if doc, err := json.Marshal(f); err != nil || !strings.Contains(string(doc), `"snapshot_observed_at":"`+snap.ObservedAt+`"`) {
		t.Errorf("marshalled finding must carry snapshot_observed_at (err=%v): %s", err, doc)
	}

	// A second drifting call carries the SAME signature — the store collapses
	// it into one finding with occurrence_count 2 (per-signature dedup).
	call2 := call
	call2.ID = "call_mcp_2"
	fs2 := d.DetectCall(call2)
	if len(fs2) != 1 || fs2[0].Signature != f.Signature {
		t.Errorf("repeat call signature = %+v, want the same %q", fs2, f.Signature)
	}
}

// TestMCP_NoSnapshotIsPassThrough: with no observed tools/list the MCP path
// captures without judging (same posture as HTTP without a spec).
func TestMCP_NoSnapshotIsPassThrough(t *testing.T) {
	d := NewMCPDetector()
	if fs := d.DetectCall(goldenMCPCall(t)); fs != nil {
		t.Fatalf("no-snapshot detection = %+v, want none", fs)
	}
}

// TestMCP_NoOutputSchema_NoMismatch: list_transactions declares NO
// outputSchema — whatever comes back, there is NO output_mismatch (the honest
// "no output contract declared" limit; §4.E).
func TestMCP_NoOutputSchema_NoMismatch(t *testing.T) {
	d := NewMCPDetector()
	if _, _, _, err := d.LoadSnapshot(goldenSnapshot(t)); err != nil {
		t.Fatalf("load: %v", err)
	}
	call := mcpCall("c1", "list_transactions", `{"account_id":"a1"}`, `{"transactions":"definitely-not-a-list"}`)
	if fs := d.DetectCall(call); len(fs) != 0 {
		t.Fatalf("no-outputSchema tool produced findings: %+v", fs)
	}
}

// TestMCP_ExtraUndeclaredField (§4.E): an extra undeclared result field is NOT
// a mismatch while additionalProperties is unset; it IS one when the tool
// declares additionalProperties:false.
func TestMCP_ExtraUndeclaredField(t *testing.T) {
	base := goldenSnapshot(t)
	okResp := `{"refund":{"id":"re_71","amount":1200,"status":"succeeded","extra_note":"undeclared"}}`
	args := `{"amount":1200,"currency":"usd"}`

	t.Run("additionalProperties unset -> no finding", func(t *testing.T) {
		d := NewMCPDetector()
		if _, _, _, err := d.LoadSnapshot(base); err != nil {
			t.Fatal(err)
		}
		if fs := d.DetectCall(mcpCall("c1", "create_refund", args, okResp)); len(fs) != 0 {
			t.Fatalf("extra undeclared field must NOT be a mismatch: %+v", fs)
		}
	})

	t.Run("additionalProperties false -> finding", func(t *testing.T) {
		d := NewMCPDetector()
		strict := base
		strict.SnapshotJSON = mutateSnapshotJSON(t, base.SnapshotJSON, func(doc map[string]any) {
			out := tool(t, doc, "create_refund")["outputSchema"].(map[string]any)
			refund := out["properties"].(map[string]any)["refund"].(map[string]any)
			refund["additionalProperties"] = false
		})
		if _, _, _, err := d.LoadSnapshot(strict); err != nil {
			t.Fatal(err)
		}
		fs := d.DetectCall(mcpCall("c1", "create_refund", args, okResp))
		if len(fs) != 1 || fs[0].Kind != model.KindOutputMismatch {
			t.Fatalf("additionalProperties:false extra field: %+v, want 1 output_mismatch", fs)
		}
	})
}

// numberProps / stringProps build captured-props records for the token tests.
func fieldRecord(part, path string, props redact.ValueProps) model.RedactedFieldRecord {
	return model.RedactedFieldRecord{Part: part, RedactedField: redact.RedactedField{Path: path, Pattern: "PAN", Props: props}}
}

// TestMCP_TokenAware: drift on a REDACTED scalar honors the token-aware rules
// exactly like HTTP drift — redacted = unknown (skip) unless the captured
// props DECIDE the constraint; genuine drift next to a token still fires.
func TestMCP_TokenAware(t *testing.T) {
	integer := true
	load := func(t *testing.T) *MCPDetector {
		d := NewMCPDetector()
		if _, _, _, err := d.LoadSnapshot(goldenSnapshot(t)); err != nil {
			t.Fatal(err)
		}
		return d
	}
	tokenResp := `{"amount":"⟦REDACTED:PAN⟧","currency":"usd"}`

	t.Run("props say number -> original satisfied -> skip", func(t *testing.T) {
		call := mcpCall("c1", "get_balance", `{"account_id":"a1"}`, tokenResp)
		call.Redaction.Fields = []model.RedactedFieldRecord{
			fieldRecord("response", "/amount", redact.ValueProps{Type: "number", Length: 4, Integer: &integer, ContainsDigits: true, ContainsASCIIPrintableChars: true}),
		}
		if fs := load(t).DetectCall(call); len(fs) != 0 {
			t.Fatalf("satisfied-by-props token produced findings: %+v", fs)
		}
	})

	t.Run("props say string -> original violated -> finding from props", func(t *testing.T) {
		call := mcpCall("c1", "get_balance", `{"account_id":"a1"}`, tokenResp)
		call.Redaction.Fields = []model.RedactedFieldRecord{
			fieldRecord("response", "/amount", redact.ValueProps{Type: "string", Length: 16, ContainsDigits: true, ContainsASCIIPrintableChars: true}),
		}
		fs := load(t).DetectCall(call)
		if len(fs) != 1 || fs[0].Kind != model.KindOutputMismatch || fs[0].Rule != "type-mismatch" {
			t.Fatalf("props-violated token: %+v, want 1 output_mismatch type-mismatch", fs)
		}
		if !strings.Contains(fs[0].Actual, "redacted") {
			t.Errorf("actual = %q, want a props-built (redacted) rendering", fs[0].Actual)
		}
	})

	t.Run("no props record -> undecidable -> skip", func(t *testing.T) {
		call := mcpCall("c1", "get_balance", `{"account_id":"a1"}`, tokenResp)
		if fs := load(t).DetectCall(call); len(fs) != 0 {
			t.Fatalf("recordless token must skip: %+v", fs)
		}
	})

	t.Run("genuine drift next to a token still fires", func(t *testing.T) {
		call := mcpCall("c1", "get_balance", `{"account_id":"a1"}`, `{"amount":"⟦REDACTED:PAN⟧","currency":42}`)
		fs := load(t).DetectCall(call)
		if len(fs) != 1 || fs[0].FieldPath == nil || *fs[0].FieldPath != "currency" {
			t.Fatalf("genuine drift beside token: %+v, want exactly the currency finding", fs)
		}
	})
}

// TestStaleClient_ToolNotListed: a call to a tool absent from the CURRENT list
// is a stale_client finding — local-only, never flaggable.
func TestStaleClient_ToolNotListed(t *testing.T) {
	d := NewMCPDetector()
	if _, _, _, err := d.LoadSnapshot(goldenSnapshot(t)); err != nil {
		t.Fatal(err)
	}
	call := mcpCall("c1", "old_refund", `{"amount":1}`, `{}`)
	call.MCPIsError = true // a stale call usually errors; detection must still fire
	fs := d.DetectCall(call)
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want exactly 1", fs)
	}
	f := fs[0]
	if f.Kind != model.KindStaleClient || f.Rule != RuleToolNotListed || f.Severity != model.SeverityWarning {
		t.Errorf("kind/rule/severity = %s/%s/%s", f.Kind, f.Rule, f.Severity)
	}
	if !strings.Contains(f.Detail, "calling `old_refund` against a stale definition") {
		t.Errorf("detail = %q, want the Health copy", f.Detail)
	}
	if f.SourceCallID == nil || *f.SourceCallID != "c1" {
		t.Errorf("source_call_id = %v", f.SourceCallID)
	}
	if f.Flaggable() {
		t.Errorf("stale_client must NEVER be flaggable")
	}
	if f.SnapshotObservedAt != "" {
		t.Errorf("stale_client must not carry snapshot_observed_at, got %q", f.SnapshotObservedAt)
	}
}

// TestStaleClient_ArgsViolation: arguments violating the CURRENT inputSchema
// are stale_client (consumer-side), not output_mismatch.
func TestStaleClient_ArgsViolation(t *testing.T) {
	d := NewMCPDetector()
	if _, _, _, err := d.LoadSnapshot(goldenSnapshot(t)); err != nil {
		t.Fatal(err)
	}
	call := mcpCall("c1", "create_refund",
		`{"amount":"12","currency":"usd"}`, // amount must be integer
		`{"refund":{"id":"re_1","amount":1200,"status":"succeeded"}}`)
	fs := d.DetectCall(call)
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want exactly 1", fs)
	}
	f := fs[0]
	if f.Kind != model.KindStaleClient || f.Rule != "type-mismatch" {
		t.Errorf("kind/rule = %s/%s", f.Kind, f.Rule)
	}
	if f.Location == nil || *f.Location != "$.request.arguments.amount" {
		t.Errorf("location = %v", f.Location)
	}
	if f.Flaggable() {
		t.Errorf("stale_client must NEVER be flaggable")
	}
}

// TestDefinitionChange_Classes drives a changed snapshot through the
// classifier: one finding per (edge, operation, rule, fieldPath), BREAKING /
// NON_BREAKING flaggable, DESCRIPTION local-only, both snapshot versions +
// timestamps on the artifact.
func TestDefinitionChange_Classes(t *testing.T) {
	d := NewMCPDetector()
	v1 := goldenSnapshot(t)
	if _, _, _, err := d.LoadSnapshot(v1); err != nil {
		t.Fatal(err)
	}

	v2 := v1
	v2.ObservedAt = "2026-08-24T12:00:00.000Z"
	v2.SnapshotJSON = mutateSnapshotJSON(t, v1.SnapshotJSON, func(doc map[string]any) {
		// BREAKING: get_balance output amount integer -> string
		gb := tool(t, doc, "get_balance")["outputSchema"].(map[string]any)
		gb["properties"].(map[string]any)["amount"].(map[string]any)["type"] = "string"
		// DESCRIPTION only: create_refund reworded
		cr := tool(t, doc, "create_refund")
		cr["description"] = "Refund a charge, now with fees."
		// BREAKING: create_refund new REQUIRED input property
		in := cr["inputSchema"].(map[string]any)
		in["properties"].(map[string]any)["reason"] = map[string]any{"type": "string"}
		in["required"] = append(in["required"].([]any), "reason")
		// NON_BREAKING: list_transactions declares an outputSchema
		tool(t, doc, "list_transactions")["outputSchema"] = map[string]any{
			"type": "object", "properties": map[string]any{"transactions": map[string]any{"type": "array"}},
		}
	})

	findings, _, _, err := d.LoadSnapshot(v2)
	if err != nil {
		t.Fatal(err)
	}
	bySig := map[string]model.Finding{}
	for _, f := range findings {
		if f.Kind != model.KindDefinitionChange {
			t.Errorf("unexpected kind %q", f.Kind)
		}
		if _, dup := bySig[f.Signature]; dup {
			t.Errorf("duplicate signature %q — one finding per (edge, operation, rule, fieldPath)", f.Signature)
		}
		bySig[f.Signature] = f
	}
	if len(findings) != 4 {
		t.Fatalf("findings = %d (%v), want exactly 4", len(findings), sigs(findings))
	}

	check := func(op, rule, fieldPath, severity string, flaggable bool) model.Finding {
		t.Helper()
		sig := "acme-payments|" + op + "|definition_change|" + rule + "|" + fieldPath
		f, ok := bySig[sig]
		if !ok {
			t.Fatalf("missing finding %q; have %v", sig, sigs(findings))
		}
		if f.Severity != severity {
			t.Errorf("%s severity = %q, want %q", rule, f.Severity, severity)
		}
		if f.Flaggable() != flaggable {
			t.Errorf("%s flaggable = %v, want %v", rule, f.Flaggable(), flaggable)
		}
		return f
	}

	br := check("get_balance", diff.RuleOutputPropertyTypeChanged, "output.amount", model.SeverityBreaking, true)
	if !strings.Contains(br.Expected, "integer") || !strings.Contains(br.Actual, "string") {
		t.Errorf("before/after fragments = %q / %q", br.Expected, br.Actual)
	}
	if br.SpecVersionFrom == nil || br.SpecVersionTo == nil ||
		!strings.HasPrefix(*br.SpecVersionFrom, "sha256:") || !strings.HasPrefix(*br.SpecVersionTo, "sha256:") ||
		*br.SpecVersionFrom == *br.SpecVersionTo {
		t.Errorf("spec versions = %v -> %v, want two distinct snapshot hashes", br.SpecVersionFrom, br.SpecVersionTo)
	}
	if !strings.Contains(br.Detail, v1.ObservedAt) || !strings.Contains(br.Detail, v2.ObservedAt) {
		t.Errorf("detail %q must carry both snapshot timestamps", br.Detail)
	}
	// snapshot_observed_at (additive, optional — CONTRACTS §4): the AFTER snapshot's.
	if br.SnapshotObservedAt != v2.ObservedAt {
		t.Errorf("snapshot_observed_at = %q, want the after snapshot's ObservedAt %q", br.SnapshotObservedAt, v2.ObservedAt)
	}

	check("create_refund", diff.RuleInputRequiredPropertyAdded, "input.reason", model.SeverityBreaking, true)
	check("list_transactions", diff.RuleOutputSchemaDeclared, "output", model.SeverityInfo, true)
	desc := check("create_refund", diff.RuleDescriptionChanged, "description", model.SeverityWarning, false)
	if desc.Rule != model.RuleDescriptionChanged {
		t.Errorf("model.RuleDescriptionChanged mirror out of sync: %q vs %q", desc.Rule, model.RuleDescriptionChanged)
	}
}

func sigs(fs []model.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Signature)
	}
	return out
}

// TestDefinitionChangeDetailTail_UIRegex pins the definition_change Detail
// TAIL against the exact regex BOTH UIs parse the snapshot timestamps out of:
//
//	/tools\/list observed (\S+) → (\S+?)\.?$/
//
// collector ui/src/mcp.ts `snapshotTimes` and control-plane
// apps/api/src/peek-web/assets/app.js `snapshotTimesOf` both rely on this
// shape (the detector is the only producer, so the shape is ours to freeze).
// Reword the Detail and those parsers silently return empty timestamps —
// change all three together or not at all.
func TestDefinitionChangeDetailTail_UIRegex(t *testing.T) {
	uiTailRE := regexp.MustCompile(`tools/list observed (\S+) → (\S+?)\.?$`)

	d := NewMCPDetector()
	v1 := goldenSnapshot(t)
	if _, _, _, err := d.LoadSnapshot(v1); err != nil {
		t.Fatal(err)
	}
	v2 := v1
	v2.ObservedAt = "2026-08-24T12:00:00.000Z"
	v2.SnapshotJSON = mutateSnapshotJSON(t, v1.SnapshotJSON, func(doc map[string]any) {
		// One change with a fieldPath, one without a whole-schema fragment —
		// the tail must parse regardless of what precedes it.
		gb := tool(t, doc, "get_balance")["outputSchema"].(map[string]any)
		gb["properties"].(map[string]any)["amount"].(map[string]any)["type"] = "string"
		tool(t, doc, "create_refund")["description"] = "Refund a charge, reworded."
	})
	findings, _, _, err := d.LoadSnapshot(v2)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("no definition_change findings to pin the Detail tail on")
	}
	for _, f := range findings {
		m := uiTailRE.FindStringSubmatch(f.Detail)
		if m == nil {
			t.Fatalf("detail %q does not match the UI parsers' regex — ui/src/mcp.ts snapshotTimes and peek-web app.js snapshotTimesOf would render empty timestamps", f.Detail)
		}
		if m[1] != v1.ObservedAt || m[2] != v2.ObservedAt {
			t.Errorf("parsed timestamps = %q → %q, want %q → %q (what the UIs will display)", m[1], m[2], v1.ObservedAt, v2.ObservedAt)
		}
	}
}

// TestSnapshotVersioning: identical snapshots never re-baseline or emit;
// changed ones rotate current -> previous (kept for diffing).
func TestSnapshotVersioning(t *testing.T) {
	d := NewMCPDetector()
	v1 := goldenSnapshot(t)
	if _, _, _, err := d.LoadSnapshot(v1); err != nil {
		t.Fatal(err)
	}
	edge := mcpEdgeRef(v1.PeerHost, v1.Direction)

	// Re-observation of the SAME list (later ts): no findings, no rotation.
	again := v1
	again.ObservedAt = "2026-08-24T13:00:00.000Z"
	fs, _, _, err := d.LoadSnapshot(again)
	if err != nil || len(fs) != 0 {
		t.Fatalf("identical snapshot: findings=%v err=%v", fs, err)
	}
	d.mu.Lock()
	st := d.edges[edge]
	hashV1 := st.current.Version.ContentHash
	if st.previous != nil {
		t.Errorf("identical snapshot rotated the baseline")
	}
	if st.current.Version.ObservedAt != v1.ObservedAt {
		t.Errorf("identical snapshot must keep the original version metadata")
	}
	d.mu.Unlock()

	// A changed snapshot rotates: previous = the old current.
	v2 := v1
	v2.ObservedAt = "2026-08-24T14:00:00.000Z"
	v2.SnapshotJSON = mutateSnapshotJSON(t, v1.SnapshotJSON, func(doc map[string]any) {
		tool(t, doc, "get_balance")["description"] = "Balance, reworded."
	})
	fs, _, _, err = d.LoadSnapshot(v2)
	if err != nil || len(fs) != 1 {
		t.Fatalf("changed snapshot: findings=%v err=%v, want the one description change", fs, err)
	}
	d.mu.Lock()
	st = d.edges[edge]
	if st.previous == nil || st.previous.Version.ContentHash != hashV1 {
		t.Errorf("previous snapshot not kept for diffing")
	}
	hashV2 := st.current.Version.ContentHash
	d.mu.Unlock()

	// Reverting produces the mirror diff against the KEPT previous.
	v3 := v1
	v3.ObservedAt = "2026-08-24T15:00:00.000Z"
	fs, _, _, err = d.LoadSnapshot(v3)
	if err != nil || len(fs) != 1 {
		t.Fatalf("reverted snapshot: findings=%v err=%v", fs, err)
	}
	d.mu.Lock()
	st = d.edges[edge]
	if st.previous.Version.ContentHash != hashV2 || st.current.Version.ContentHash != hashV1 {
		t.Errorf("revert did not rotate current/previous correctly")
	}
	d.mu.Unlock()
}

// TestSeed: a persisted snapshot restores the baseline across a restart — the
// next identical list emits nothing, a changed one diffs against the seed; a
// live baseline is never overridden by a late seed.
func TestSeed(t *testing.T) {
	v1 := goldenSnapshot(t)
	info := model.SpecInfo{Integration: "acme-payments", Role: model.SpecRoleProvider,
		PeerHost: "mcp.acme.test", Format: model.SpecFormatMCP, LoadedAt: v1.ObservedAt}

	d := NewMCPDetector()
	if err := d.Seed(info, []byte(v1.SnapshotJSON)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Seeded baseline validates calls…
	if fs := d.DetectCall(goldenMCPCall(t)); len(fs) != 1 {
		t.Fatalf("seeded detection = %+v, want the golden output_mismatch", fs)
	}
	// …an identical observed list is a no-op…
	if fs, _, _, err := d.LoadSnapshot(v1); err != nil || len(fs) != 0 {
		t.Fatalf("identical-after-seed: findings=%v err=%v", fs, err)
	}
	// …and a changed one diffs against the seed (no silent re-baseline).
	v2 := v1
	v2.ObservedAt = "2026-08-24T16:00:00.000Z"
	v2.SnapshotJSON = mutateSnapshotJSON(t, v1.SnapshotJSON, func(doc map[string]any) {
		tool(t, doc, "get_balance")["description"] = "Balance, reworded."
	})
	if fs, _, _, err := d.LoadSnapshot(v2); err != nil || len(fs) != 1 {
		t.Fatalf("changed-after-seed: findings=%v err=%v", fs, err)
	}

	// Live state wins over a late seed.
	if err := d.Seed(info, []byte(v1.SnapshotJSON)); err != nil {
		t.Fatalf("late seed: %v", err)
	}
	edge := mcpEdgeRef(v1.PeerHost, "client")
	d.mu.Lock()
	if d.edges[edge].current.Version.ObservedAt != v2.ObservedAt {
		t.Errorf("late seed overrode live state")
	}
	d.mu.Unlock()

	// A non-MCP row never seeds.
	if err := d.Seed(model.SpecInfo{Format: model.SpecFormatOpenAPI}, []byte("openapi: 3.0.3")); err != nil {
		t.Errorf("openapi rows must be ignored, got %v", err)
	}
}

// TestRuleDescriptionChangedMirror pins the model mirror to the classifier's
// constant — the flag-refusal path depends on the equality.
func TestRuleDescriptionChangedMirror(t *testing.T) {
	if model.RuleDescriptionChanged != diff.RuleDescriptionChanged {
		t.Fatalf("model.RuleDescriptionChanged %q != diff.RuleDescriptionChanged %q",
			model.RuleDescriptionChanged, diff.RuleDescriptionChanged)
	}
}
