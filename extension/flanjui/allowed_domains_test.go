package flanjui

import (
	"fmt"
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
	if e, has := r.cp.lastFlagBody["allowed_emails"]; !has || e != nil {
		t.Errorf("allowed_emails must be on the wire too, as null, for Anyone with the link: present=%v value=%v", has, e)
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
		{"a list of non-strings", map[string]any{"finding_id": "fnd_1", "allowed_domains": []int{42}}, "invalid_domain"},
		{"a string beside a number", map[string]any{"finding_id": "fnd_1", "allowed_domains": []any{"acme.test", 5}}, "invalid_domain"},
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
	// The relay answers with the CP's sentences word for word: a list with a non-string entry is a bad
	// entry, never "must be a list" (false about a list), and "anyone with the link" is not a label here.
	_, out, _ := r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_1", "allowed_domains": []any{"acme.test", 5}})
	if out["message"] != msgOpenToInvalid {
		t.Errorf("a non-string entry: %v, want %q", out, msgOpenToInvalid)
	}
	_, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_1", "allowed_domains": "acme.test"})
	if out["message"] != "allowed_domains must be a list of domains, or null for anyone with the link." {
		t.Errorf("not a list: %v", out)
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

// Three modes (2026-09-15): specific people travel as allowed_emails, normalized,
// with allowed_domains present and null — the relay always forwards both keys.
func TestFlagSpecificPeopleReachTheWire(t *testing.T) {
	r := connectedRig(t)

	resp, _, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{
		"finding_id":     "fnd_1",
		"allowed_emails": []string{" Dana@Acme-Payments.test ", "Dana Reyes <dana@acme-payments.test>", "lee@eu.acme-payments.test"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("flag: %d %s", resp.StatusCode, raw)
	}
	got, _ := r.cp.lastFlagBody["allowed_emails"].([]any)
	if want := []any{"dana@acme-payments.test", "lee@eu.acme-payments.test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("allowed_emails on the wire = %v, want %v", got, want)
	}
	if d, has := r.cp.lastFlagBody["allowed_domains"]; !has || d != nil {
		t.Errorf("allowed_domains must be on the wire as null beside a people list: present=%v value=%v", has, d)
	}
}

func TestFlagRefusesAConflictOrABadPeopleList(t *testing.T) {
	r := connectedRig(t)
	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"both lists", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{"acme-payments.test"}, "allowed_emails": []string{"dana@acme-payments.test"}}, "access_conflict"},
		{"empty people", map[string]any{"finding_id": "fnd_1", "allowed_emails": []string{}}, "allowed_emails_empty"},
		{"not an address", map[string]any{"finding_id": "fnd_1", "allowed_emails": []string{"dana"}}, "invalid_email"},
		{"two @", map[string]any{"finding_id": "fnd_1", "allowed_emails": []string{"a@b@acme.test"}}, "invalid_email"},
		{"people not a list", map[string]any{"finding_id": "fnd_1", "allowed_emails": "dana@acme-payments.test"}, "bad_request"},
		{"a null among the people", map[string]any{"finding_id": "fnd_1", "allowed_emails": []any{"dana@acme-payments.test", nil}}, "invalid_email"},
		{"a name with empty brackets", map[string]any{"finding_id": "fnd_1", "allowed_emails": []string{"dana@acme-payments.test", "Dana <>"}}, "invalid_email"},
		{"21 domains", map[string]any{"finding_id": "fnd_1", "allowed_domains": manyOf(21, "d%d.test")}, "bad_request"},
		{"21 people", map[string]any{"finding_id": "fnd_1", "allowed_emails": manyOf(21, "p%d@acme.test")}, "bad_request"},
	}
	for _, c := range cases {
		before := r.cp.flagCalls
		resp, out, _ := r.do(t, http.MethodPost, "/api/flag", c.body)
		if resp.StatusCode != http.StatusBadRequest || out["error"] != c.code {
			t.Errorf("%s: %d %v, want 400 %s", c.name, resp.StatusCode, out, c.code)
		}
		if r.cp.flagCalls != before {
			t.Errorf("%s: a refused Open to must not leave the collector", c.name)
		}
	}
	// One list and the other key null is a choice, not a conflict.
	resp, _, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_1", "allowed_domains": nil, "allowed_emails": []string{"dana@acme-payments.test"}})
	if resp.StatusCode != 201 {
		t.Errorf("a people list beside a null domains key: %d %s", resp.StatusCode, raw)
	}
}

func TestEdgeThreadSendsSpecificPeople(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")
	resp, _, raw := r.do(t, http.MethodPost, "/api/edges/thread", map[string]any{
		"host": "api.globex.test", "message": "hello", "request_id": "req-1", "allowed_emails": []string{"Kai@Globex.test"},
	})
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		t.Fatalf("POST /api/edges/thread: %d %s", resp.StatusCode, raw)
	}
	if got, _ := r.cp.lastFlagBody["allowed_emails"].([]any); !reflect.DeepEqual(got, []any{"kai@globex.test"}) {
		t.Errorf("allowed_emails on the wire = %v, want [kai@globex.test]", got)
	}
}

func manyOf(n int, format string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(format, i)
	}
	return out
}
