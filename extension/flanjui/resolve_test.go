package flanjui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/integration"
	"github.com/flanj-io/collector/internal/model"
)

// Resolve / Reopen, driven through the real routes and read back through the
// real GET /api/findings join.
//
// The fake store keys findings by id and does no signature dedup, so a "second
// occurrence" or a "second change" here is a re-insert of the SAME id with a
// moved counter or a moved evidence version — which is exactly the row the real
// store produces (internal/store resolution_test.go is the oracle for that half,
// against both backends).

func findingRowsByID(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var body struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("findings: %v (%s)", err, raw)
	}
	out := map[string]map[string]any{}
	for _, f := range body.Findings {
		out[f["id"].(string)] = f
	}
	return out
}

func mkResolveFinding(id, kind, severity, endpoint, rule string, src *string) model.Finding {
	f := model.Finding{SchemaVersion: 1, ID: id, Kind: kind, Severity: severity, Integration: "acme-payments",
		Endpoint: endpoint, Expected: "x", Actual: "y", Rule: rule, SourceCallID: src,
		DetectedAt: "2026-09-20T10:00:01Z", OccurrenceCount: 1}
	f.Signature = f.ComputeSignature()
	return f
}

// TestFindingResolveFlow: every kind a row is drawn for can be resolved — the
// breaking ones included, which the earlier acknowledge refused — the mark is
// local (it works with no control plane at all and never calls one), the note
// is stored floor-passed, and Reopen puts the row back.
func TestFindingResolveFlow(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.ext.cp = nil // LOCAL: the routes must work with no control plane configured.

	callID := "call_mcp_1"
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-09-20T10:00:00Z",
		Integration: "acme-payments", Direction: "client", Method: "tools/call", Route: "/get_balance",
		Transport: "mcp", MCPToolName: "get_balance", RequestBody: "{}", ResponseBody: "{}",
		Redaction: model.Redaction{Patterns: []string{}}})
	live := mkResolveFinding("fnd_live", model.KindLiveVsSpec, model.SeverityBreaking, "GET /v1/orders", "type-mismatch", &callID)
	desc := mkResolveFinding("fnd_desc", model.KindDefinitionChange, model.SeverityWarning, "create_refund", model.RuleDescriptionChanged, nil)
	mism := mkResolveFinding("fnd_mism", model.KindOutputMismatch, model.SeverityBreaking, "get_balance", "type-mismatch", &callID)
	stale := mkResolveFinding("fnd_stale", model.KindStaleClient, model.SeverityWarning, "old_refund", "tool-not-listed", &callID)
	for _, f := range []model.Finding{live, desc, mism, stale} {
		_ = r.st.InsertFinding(f)
	}

	// 1. Resolve a BREAKING live finding, with a note carrying a card number.
	resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_live/resolve", map[string]any{
		"note": "  fixed in the client, test card 4111 1111 1111 1111  ",
	})
	if resp.StatusCode != 200 || out["resolved"] != true || out["resolved_at"] == nil || out["resolved_at"] == "" {
		t.Fatalf("resolve: %d %v", resp.StatusCode, out)
	}
	stored := r.st.resolutions["fnd_live"]
	if strings.Contains(stored.Note, "4111 1111 1111 1111") {
		t.Errorf("the note was stored without passing the redaction floor: %q", stored.Note)
	}
	if !strings.HasPrefix(stored.Note, "fixed in the client") {
		t.Errorf("stored note = %q, want it trimmed and otherwise intact", stored.Note)
	}

	// 2. The join: resolved rows say so, with the note; the others carry nothing.
	_, _, raw := r.do(t, http.MethodGet, "/api/findings", nil)
	rows := findingRowsByID(t, raw)
	if rows["fnd_live"]["resolved"] != true || rows["fnd_live"]["resolved_at"] == nil || rows["fnd_live"]["resolved_note"] != stored.Note {
		t.Errorf("fnd_live not resolved in the join: %v", rows["fnd_live"])
	}
	for _, id := range []string{"fnd_desc", "fnd_mism", "fnd_stale"} {
		for _, k := range []string{"resolved", "resolved_at", "resolved_note", "reopened_after"} {
			if _, has := rows[id][k]; has {
				t.Errorf("%s must not carry %s: %v", id, k, rows[id])
			}
		}
	}
	// The finding itself is untouched — resolving is not deleting or editing.
	if f, ok, _ := r.st.GetFinding("fnd_live"); !ok || f.Actual != live.Actual || f.OccurrenceCount != 1 {
		t.Errorf("finding after resolve = %+v (present %v)", f, ok)
	}

	// 3. Every other row kind resolves too; the body is optional.
	for _, id := range []string{"fnd_desc", "fnd_mism"} {
		if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/"+id+"/resolve", map[string]string{}); resp.StatusCode != 200 || out["resolved"] != true {
			t.Errorf("resolve %s: %d %v", id, resp.StatusCode, out)
		}
	}
	// stale_client is a notice about this deployment's own client, not a row.
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_stale/resolve", map[string]string{}); resp.StatusCode != 403 || out["error"] != "not_resolvable" {
		t.Errorf("resolve stale_client = %d %v, want 403 not_resolvable", resp.StatusCode, out)
	}
	for _, path := range []string{"/api/findings/nope/resolve", "/api/findings/nope/reopen"} {
		if resp, out, _ = r.do(t, http.MethodPost, path, map[string]string{}); resp.StatusCode != 404 || out["error"] != "finding_not_found" {
			t.Errorf("%s: %d %v, want 404 finding_not_found", path, resp.StatusCode, out)
		}
	}

	// 4. A note over the cap is REFUSED, never cut, and nothing is written.
	long := strings.Repeat("n", maxResolveNoteRunes+1)
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_mism/resolve", map[string]string{"note": long}); resp.StatusCode != 400 || out["error"] != "note_too_long" {
		t.Errorf("over-long note: %d %v, want 400 note_too_long", resp.StatusCode, out)
	}
	if got := r.st.resolutions["fnd_mism"].Note; got != "" {
		t.Errorf("a refused note was stored: %q", got)
	}

	// 5. Reopen by hand → the join drops the mark; reopening again is harmless.
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_live/reopen", map[string]string{}); resp.StatusCode != 200 || out["resolved"] != false {
		t.Fatalf("reopen: %d %v", resp.StatusCode, out)
	}
	_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
	if row := findingRowsByID(t, raw)["fnd_live"]; row["resolved"] != nil || row["reopened_after"] != nil {
		t.Errorf("fnd_live still marked after a manual reopen: %v", row)
	}
	if resp, _, _ = r.do(t, http.MethodPost, "/api/findings/fnd_live/reopen", map[string]string{}); resp.StatusCode != 200 {
		t.Errorf("re-reopen: %d", resp.StatusCode)
	}

	// 6. With no control plane the RELAY routes still 503 — only resolve and
	// reopen are local — and nothing here ever reached one.
	if resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_mism", "allowed_domains": []string{"acme-payments.test"}}); resp.StatusCode != 503 || out["error"] != "cp_not_configured" {
		t.Errorf("flag with no cp: %d %v", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 || r.cp.registerCalls != 0 {
		t.Errorf("the resolve flow must never touch the control plane (flags=%d registers=%d)", r.cp.flagCalls, r.cp.registerCalls)
	}
}

