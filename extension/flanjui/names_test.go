package flanjui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// Locked accessors for the stub's directory counters (the sync ticker and the
// test body would race otherwise — same discipline as sync_test.go).
func (s *stubCP) submissionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.submissionBodies)
}

func (s *stubCP) submissionBody(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submissionBodies[i]
}

func (s *stubCP) directoryCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.directoryCalls
}

func (s *stubCP) directoryINM(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.directoryINMs[i]
}

// seedOutboundEdge registers a discovered external OUTBOUND edge in the fake
// store — the row a rename targets.
func seedOutboundEdge(r *testRig, host string) {
	r.st.mu.Lock()
	defer r.st.mu.Unlock()
	r.st.edges = append(r.st.edges, model.Edge{
		PeerHost: host, Direction: "client", Role: "consumer", Class: "external",
		FirstSeen: "2026-08-30T08:00:00Z", LastSeen: "2026-08-30T09:00:00Z", CallCount: 3,
	})
}

// edgeRowFor fetches GET /api/edges and returns the row for host.
func edgeRowFor(t *testing.T, r *testRig, host string) map[string]any {
	t.Helper()
	resp, out, raw := r.do(t, http.MethodGet, "/api/edges", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/edges: %d %s", resp.StatusCode, raw)
	}
	rows, _ := out["edges"].([]any)
	for _, x := range rows {
		row, _ := x.(map[string]any)
		if row["peer_host"] == host {
			return row
		}
	}
	t.Fatalf("no edge row for %q in %s", host, raw)
	return nil
}

// TestEdgeNameSaveAndOptInOnly: a rename persists (source `user`, keyed by the
// registrable domain) and — the non-negotiable — NOTHING reaches the CP when
// suggest is false: zero submission POSTs, asserted at the wire.
func TestEdgeNameSaveAndOptInOnly(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedOutboundEdge(r, "api.zzguava.dev")

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava Billing", "suggest": false})
	if resp.StatusCode != 200 || out["saved"] != true || out["suggested"] != false {
		t.Fatalf("save: %d %s", resp.StatusCode, raw)
	}
	edge, _ := out["edge"].(map[string]any)
	if edge["registrable_domain"] != "zzguava.dev" || edge["display_name"] != "Guava Billing" || edge["name_source"] != "user" {
		t.Errorf("response row = %v", edge)
	}
	row := edgeRowFor(t, r, "api.zzguava.dev")
	if row["display_name"] != "Guava Billing" || row["name_source"] != "user" || row["registrable_domain"] != "zzguava.dev" {
		t.Errorf("listed row = %v", row)
	}
	if r.cp.submissionCount() != 0 {
		t.Fatalf("suggest=false must never POST a submission; got %d", r.cp.submissionCount())
	}
}

// TestEdgeNameClearReturnsNextTier: clearing a `user` name (empty name = the
// tombstone) drops the row to the next tier down — the baked seed for a
// seeded domain, auto for an unknown one.
func TestEdgeNameClearReturnsNextTier(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedOutboundEdge(r, "api.stripe.com")  // stripe.com is in the baked seed
	seedOutboundEdge(r, "api.zzguava.dev") // not in the seed

	for _, host := range []string{"api.stripe.com", "api.zzguava.dev"} {
		resp, _, raw := r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": host, "name": "Renamed"})
		if resp.StatusCode != 200 {
			t.Fatalf("save %s: %d %s", host, resp.StatusCode, raw)
		}
		resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": host, "name": ""})
		if resp.StatusCode != 200 || out["saved"] != true {
			t.Fatalf("clear %s: %d %s", host, resp.StatusCode, raw)
		}
	}
	if row := edgeRowFor(t, r, "api.stripe.com"); row["display_name"] != "Stripe" || row["name_source"] != "directory" {
		t.Errorf("seeded domain after clear = %v, want the seed name at the directory tier", row)
	}
	if row := edgeRowFor(t, r, "api.zzguava.dev"); row["display_name"] != "" || row["name_source"] != "auto" {
		t.Errorf("unknown domain after clear = %v, want empty name at the auto tier", row)
	}
}

