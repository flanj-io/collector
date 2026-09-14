package flanjui

import (
	"net/http"
	"reflect"
	"testing"
)

// Who may OPEN a thread (thread-domain-gate, 2026-09-14). The sheet's "Open to"
// choice is REQUIRED on both thread-creating relay routes, normalized here the
// way the CP normalizes it, and put on the wire as `allowed_domains` — a list,
// or an explicit JSON null for "Anyone with the link". Never absent.

func TestFlagAllowedDomainsReachTheWireNormalized(t *testing.T) {
	r := connectedRig(t)

	resp, _, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{
		"finding_id":      "fnd_1",
		"allowed_domains": []string{" Acme-Payments.test ", "@acme-payments.test", "globex.test."},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("flag: %d %s", resp.StatusCode, raw)
	}
	got, _ := r.cp.lastFlagBody["allowed_domains"].([]any)
	want := []any{"acme-payments.test", "globex.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("allowed_domains on the wire = %v, want %v (trimmed, lower-cased, de-duplicated, @ and trailing dot stripped)", got, want)
	}
}

func TestFlagAnyoneWithTheLinkIsAnExplicitNull(t *testing.T) {
	r := connectedRig(t)

	resp, _, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_1", "allowed_domains": nil})
	if resp.StatusCode != 201 {
		t.Fatalf("flag: %d %s", resp.StatusCode, raw)
	}
	v, present := r.cp.lastFlagBody["allowed_domains"]
	if !present {
		t.Fatalf("allowed_domains must ALWAYS be on the wire — an absent field is the pre-field collector's shape: %v", r.cp.lastFlagBody)
	}
	if v != nil {
		t.Errorf("Anyone with the link is JSON null, got %v", v)
	}
}

func TestFlagRefusesAMissingOrEmptyOpenTo(t *testing.T) {
	r := connectedRig(t)
	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"absent", map[string]any{"finding_id": "fnd_1"}, "missing_fields"},
		{"empty list", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{}}, "allowed_domains_empty"},
		{"blank entries", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{" ", "@"}}, "allowed_domains_empty"},
		{"not a domain", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{"https://acme.test/x"}}, "invalid_domain"},
		{"an address", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{"dana@acme.test"}}, "invalid_domain"},
		{"not a list", map[string]any{"finding_id": "fnd_1", "allowed_domains": "acme.test"}, "bad_request"},
		{"a list of non-strings", map[string]any{"finding_id": "fnd_1", "allowed_domains": []int{42}}, "bad_request"},
	}
	for _, c := range cases {
		before := r.cp.flagCalls
		resp, out, _ := r.do(t, http.MethodPost, "/api/flag", c.body)
		if resp.StatusCode != http.StatusBadRequest || out["error"] != c.code {
			t.Errorf("%s: %d %v, want 400 %s", c.name, resp.StatusCode, out, c.code)
		}
		if msg, _ := out["message"].(string); msg == "" {
			t.Errorf("%s: the refusal carries no sentence", c.name)
		}
		if r.cp.flagCalls != before {
			t.Errorf("%s: a refused Open to must not leave the collector", c.name)
		}
	}
}

func TestEdgeThreadRequiresOpenToAndSendsIt(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")

	resp, out, _ := r.do(t, http.MethodPost, "/api/edges/thread", map[string]any{
		"host": "api.globex.test", "message": "hello", "request_id": "req-1",
	})
	if resp.StatusCode != http.StatusBadRequest || out["error"] != "missing_fields" {
		t.Errorf("no Open to = %d %v, want 400 missing_fields", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("a refused Open to must not leave the collector")
	}

	resp, _, raw := r.do(t, http.MethodPost, "/api/edges/thread", map[string]any{
		"host": "api.globex.test", "message": "hello", "request_id": "req-1", "allowed_domains": []string{"Globex.test"},
	})
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		t.Fatalf("POST /api/edges/thread: %d %s", resp.StatusCode, raw)
	}
	if got, _ := r.cp.lastFlagBody["allowed_domains"].([]any); !reflect.DeepEqual(got, []any{"globex.test"}) {
		t.Errorf("allowed_domains on the wire = %v, want [globex.test]", got)
	}
}

// The "Open to" prefill: the sheet asks whether the provider host's registrable
// domain is a CLAIMED directory entry (D5 domain proof). A curated name is a
// label, not a proof, and prefills nothing.
func TestDirectoryHint(t *testing.T) {
	r := newRig(t)
	r.startBare(t)
	_ = r.st.PutSetting(settingDirectoryTable, `{"entries":{"claimed.test":{"name":"Claimed Co","tier":"claimed"},"zzcurated.test":{"name":"Curated Co","tier":"curated"}},"count":2}`)

	resp, out, _ := r.do(t, http.MethodGet, "/api/directory/hint?host=api.claimed.test", nil)
	if resp.StatusCode != 200 || out["domain"] != "claimed.test" || out["claimed"] != true || out["tier"] != "claimed" || out["name"] != "Claimed Co" {
		t.Errorf("claimed host: %d %v", resp.StatusCode, out)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the hint must not be cached: %q", resp.Header.Get("Cache-Control"))
	}
	resp, out, _ = r.do(t, http.MethodGet, "/api/directory/hint?host=api.zzcurated.test", nil)
	if resp.StatusCode != 200 || out["claimed"] != false || out["tier"] != "curated" {
		t.Errorf("curated host: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodGet, "/api/directory/hint?host=api.unknown.test", nil)
	if resp.StatusCode != 200 || out["claimed"] != false || out["tier"] != nil || out["name"] != nil || out["domain"] != "unknown.test" {
		t.Errorf("unknown host: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodGet, "/api/directory/hint", nil)
	if resp.StatusCode != http.StatusBadRequest || out["error"] != "missing_fields" {
		t.Errorf("no host: %d %v", resp.StatusCode, out)
	}
	resp, _, _ = r.do(t, http.MethodPost, "/api/directory/hint?host=api.claimed.test", map[string]any{})
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", resp.StatusCode)
	}
}