// TestResolvedFindingReopensOnRecurrence — THE SAFETY PROPERTY, occurrence arm:
// one more occurrence than was resolved, and the row is open again under the
// same id, saying when it had been resolved.
func TestResolvedFindingReopensOnRecurrence(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.ext.cp = nil

	callID := "call_1"
	f := mkResolveFinding("fnd_live", model.KindLiveVsSpec, model.SeverityBreaking, "GET /v1/orders", "type-mismatch", &callID)
	f.OccurrenceCount = 5
	_ = r.st.InsertFinding(f)

	resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_live/resolve", map[string]any{"seen_occurrence_count": 5})
	if resp.StatusCode != 200 || out["resolved"] != true {
		t.Fatalf("resolve: %d %v", resp.StatusCode, out)
	}
	resolvedAt, _ := out["resolved_at"].(string)

	f.OccurrenceCount = 6 // the drift happened again
	_ = r.st.InsertFinding(f)
	_, _, raw := r.do(t, http.MethodGet, "/api/findings", nil)
	row := findingRowsByID(t, raw)["fnd_live"]
	if row["resolved"] == true {
		t.Fatalf("A RESOLUTION IS HIDING NEW TROUBLE: the finding recurred and still reads resolved: %v", row)
	}
	if row["reopened_after"] != resolvedAt {
		t.Errorf("reopened_after = %v, want %q — the row must be able to say why it is back", row["reopened_after"], resolvedAt)
	}
	if _, has := row["resolved_note"]; has {
		t.Errorf("an open row must not carry a resolution's note: %v", row)
	}

	// The count the operator SAW bounds what they resolved. The store holds 6;
	// the row on screen said 5; the sixth occurrence was never seen, so the
	// press does not cover it and the row stays open.
	resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_live/resolve", map[string]any{"seen_occurrence_count": 5})
	if resp.StatusCode != 200 || out["resolved"] != false {
		t.Fatalf("resolve with a stale count: %d %v, want 200 resolved:false", resp.StatusCode, out)
	}
	// A count the browser inflates can never widen it past what the store holds.
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_live/resolve", map[string]any{"seen_occurrence_count": 9000}); resp.StatusCode != 200 || out["resolved"] != true {
		t.Fatalf("resolve: %d %v", resp.StatusCode, out)
	}
	if got := r.st.resolutions["fnd_live"].OccurrenceCount; got != 6 {
		t.Errorf("covered occurrences = %d, want 6 — never more than the store held", got)
	}
}