// TestEdgeNamePrecedence: named-by-you > config > directory > auto, on one
// listing. The config tier resolves through the spec_infos linkage (the
// singular v0 model: the configured integration's spec names its peer_host).
func TestEdgeNamePrecedence(t *testing.T) {
	r := newRig(t)
	r.ext.cfg.ProviderDisplayName = "Acme Payments"
	r.start(t)
	// The config linkage points at api.stripe.com — which is ALSO in the seed,
	// so config-beats-directory is observable on the same row.
	_ = r.st.PutSpecInfo(model.SpecInfo{Integration: "acme-payments", Role: "provider", PeerHost: "api.stripe.com"}, nil)
	seedOutboundEdge(r, "api.stripe.com")
	seedOutboundEdge(r, "api.adyen.com")   // seed only → directory tier
	seedOutboundEdge(r, "api.zzguava.dev") // nowhere → auto tier

	if row := edgeRowFor(t, r, "api.stripe.com"); row["display_name"] != "Acme Payments" || row["name_source"] != "config" {
		t.Errorf("config tier = %v, want the YAML name over the seed", row)
	}
	if row := edgeRowFor(t, r, "api.adyen.com"); row["display_name"] != "Adyen" || row["name_source"] != "directory" {
		t.Errorf("directory tier = %v", row)
	}
	if row := edgeRowFor(t, r, "api.zzguava.dev"); row["display_name"] != "" || row["name_source"] != "auto" {
		t.Errorf("auto tier = %v", row)
	}
	// A UI rename beats the config tier.
	if resp, _, raw := r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": "api.stripe.com", "name": "Our PSP"}); resp.StatusCode != 200 {
		t.Fatalf("rename: %d %s", resp.StatusCode, raw)
	}
	if row := edgeRowFor(t, r, "api.stripe.com"); row["display_name"] != "Our PSP" || row["name_source"] != "user" {
		t.Errorf("user tier = %v, want the rename over config", row)
	}
	// Inbound rows never resolve a name (outbound only — ruling 6).
	r.st.mu.Lock()
	r.st.edges = append(r.st.edges, model.Edge{PeerHost: "api.stripe.com", Direction: "server", Role: "provider", Class: "external"})
	r.st.mu.Unlock()
	_, out, _ := r.do(t, http.MethodGet, "/api/edges", nil)
	for _, x := range out["inbound"].([]any) {
		row := x.(map[string]any)
		if row["display_name"] != "" || row["name_source"] != "auto" {
			t.Errorf("inbound row resolved a name: %v", row)
		}
	}
}

// TestEdgeNameRedactionFloor: a rename passes the SAME floor Connect display
// names pass — a PAN never persists, and never rides an opt-in suggestion.
func TestEdgeNameRedactionFloor(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedOutboundEdge(r, "api.zzguava.dev")
	const pan = "4242424242424242"

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava " + pan + " Ltd", "suggest": true})
	if resp.StatusCode != 200 || out["saved"] != true {
		t.Fatalf("save: %d %s", resp.StatusCode, raw)
	}
	r.st.mu.Lock()
	for k, v := range r.st.settings {
		if strings.Contains(v, pan) {
			t.Errorf("PAN persisted under %q: %q", k, v)
		}
	}
	r.st.mu.Unlock()
	row := edgeRowFor(t, r, "api.zzguava.dev")
	name, _ := row["display_name"].(string)
	if strings.Contains(name, pan) || !strings.Contains(name, "⟦REDACTED:") {
		t.Errorf("resolved name not tokenised: %q", name)
	}
	if r.cp.submissionCount() != 1 {
		t.Fatalf("submissions = %d, want 1", r.cp.submissionCount())
	}
	if wire := string(r.cp.submissionBody(0)); strings.Contains(wire, pan) {
		t.Errorf("PAN reached the directory submission wire: %s", wire)
	}
}

// TestEdgeNameSuggestSendsMapping: WITH the opt-in and a Connected collector,
// exactly {domain, name} goes out with the collector key — nothing else.
func TestEdgeNameSuggestSendsMapping(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedOutboundEdge(r, "api.zzguava.dev")

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava Billing", "suggest": true})
	if resp.StatusCode != 200 || out["saved"] != true || out["suggested"] != true {
		t.Fatalf("save+suggest: %d %s", resp.StatusCode, raw)
	}
	if r.cp.submissionCount() != 1 {
		t.Fatalf("submissions = %d, want 1", r.cp.submissionCount())
	}
	var body map[string]any
	if err := json.Unmarshal(r.cp.submissionBody(0), &body); err != nil {
		t.Fatal(err)
	}
	if body["domain"] != "zzguava.dev" || body["name"] != "Guava Billing" || len(body) != 2 {
		t.Errorf("submission body = %v, want exactly {domain, name}", body)
	}
	if r.cp.lastAuth != "Bearer "+r.cp.collectorKey {
		t.Errorf("submission must carry the collector key, got %q", r.cp.lastAuth)
	}
	r.assertNeverLogged(t, r.cp.collectorKey)
}

// TestEdgeNameSuggestFailureKeepsSave: a CP failure on the suggestion NEVER
// fails the save — 200, suggested=false, the distinct copy string, and the
// name is persisted.
func TestEdgeNameSuggestFailureKeepsSave(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedOutboundEdge(r, "api.zzguava.dev")
	r.cp.mu.Lock()
	r.cp.submissionStatus = 500
	r.cp.mu.Unlock()

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava Billing", "suggest": true})
	if resp.StatusCode != 200 || out["saved"] != true || out["suggested"] != false {
		t.Fatalf("save with failed suggest: %d %s", resp.StatusCode, raw)
	}
	if out["message"] != msgNameSavedSuggestFailed {
		t.Errorf("message = %v, want the distinct partial-success copy", out["message"])
	}
	if row := edgeRowFor(t, r, "api.zzguava.dev"); row["display_name"] != "Guava Billing" || row["name_source"] != "user" {
		t.Errorf("name not persisted after suggest failure: %v", row)
	}
}

