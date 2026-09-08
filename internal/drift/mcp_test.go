package drift

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/contract/diff"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/redact"
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
	// snapshot_observed_from is a definition_change-only field: an
	// output_mismatch has no BEFORE snapshot to name.
	if f.SnapshotObservedFrom != "" {
		t.Errorf("output_mismatch must not carry snapshot_observed_from, got %q", f.SnapshotObservedFrom)
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
	if f.SnapshotObservedFrom != "" {
		t.Errorf("stale_client must not carry snapshot_observed_from, got %q", f.SnapshotObservedFrom)
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
	// snapshot_observed_from (additive, optional — CONTRACTS §4): the BEFORE
	// snapshot's — the structured sibling that replaces regexing the Detail.
	if br.SnapshotObservedFrom != v1.ObservedAt {
		t.Errorf("snapshot_observed_from = %q, want the before snapshot's ObservedAt %q", br.SnapshotObservedFrom, v1.ObservedAt)
	}
	if doc, err := json.Marshal(br); err != nil || !strings.Contains(string(doc), `"snapshot_observed_from":"`+v1.ObservedAt+`"`) {
		t.Errorf("marshalled definition_change must carry snapshot_observed_from (err=%v): %s", err, doc)
	}

	check("create_refund", diff.RuleInputRequiredPropertyAdded, "input.reason", model.SeverityBreaking, true)
	check("list_transactions", diff.RuleOutputSchemaDeclared, "output", model.SeverityInfo, true)
	// DESCRIPTION is FLAGGABLE since qfix2-2026-08-26 (ux-design-v2 §2.7): the
	// evidence rule is amended, not broken — a description change is the
	// provider's own published text, before and after. It still never
	// auto-flags; only a human pressing the control sends it.
	desc := check("create_refund", diff.RuleDescriptionChanged, "description", model.SeverityWarning, true)
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

	// Re-observation of the SAME list (later ts): no findings, no rotation —
	// and the row it reports carries the FIRST observation's stamp, so the
	// store's loaded_at (the UI's "since this snapshot" anchor, the contract
	// channel's change token) does not move for a list that did not.
	again := v1
	again.ObservedAt = "2026-08-24T13:00:00.000Z"
	fs, againInfo, _, err := d.LoadSnapshot(again)
	if err != nil || len(fs) != 0 {
		t.Fatalf("identical snapshot: findings=%v err=%v", fs, err)
	}
	if againInfo.LoadedAt != v1.ObservedAt {
		t.Errorf("re-observed identical list restamped the row: loaded_at=%s, want %s", againInfo.LoadedAt, v1.ObservedAt)
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
	fs, v2Info, _, err := d.LoadSnapshot(v2)
	if err != nil || len(fs) != 1 {
		t.Fatalf("changed snapshot: findings=%v err=%v, want the one description change", fs, err)
	}
	if v2Info.LoadedAt != v2.ObservedAt {
		t.Errorf("changed list must stamp the row with its own observation: loaded_at=%s, want %s", v2Info.LoadedAt, v2.ObservedAt)
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

// TestSeed: the store's snapshot is the org-wide baseline, and the newer
// observation wins. Empty → adopted; an identical observed list → no findings
// and the row keeps the seed's stamp; a changed observed list → diffs against
// the seed; an OLDER seed → live wins; a NEWER seed with the same content →
// nothing to learn; a NEWER seed with different content — a sibling front
// observed a change — → adopted with NO findings and the live list rotated to
// previous; the next observed change then diffs against what the STORE held.
func TestSeed(t *testing.T) {
	v1 := goldenSnapshot(t)
	info := model.SpecInfo{Integration: "acme-payments", Role: model.SpecRoleProvider,
		PeerHost: "mcp.acme.test", Format: model.SpecFormatMCP, LoadedAt: v1.ObservedAt}
	edge := mcpEdgeRef(v1.PeerHost, "client")

	d := NewMCPDetector()
	if d.HasBaseline(v1.PeerHost, "client") {
		t.Fatal("an empty detector claims a baseline")
	}
	fs0, adopted, err := d.Seed(info, []byte(v1.SnapshotJSON))
	if err != nil || !adopted || len(fs0) != 0 {
		t.Fatalf("first seed: adopted=%v findings=%v err=%v, want adopted, nothing to diff", adopted, fs0, err)
	}
	if !d.HasBaseline(v1.PeerHost, "client") {
		t.Fatal("seeded edge reports no baseline")
	}
	// Seeded baseline validates calls…
	if fs := d.DetectCall(goldenMCPCall(t)); len(fs) != 1 {
		t.Fatalf("seeded detection = %+v, want the golden output_mismatch", fs)
	}
	// …an identical observed list is a no-op that keeps the seed's stamp…
	if fs, again, _, err := d.LoadSnapshot(v1); err != nil || len(fs) != 0 || again.LoadedAt != v1.ObservedAt {
		t.Fatalf("identical-after-seed: findings=%v loaded_at=%s err=%v", fs, again.LoadedAt, err)
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

	observed := func() (cur, prev string) {
		t.Helper()
		d.mu.Lock()
		defer d.mu.Unlock()
		st := d.edges[edge]
		if st.previous != nil {
			prev = st.previous.Version.ObservedAt
		}
		return st.current.Version.ObservedAt, prev
	}

	// An OLDER seed never displaces live state: this process is ahead of the
	// store, and the store catches up from the spec_info it forwards.
	if fs, adopted, err := d.Seed(info, []byte(v1.SnapshotJSON)); err != nil || adopted || len(fs) != 0 {
		t.Fatalf("older seed: adopted=%v findings=%v err=%v, want live state kept", adopted, fs, err)
	}
	if cur, _ := observed(); cur != v2.ObservedAt {
		t.Errorf("older seed overrode live state: current observed_at=%s", cur)
	}

	// A NEWER seed with the SAME content is nothing to learn — and must not
	// restamp the live version (that stamp is what "since this snapshot" and
	// the row's loaded_at are anchored on).
	same := info
	same.LoadedAt = "2026-08-24T17:00:00.000Z"
	if fs, adopted, err := d.Seed(same, []byte(v2.SnapshotJSON)); err != nil || adopted || len(fs) != 0 {
		t.Fatalf("same-content newer seed: adopted=%v findings=%v err=%v, want nothing learned", adopted, fs, err)
	}
	if cur, _ := observed(); cur != v2.ObservedAt {
		t.Errorf("same-content seed restamped the live version: %s", cur)
	}

	// A NEWER seed with DIFFERENT content: a sibling front observed a change
	// this process never saw. Adopted, with the live list kept as previous —
	// and the change between them REPORTED, exactly as observing it would
	// have (TestSeedReportsTheChangeItAdopts has the case that made this
	// necessary); findings dedup by signature.
	v3JSON := mutateSnapshotJSON(t, v1.SnapshotJSON, func(doc map[string]any) {
		tool(t, doc, "get_balance")["description"] = "Balance, reworded again."
	})
	newer := info
	newer.LoadedAt = "2026-08-24T18:00:00.000Z"
	fs3, adopted, err := d.Seed(newer, []byte(v3JSON))
	if err != nil || !adopted {
		t.Fatalf("newer seed: adopted=%v err=%v, want adopted", adopted, err)
	}
	if len(fs3) != 1 || fs3[0].Kind != model.KindDefinitionChange || fs3[0].Rule != diff.RuleDescriptionChanged {
		t.Fatalf("newer seed reported %+v, want the one description change", fs3)
	}
	if fs3[0].SnapshotObservedFrom != v2.ObservedAt || fs3[0].SnapshotObservedAt != newer.LoadedAt {
		t.Errorf("seed finding spans %s → %s, want %s → %s", fs3[0].SnapshotObservedFrom, fs3[0].SnapshotObservedAt, v2.ObservedAt, newer.LoadedAt)
	}
	if cur, prev := observed(); cur != newer.LoadedAt || prev != v2.ObservedAt {
		t.Errorf("newer seed: current=%s previous=%s, want %s / %s", cur, prev, newer.LoadedAt, v2.ObservedAt)
	}
	// Re-observing the adopted list reports the STORE's stamp, not this record's.
	v3 := v1
	v3.ObservedAt = "2026-08-24T19:00:00.000Z"
	v3.SnapshotJSON = v3JSON
	if fs, ri, _, err := d.LoadSnapshot(v3); err != nil || len(fs) != 0 || ri.LoadedAt != newer.LoadedAt {
		t.Fatalf("re-observe adopted: findings=%v loaded_at=%s err=%v, want none / %s", fs, ri.LoadedAt, err, newer.LoadedAt)
	}
	// The next observed CHANGE diffs against what the store held: the
	// finding's before-snapshot is the sibling's observation, not this one's.
	v4 := v1
	v4.ObservedAt = "2026-08-24T20:00:00.000Z"
	v4.SnapshotJSON = mutateSnapshotJSON(t, v3JSON, func(doc map[string]any) {
		tool(t, doc, "get_balance")["description"] = "Balance, final."
	})
	fs, _, _, err := d.LoadSnapshot(v4)
	if err != nil || len(fs) != 1 {
		t.Fatalf("change after adoption: findings=%v err=%v", fs, err)
	}
	if fs[0].SnapshotObservedFrom != newer.LoadedAt || fs[0].SnapshotObservedAt != v4.ObservedAt {
		t.Errorf("finding spans %s → %s, want %s → %s", fs[0].SnapshotObservedFrom, fs[0].SnapshotObservedAt, newer.LoadedAt, v4.ObservedAt)
	}

	// Stamps order by instant, not by text: a whole-second stamp and a
	// millisecond one compare correctly, and equal instants are a tie.
	if !observedAfter("2026-08-24T18:00:01Z", "2026-08-24T18:00:00.500Z") {
		t.Error("18:00:01 is after 18:00:00.500")
	}
	if observedAfter("2026-08-24T18:00:00Z", "2026-08-24T18:00:00.000Z") {
		t.Error("equal instants must not be 'after' — a tie never displaces live state")
	}

	// A non-MCP row never seeds.
	if _, adopted, err := d.Seed(model.SpecInfo{Format: model.SpecFormatOpenAPI}, []byte("openapi: 3.0.3")); err != nil || adopted {
		t.Errorf("openapi rows must be ignored, got adopted=%v err=%v", adopted, err)
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

// TestSeedTwoFrontsConvergeOnTheEarlierStamp is review finding 3 on the
// baseline seeding (2026-09-08), detector side. Two fronts observe the
// IDENTICAL tools/list, each before its own tick could seed it: front-a at
// t1, front-b at t2. Each forwards a spec_info row carrying its OWN first
// sighting, so the store row alternated t1/t2 on every re-observation — each
// flip re-downloaded the document on every front and moved the UI's "since
// this snapshot" anchor, the NOT CHECKED flip this seeding was built to end.
// Seeding across, both ways, must leave both fronts on t1 and reporting t1 on
// every re-observation from then on, so the row never moves again and nothing
// is re-downloaded.
func TestSeedTwoFrontsConvergeOnTheEarlierStamp(t *testing.T) {
	first := goldenSnapshot(t) // front-a's sighting, at t1
	later := first             // the same list, front-b's sighting, at t2
	later.ObservedAt = "2026-08-24T13:00:00.000Z"
	if !observedAfter(later.ObservedAt, first.ObservedAt) {
		t.Fatalf("fixture: %s must be after %s", later.ObservedAt, first.ObservedAt)
	}
	t1 := first.ObservedAt

	a, b := NewMCPDetector(), NewMCPDetector()
	_, rowA, rawA, err := a.LoadSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	_, rowB, rawB, err := b.LoadSnapshot(later)
	if err != nil {
		t.Fatal(err)
	}
	if rowA.LoadedAt != t1 || rowB.LoadedAt != later.ObservedAt {
		t.Fatalf("each front forwards its own first sighting: a=%s b=%s", rowA.LoadedAt, rowB.LoadedAt)
	}

	// front-a is offered front-b's row: the same list, sighted later — nothing
	// to learn, nothing changes.
	if fs, adopted, err := a.Seed(rowB, rawB); err != nil || adopted || len(fs) != 0 {
		t.Fatalf("a offered the later stamp: adopted=%v findings=%v err=%v, want nothing", adopted, fs, err)
	}
	// front-b is offered front-a's row: the same list, sighted EARLIER —
	// b converges on it. A stamp, not a contract: no findings, no rotation.
	fs, adopted, err := b.Seed(rowA, rawA)
	if err != nil || !adopted || len(fs) != 0 {
		t.Fatalf("b offered the earlier stamp: adopted=%v findings=%v err=%v, want adopted, no findings", adopted, fs, err)
	}
	b.mu.Lock()
	st := b.edges[mcpEdgeRef(first.PeerHost, "client")]
	cur, prev := st.current.Version.ObservedAt, st.previous
	b.mu.Unlock()
	if cur != t1 || prev != nil {
		t.Fatalf("b after convergence: current=%s previous=%v, want %s / nil", cur, prev, t1)
	}

	// From here on both fronts report t1 on every re-observation, so the row
	// they both keep forwarding never moves — and nothing is re-downloaded.
	again := first
	again.ObservedAt = "2026-08-24T14:00:00.000Z"
	for name, d := range map[string]*MCPDetector{"a": a, "b": b} {
		fs, row, raw, err := d.LoadSnapshot(again)
		if err != nil || len(fs) != 0 || row.LoadedAt != t1 {
			t.Fatalf("%s re-observation: findings=%v loaded_at=%s err=%v, want none / %s", name, fs, row.LoadedAt, err, t1)
		}
		// The converged row is nothing more for either to learn: quiescent.
		if fs, adopted, err := d.Seed(row, raw); err != nil || adopted || len(fs) != 0 {
			t.Errorf("%s offered the converged row: adopted=%v findings=%v err=%v, want nothing", name, adopted, fs, err)
		}
		// And the baseline still judges calls, anchored on t1.
		if fs := d.DetectCall(goldenMCPCall(t)); len(fs) != 1 || fs[0].SnapshotObservedAt != t1 {
			t.Errorf("%s judges %+v, want the golden output_mismatch anchored on %s", name, fs, t1)
		}
	}
}

// TestSeedReportsTheChangeItAdopts is review finding 4 on the baseline
// seeding (2026-09-08). front-a holds V1. The server renames a tool, and the
// FIRST front to list it is front-b, which has no baseline yet: it observes
// V2, has nothing to diff, reports nothing, and forwards V2. front-a then
// met V2 as a newer seed and adopted it silently — a rename reported by
// nobody, where front-a used to diff V1→V2 on its own next observation.
// Adopting a newer, different list over a live baseline reports the diff,
// exactly as observing it would have.
func TestSeedReportsTheChangeItAdopts(t *testing.T) {
	v1 := goldenSnapshot(t)
	a := NewMCPDetector()
	if _, _, _, err := a.LoadSnapshot(v1); err != nil {
		t.Fatal(err)
	}

	// What front-b forwarded: the renamed list, first sighted at t2.
	v2JSON := mutateSnapshotJSON(t, v1.SnapshotJSON, func(doc map[string]any) {
		tool(t, doc, "get_balance")["name"] = "get_account_balance"
	})
	row := model.SpecInfo{Integration: v1.Integration, Role: model.SpecRoleProvider, PeerHost: v1.PeerHost,
		Format: model.SpecFormatMCP, LoadedAt: "2026-08-24T13:00:00.000Z"}

	fs, adopted, err := a.Seed(row, []byte(v2JSON))
	if err != nil || !adopted {
		t.Fatalf("seed: adopted=%v err=%v, want adopted", adopted, err)
	}
	if len(fs) == 0 {
		t.Fatal("adopting the rename over a live baseline reported nothing")
	}

	// The very findings observing V2 would have produced — the same
	// signatures, so a front that did observe it dedups into one occurrence
	// more, not a second finding.
	ref := NewMCPDetector()
	if _, _, _, err := ref.LoadSnapshot(v1); err != nil {
		t.Fatal(err)
	}
	v2 := v1
	v2.ObservedAt = row.LoadedAt
	v2.SnapshotJSON = v2JSON
	want, _, _, err := ref.LoadSnapshot(v2)
	if err != nil {
		t.Fatal(err)
	}
	got, exp := sigs(fs), sigs(want)
	sort.Strings(got)
	sort.Strings(exp)
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("seed reported %v, observing reports %v: must be the same findings", got, exp)
	}
	renamed := false
	for _, f := range fs {
		if f.Kind != model.KindDefinitionChange {
			t.Errorf("seed reported a %s finding: %+v", f.Kind, f)
		}
		if f.SnapshotObservedFrom != v1.ObservedAt || f.SnapshotObservedAt != row.LoadedAt {
			t.Errorf("finding spans %s → %s, want %s → %s", f.SnapshotObservedFrom, f.SnapshotObservedAt, v1.ObservedAt, row.LoadedAt)
		}
		if f.Rule == diff.RuleOperationRenamed && f.Endpoint == "get_balance" {
			renamed = true
		}
	}
	if !renamed {
		t.Errorf("no %s finding on get_balance among %v", diff.RuleOperationRenamed, got)
	}

	// The baseline rotated: a client still calling the old name through
	// front-a is a stale_client here too.
	stale := a.DetectCall(mcpCall("call_stale", "get_balance", `{"account_id":"a1"}`, `{}`))
	if len(stale) != 1 || stale[0].Kind != model.KindStaleClient || stale[0].Rule != RuleToolNotListed {
		t.Fatalf("pre-rename name after adoption = %+v, want stale_client tool-not-listed", stale)
	}
	// Offered the same row again (next tick, or re-listed): nothing more.
	if fs, adopted, err := a.Seed(row, []byte(v2JSON)); err != nil || adopted || len(fs) != 0 {
		t.Errorf("re-offered row: adopted=%v findings=%v err=%v, want nothing", adopted, fs, err)
	}
}