// TestResolvedChangeResurfacesOnNewEvidence — THE SAFETY PROPERTY, evidence arm,
// for each evidence-versioned kind. The signature is identical for a first and a
// second change to the same field, so a resolution keyed on the row alone would
// cover the second one unseen.
func TestResolvedChangeResurfacesOnNewEvidence(t *testing.T) {
	callID := "call_1"
	cases := []struct {
		name string
		mk   func(version string) model.Finding
	}{
		{"definition_change", func(v string) model.Finding {
			f := mkResolveFinding("fnd_x", model.KindDefinitionChange, model.SeverityWarning, "create_refund", model.RuleDescriptionChanged, nil)
			f.SpecVersionTo = model.Ptr("sha256:" + v)
			return f
		}},
		{"version-diff", func(v string) model.Finding {
			f := mkResolveFinding("fnd_x", model.KindVersionDiff, model.SeverityBreaking, "GET /v1/orders", "response-property-removed", nil)
			f.SpecVersionTo = model.Ptr(v)
			return f
		}},
		{"deprecation (contract diff)", func(v string) model.Finding {
			f := mkResolveFinding("fnd_x", model.KindDeprecation, model.SeverityWarning, "GET /v1/orders", "endpoint-deprecated", nil)
			f.SpecVersionTo = model.Ptr(v)
			return f
		}},
		{"deprecation (live traffic)", func(v string) model.Finding {
			f := mkResolveFinding("fnd_x", model.KindDeprecation, model.SeverityWarning, "GET /v1/orders", "deprecated-operation", &callID)
			f.Actual = "deprecated, sunset " + v
			return f
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.start(t)
			r.ext.cp = nil

			first, second := tc.mk("2026-12-31"), tc.mk("2026-10-01")
			if first.Signature != second.Signature {
				t.Fatalf("test premise broken: signatures differ (%q vs %q)", first.Signature, second.Signature)
			}
			_ = r.st.InsertFinding(first)
			if resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_x/resolve", map[string]string{}); resp.StatusCode != 200 || out["resolved"] != true {
				t.Fatalf("resolve: %d %v", resp.StatusCode, out)
			}

			// More of the SAME evidence is not news. For the live-traffic
			// deprecation this is the whole point: its count rises with every
			// call for as long as the deprecation window lasts.
			again := first
			again.OccurrenceCount = 400
			_ = r.st.InsertFinding(again)
			_, _, raw := r.do(t, http.MethodGet, "/api/findings", nil)
			if row := findingRowsByID(t, raw)["fnd_x"]; row["resolved"] != true {
				t.Fatalf("the same evidence, seen again, reopened the row: %v", row)
			}

			_ = r.st.InsertFinding(second)
			_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
			row := findingRowsByID(t, raw)["fnd_x"]
			if row["resolved"] == true {
				t.Fatalf("NEW EVIDENCE ARRIVED ALREADY RESOLVED: %v", row)
			}
			if row["reopened_after"] == nil {
				t.Errorf("the re-surfaced row must say it had been resolved: %v", row)
			}
		})
	}
}

