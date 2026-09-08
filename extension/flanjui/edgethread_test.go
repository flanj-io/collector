package flanjui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// connectedRig: started, Connected, with a confirmed contact — the state every
// thread-creating route requires. The 412 paths get their own test below.
func connectedRig(t *testing.T) *testRig {
	t.Helper()
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme Consumer Ltd",
		ContactEmail: "ops@acme.test", ContactStatus: "confirmed", ConfirmedContactEmail: "ops@acme.test"})
	r.cp.mu.Lock()
	r.cp.contactEmail, r.cp.contactStatus, r.cp.confirmedEmail = "ops@acme.test", "confirmed", "ops@acme.test"
	r.cp.mu.Unlock()
	return r
}

// TestEdgeThreadCreatesMessageOnlyThread — v1 phase 4, case 1. "Start a thread"
// on an edge row sends a MESSAGE-ONLY flag: no `call`, no `finding`, and a
// `provider_host` naming the edge so the thread page can anchor its provider
// slot on the domain instead of an unattributed asserted name.
func TestEdgeThreadCreatesMessageOnlyThread(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")

	resp, out, raw := r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
		"host":       "api.globex.test",
		"message":    "Are you versioning /v1/refunds this quarter?",
		"request_id": "req-1",
	})
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		t.Fatalf("POST /api/edges/thread: %d %s", resp.StatusCode, raw)
	}
	if out["thread_url"] == "" || out["thread_id"] == "" {
		t.Fatalf("no thread link came back: %s", raw)
	}
	// The response carries no finding_id — there is no finding.
	if _, has := out["finding_id"]; has {
		t.Errorf("a message-only thread has no finding_id: %v", out)
	}

	body := r.cp.lastFlagBody
	if _, has := body["call"]; has {
		t.Errorf("a message-only flag must not carry a `call` key: %v", body)
	}
	if _, has := body["finding"]; has {
		t.Errorf("a message-only flag must not carry a `finding` key: %v", body)
	}
	if body["provider_host"] != "api.globex.test" {
		t.Errorf("provider_host = %v, want the edge host", body["provider_host"])
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "/v1/refunds") {
		t.Errorf("message = %q, want the operator's question", msg)
	}
	if body["consumer_display_name"] != "Acme Consumer Ltd" {
		t.Errorf("consumer_display_name = %v, want the CONNECTED org name", body["consumer_display_name"])
	}
}

// The idempotency key comes from the sheet's request id, namespaced so it can
// never collide with a flag's `flag_<finding_id>`. Same id twice = one thread;
// a different id on the same edge = a second, separate question.
func TestEdgeThreadIdempotency(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")
	send := func(requestID string) map[string]any {
		_, out, _ := r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
			"host": "api.globex.test", "message": "ping", "request_id": requestID,
		})
		return out
	}

	send("req-1")
	first := r.cp.lastFlagBody["idempotency_key"]
	if key, _ := first.(string); !strings.HasPrefix(key, "edge_") {
		t.Errorf("idempotency_key = %q, want an edge_ prefix that cannot collide with flag_", key)
	}
	send("req-1")
	if r.cp.lastFlagBody["idempotency_key"] != first {
		t.Errorf("a retry with the same request id must reuse the key: %v vs %v", r.cp.lastFlagBody["idempotency_key"], first)
	}
	send("req-2")
	if r.cp.lastFlagBody["idempotency_key"] == first {
		t.Errorf("a SECOND question about one edge must get its own key: %v", r.cp.lastFlagBody["idempotency_key"])
	}
}

// A blank message is refused HERE, before the round-trip: it is the entire
// artifact of a message-only thread, and the operator should see the refusal in
// the sheet rather than a relayed control-plane 400.
func TestEdgeThreadRefusesBlankMessage(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")

	resp, out, _ := r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
		"host": "api.globex.test", "message": "   ", "request_id": "req-1",
	})
	if resp.StatusCode != 400 || out["error"] != "missing_fields" {
		t.Errorf("blank message = %d %v, want 400 missing_fields", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("flag calls = %d, want 0 — nothing may reach the CP", r.cp.flagCalls)
	}
}

// Outbound rows only, exactly like the rename route: an INBOUND `peer_host` is a
// forgeable XFF first hop and is never identity, and an internal edge never
// crosses the org boundary at all.
func TestEdgeThreadRefusesUnknownAndInboundEdges(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")
	r.st.mu.Lock()
	r.st.edges = append(r.st.edges, model.Edge{PeerHost: "in.caller.test", Direction: "server", Role: "provider", Class: "external"})
	r.st.mu.Unlock()

	for _, host := range []string{"api.nowhere.test", "in.caller.test"} {
		resp, out, _ := r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
			"host": host, "message": "hello", "request_id": "req-1",
		})
		if resp.StatusCode != 404 || out["error"] != "edge_not_found" {
			t.Errorf("host %q = %d %v, want 404 edge_not_found", host, resp.StatusCode, out)
		}
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("flag calls = %d, want 0", r.cp.flagCalls)
	}
}

// The Connect gate is the SAME one the flag sheet meets — same 412s, from the
// same helper — so the sheet unlocks on one code path for both doors.
func TestEdgeThreadIsConnectGatedIdentically(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedOutboundEdge(r, "api.globex.test")

	resp, out, _ := r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
		"host": "api.globex.test", "message": "hello", "request_id": "req-1",
	})
	if resp.StatusCode != http.StatusPreconditionFailed || out["error"] != "not_connected" {
		t.Errorf("un-Connected = %d %v, want 412 not_connected", resp.StatusCode, out)
	}

	// Connected, contact still pending: the other 412.
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme",
		ContactEmail: "ops@acme.test", ContactStatus: "pending"})
	r.cp.mu.Lock()
	r.cp.contactEmail, r.cp.contactStatus, r.cp.confirmedEmail = "ops@acme.test", "pending", ""
	r.cp.mu.Unlock()
	resp, out, _ = r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
		"host": "api.globex.test", "message": "hello", "request_id": "req-1",
	})
	if resp.StatusCode != http.StatusPreconditionFailed || out["error"] != "contact_unconfirmed" {
		t.Errorf("unconfirmed contact = %d %v, want 412 contact_unconfirmed", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("flag calls = %d, want 0", r.cp.flagCalls)
	}
}

// The message is free text that leaves the collector, so it passes the redaction
// floor before it is sent — a question is exactly where someone pastes the
// response they are asking about.
func TestEdgeThreadRedactsTheMessage(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")

	r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
		"host":       "api.globex.test",
		"message":    "Is this card still on file? 4111111111111111",
		"request_id": "req-1",
	})
	if msg, _ := r.cp.lastFlagBody["message"].(string); strings.Contains(msg, "4111111111111111") {
		t.Errorf("the PAN reached the control plane: %q", msg)
	}
}

// The thread link is parked under thread.link.<thread_id> — the existing key for
// a thread this collector holds no finding record for. It is what makes Copy
// thread link work on the row the CP will list.
func TestEdgeThreadPersistsTheLink(t *testing.T) {
	r := connectedRig(t)
	seedOutboundEdge(r, "api.globex.test")

	_, out, _ := r.do(t, http.MethodPost, "/api/edges/thread", map[string]string{
		"host": "api.globex.test", "message": "hello", "request_id": "req-1",
	})
	threadID, _ := out["thread_id"].(string)
	parked, ok, err := r.st.GetSetting(settingThreadLinkPrefix + threadID)
	if err != nil || !ok || parked != out["thread_url"] {
		t.Errorf("parked link = %q ok=%v err=%v, want the thread url %v", parked, ok, err, out["thread_url"])
	}
}