// TestEdgeNameSuggestRefused400: a CP 400 on the suggestion is the directory
// normalizer REFUSING the name — still 200/saved/suggested=false, but the
// message relays the CP's one-sentence reason instead of the transport copy.
func TestEdgeNameSuggestRefused400(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedOutboundEdge(r, "api.zzguava.dev")
	r.cp.mu.Lock()
	r.cp.submissionStatus = 400
	r.cp.submissionMessage = "That name is reserved."
	r.cp.mu.Unlock()

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava Billing", "suggest": true})
	if resp.StatusCode != 200 || out["saved"] != true || out["suggested"] != false {
		t.Fatalf("save with refused suggest: %d %s", resp.StatusCode, raw)
	}
	if want := "Name saved. The directory didn't take the suggestion: That name is reserved."; out["message"] != want {
		t.Errorf("message = %v, want %q", out["message"], want)
	}
	if row := edgeRowFor(t, r, "api.zzguava.dev"); row["display_name"] != "Guava Billing" || row["name_source"] != "user" {
		t.Errorf("name not persisted after refused suggestion: %v", row)
	}

	// A 400 WITHOUT a decodable message falls back to the transport copy —
	// never an empty reason after the colon.
	r2 := newRig(t)
	r2.start(t)
	connectKeyOnly(t, r2)
	seedOutboundEdge(r2, "api.zzguava.dev")
	r2.cp.mu.Lock()
	r2.cp.submissionStatus = 400
	r2.cp.submissionMessage = " "
	r2.cp.mu.Unlock()
	_, out2, _ := r2.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava Billing", "suggest": true})
	if out2["message"] != msgNameSavedSuggestFailed {
		t.Errorf("message-less 400 = %v, want the it-stays-local copy", out2["message"])
	}
}

// TestEdgeNameSuggestUnconnected: no collector key — the local save still
// lands (200), nothing goes out, and the answer says the suggestion stayed
// local.
func TestEdgeNameSuggestUnconnected(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedOutboundEdge(r, "api.zzguava.dev")

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/name",
		map[string]any{"host": "api.zzguava.dev", "name": "Guava Billing", "suggest": true})
	if resp.StatusCode != 200 || out["saved"] != true || out["suggested"] != false {
		t.Fatalf("un-Connected save: %d %s", resp.StatusCode, raw)
	}
	if out["message"] != msgNameSavedSuggestFailed {
		t.Errorf("message = %v", out["message"])
	}
	if r.cp.submissionCount() != 0 {
		t.Fatalf("un-Connected suggest must not POST; got %d", r.cp.submissionCount())
	}
	if row := edgeRowFor(t, r, "api.zzguava.dev"); row["display_name"] != "Guava Billing" {
		t.Errorf("name not saved: %v", row)
	}
}

// TestEdgeNameValidation: unknown host → 404 (outbound rows only); a name over
// the cap → 400; a missing host → 400; non-POST / missing UI header → the
// standard guards.
func TestEdgeNameValidation(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedOutboundEdge(r, "api.zzguava.dev")

	resp, out, _ := r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": "api.nobody.dev", "name": "X"})
	if resp.StatusCode != 404 || out["error"] != "edge_not_found" {
		t.Errorf("unknown host: %d %v", resp.StatusCode, out)
	}
	// An inbound-only host is not renameable either (outbound rows only).
	r.st.mu.Lock()
	r.st.edges = append(r.st.edges, model.Edge{PeerHost: "in.zzcaller.dev", Direction: "server", Role: "provider", Class: "external"})
	r.st.mu.Unlock()
	resp, out, _ = r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": "in.zzcaller.dev", "name": "X"})
	if resp.StatusCode != 404 || out["error"] != "edge_not_found" {
		t.Errorf("inbound host: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": "api.zzguava.dev", "name": strings.Repeat("x", 81)})
	if resp.StatusCode != 400 || out["error"] != "name_too_long" {
		t.Errorf("over-cap name: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": "", "name": "X"})
	if resp.StatusCode != 400 || out["error"] != "missing_fields" {
		t.Errorf("missing host: %d %v", resp.StatusCode, out)
	}
	resp, _, _ = r.do(t, http.MethodGet, "/api/edges/name", nil)
	if resp.StatusCode != 405 {
		t.Errorf("GET = %d, want 405", resp.StatusCode)
	}
	resp, out, _ = r.do(t, http.MethodPost, "/api/edges/name", map[string]any{"host": "api.zzguava.dev", "name": "X"},
		func(req *http.Request) { req.Header.Del("X-Flanj-UI") })
	if resp.StatusCode != 403 || out["error"] != "ui_header_required" {
		t.Errorf("missing UI header: %d %v", resp.StatusCode, out)
	}
}