// TestSyncedShapeCarriesTheResolutionOnlyWhileCovered: what the dashboard hears
// is this collector's answer as of this tick — resolved, with the note as it was
// STORED (floor-passed), while covered; open, and with no note, the tick after
// the trouble returns.
func TestSyncedShapeCarriesTheResolutionOnlyWhileCovered(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)

	callID := "call_1"
	f := mkResolveFinding("fnd_sync", model.KindLiveVsSpec, model.SeverityBreaking, "GET /v1/orders", "type-mismatch", &callID)
	_ = r.st.InsertFinding(f)
	if resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_sync/resolve", map[string]string{"note": "NOTE_SENTINEL card 4111 1111 1111 1111"}); resp.StatusCode != 200 {
		t.Fatalf("resolve: %d %v", resp.StatusCode, out)
	}

	syncedRow := func() (map[string]any, string) {
		t.Helper()
		r.ext.syncFindingsOnce(context.Background())
		body := r.cp.findingsBody(r.cp.findingsCallCount() - 1)
		var req struct {
			Findings []map[string]any `json:"findings"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("sync body: %v (%s)", err, body)
		}
		for _, row := range req.Findings {
			if row["finding_id"] == "fnd_sync" {
				return row, string(body)
			}
		}
		t.Fatalf("fnd_sync was not synced: %s", body)
		return nil, ""
	}

	row, wire := syncedRow()
	if row["resolved_at"] == nil || row["resolved_at"] == "" {
		t.Errorf("a resolved finding synced without resolved_at: %v", row)
	}
	if note, _ := row["resolved_note"].(string); !strings.HasPrefix(note, "NOTE_SENTINEL") {
		t.Errorf("resolved_note = %q, want the operator's note", note)
	}
	if strings.Contains(wire, "4111 1111 1111 1111") {
		t.Errorf("the note crossed WITHOUT passing the redaction floor: %s", wire)
	}

	f.OccurrenceCount = 2
	_ = r.st.InsertFinding(f)
	row, wire = syncedRow()
	if row["resolved_at"] != nil {
		t.Errorf("a finding that recurred still synced as resolved (%v) — the dashboard would mute live trouble", row["resolved_at"])
	}
	if strings.Contains(wire, "NOTE_SENTINEL") {
		t.Errorf("a lapsed resolution's note still crossed: %s", wire)
	}
}

// TestEdgeRowsCountOpenDriftFindings: an edge row's DRIFTED chip is drawn from a
// cumulative tally of drifted calls, which no resolution lowers. The Overview
// headline counts OPEN findings. Without open_drift_findings the two disagree the
// moment a finding is resolved — "No drift detected" over a row saying drifted.
func TestEdgeRowsCountOpenDriftFindings(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.ext.cp = nil
	const host = "api.orders.test"
	r.st.edges = []model.Edge{
		{PeerHost: host, Direction: "client", Role: "consumer", Class: "external", CallCount: 9, DriftCount: 3},
		{PeerHost: "api.quiet.test", Direction: "client", Role: "consumer", Class: "external", CallCount: 4},
	}

	callID := "call_orders_1"
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-09-20T10:00:00Z",
		Integration: integration.ForHost(host), Direction: "client", Method: "GET", Route: "/v1/orders", PeerHost: host,
		RequestBody: "{}", ResponseBody: "{}", Redaction: model.Redaction{Patterns: []string{}}})
	live := mkResolveFinding("fnd_orders", model.KindLiveVsSpec, model.SeverityBreaking, "GET /v1/orders", "type-mismatch", &callID)
	live.Integration = integration.ForHost(host)
	_ = r.st.InsertFinding(live)
	// A version diff on the same host is not a drifted CALL and must never count.
	diff := mkResolveFinding("fnd_diff", model.KindVersionDiff, model.SeverityBreaking, "GET /v1/orders", "response-property-removed", nil)
	diff.Integration = integration.ForHost(host)
	_ = r.st.InsertFinding(diff)

	openOn := func() map[string]any {
		t.Helper()
		_, _, raw := r.do(t, http.MethodGet, "/api/edges", nil)
		var body struct {
			Outbound []map[string]any `json:"outbound"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("edges: %v (%s)", err, raw)
		}
		out := map[string]any{}
		for _, e := range body.Outbound {
			out[e["peer_host"].(string)] = e["open_drift_findings"]
		}
		return out
	}

	if got := openOn(); got[host] != float64(1) || got["api.quiet.test"] != float64(0) {
		t.Fatalf("open drift findings = %v, want 1 on %s and 0 on the quiet edge", got, host)
	}
	if resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_orders/resolve", map[string]string{}); resp.StatusCode != 200 {
		t.Fatalf("resolve: %d %v", resp.StatusCode, out)
	}
	if got := openOn(); got[host] != float64(0) {
		t.Fatalf("open drift findings after resolve = %v, want 0 — the chip must be able to stand down", got[host])
	}
	// It recurs: open again, and the edge says so.
	live.OccurrenceCount = 2
	_ = r.st.InsertFinding(live)
	if got := openOn(); got[host] != float64(1) {
		t.Fatalf("open drift findings after a recurrence = %v, want 1", got[host])
	}
	// The source call ages out (a flagged finding's call is unpinned): the
	// finding still belongs to its edge, through the integration its host derives.
	delete(r.st.calls, callID)
	if got := openOn(); got[host] != float64(1) {
		t.Fatalf("open drift findings with the source call gone = %v, want 1", got[host])
	}
}
