package viniferaui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/promote"
)

// stubCP is a minimal control plane for the relay tests: register / me / flags
// / thread routes with just enough state to walk the v0.1a loop.
type stubCP struct {
	mu               sync.Mutex
	srv              *httptest.Server
	deployToken      string
	collectorKey     string
	contactStatus    string
	contactEmail     string // the most recent (possibly pending) contact
	confirmedEmail   string // the contact usable for threads ("" until the first confirmation)
	registerCalls    int
	registerAuths    []string // Authorization header of every register call, in order
	flagCalls        int
	state            map[string]string // thread id -> open|closed
	replaceCalls     int
	lastAuth         string
	lastFlagBody     map[string]any
	lastRegisterBody map[string]any
	wrongOrigin      bool
	mintCount        int
}

func newStubCP(t *testing.T) *stubCP {
	t.Helper()
	s := &stubCP{deployToken: "deploy_tok_e2e", collectorKey: "ckey_SECRET_0123456789abcdef", contactStatus: "pending", state: map[string]string{}}
	mux := http.NewServeMux()
	jsonOut := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/v1/collectors/register", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		auth := r.Header.Get("Authorization")
		s.registerAuths = append(s.registerAuths, auth)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		email, _ := b["contact_email"].(string)
		s.lastRegisterBody = b
		s.registerCalls++
		switch auth {
		case "Bearer " + s.deployToken:
			// The CP has ONE deploy token: it cannot tell deployments apart by it.
			if s.registerCalls == 1 {
				s.contactEmail = email
				jsonOut(w, 201, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "collector_key": s.collectorKey, "contact_status": "pending"})
				return
			}
			if email == s.contactEmail {
				// same email → idempotent replay; the key is returned once, never again
				jsonOut(w, 200, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "contact_status": s.contactStatus})
				return
			}
			// a different email with only the deploy token = a NEW collector (CONTRACTS-CP §5.1) —
			// a Connected collector must never land here.
			jsonOut(w, 201, map[string]any{"collector_id": "c2", "collector_public_id": "pub_c2", "collector_key": "ckey_OTHER_COLLECTOR", "contact_status": "pending"})
		case "Bearer " + s.collectorKey:
			// re-register with the key: same email = resend; different = new pending contact, same key
			if email != s.contactEmail {
				s.contactEmail, s.contactStatus = email, "pending"
			}
			jsonOut(w, 200, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "contact_status": s.contactStatus})
		default:
			jsonOut(w, 401, map[string]string{"error": "unauthorized", "message": "bad bearer"})
		}
	})
	keyed := func(w http.ResponseWriter, r *http.Request) bool {
		s.lastAuth = r.Header.Get("Authorization")
		if s.lastAuth != "Bearer "+s.collectorKey {
			jsonOut(w, 401, map[string]string{"error": "unauthorized", "message": "bad key"})
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v1/collectors/me", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		if s.contactStatus == "confirmed" {
			s.confirmedEmail = s.contactEmail
		}
		confirmedAt := ""
		var confirmedEmail any
		if s.confirmedEmail != "" {
			confirmedAt = "2026-08-23T10:05:00Z"
			confirmedEmail = s.confirmedEmail
		}
		jsonOut(w, 200, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "consumer_display_name": "Acme Consumer Ltd",
			"contact_email": s.contactEmail, "contact_display_name": "Dana", "contact_status": s.contactStatus, "confirmed_contact_email": confirmedEmail,
			"registered_at": "2026-08-23T10:00:00Z", "confirmed_at": confirmedAt})
	})
	mux.HandleFunc("POST /api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		if s.contactStatus == "confirmed" {
			s.confirmedEmail = s.contactEmail
		}
		if s.confirmedEmail == "" {
			jsonOut(w, 412, map[string]string{"error": "contact_unconfirmed", "message": "confirm first"})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&s.lastFlagBody)
		s.flagCalls++
		s.state["thr_1"] = "open"
		status, st := 201, "created"
		if s.flagCalls > 1 {
			status, st = 200, "existing"
		}
		jsonOut(w, status, map[string]any{"thread_id": "thr_1", "thread_public_id": "pub_thr_1", "thread_url": fmt.Sprintf("https://cp.test/t/pub_thr_1#k=tok_%d", s.flagCalls),
			"peek_url": fmt.Sprintf("https://cp.test/t/pub_thr_1#k=tok_%d", s.flagCalls), "magic_token": "x", "state": "open", "status": st})
	})
	mux.HandleFunc("/api/v1/threads/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		id := r.PathValue("id")
		if s.wrongOrigin {
			jsonOut(w, 403, map[string]string{"error": "wrong_origin", "message": "not yours"})
			return
		}
		if _, ok := s.state[id]; !ok {
			jsonOut(w, 404, map[string]string{"error": "not_found", "message": "no thread"})
			return
		}
		switch r.PathValue("action") {
		case "summary":
			jsonOut(w, 200, map[string]any{"id": id, "thread_public_id": "pub_" + id, "state": s.state[id], "turn": "waiting_on_provider", "provider_display_name": "Acme Payments",
				"endpoint": "POST /v1/charges", "evidence_count": 1, "opened_count": 2, "knock_count": 0, "message_count": 0, "last_reply_at": nil, "fixed_claim": nil,
				"link": map[string]any{"status": "active", "expires_at": "2026-09-22T00:00:00Z"}, "archived": false})
		case "close":
			s.state[id] = "closed"
			jsonOut(w, 200, map[string]any{"state": "closed", "closed_at": "2026-08-23T12:00:00Z", "reopened_at": nil})
		case "reopen":
			s.state[id] = "open"
			jsonOut(w, 200, map[string]any{"state": "open", "closed_at": "2026-08-23T12:00:00Z", "reopened_at": "2026-08-23T12:30:00Z"})
		case "handoff":
			jsonOut(w, 201, map[string]any{"owner_url": "https://cp.test/o/pub_" + id + "#o=HANDOFF_SECRET", "expires_at": "2026-08-23T12:10:00Z"})
		case "peek-links":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["revoke_existing"] != true {
				jsonOut(w, 400, map[string]string{"error": "bad_request", "message": "expected revoke_existing"})
				return
			}
			s.replaceCalls++
			s.mintCount++
			jsonOut(w, 201, map[string]any{"thread_url": fmt.Sprintf("https://cp.test/t/pub_%s#k=replaced_%d", id, s.mintCount), "expires_at": "2026-09-22T00:00:00Z", "revoked": 1})
		default:
			jsonOut(w, 404, map[string]string{"error": "not_found"})
		}
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// testRig wires an extension around the fake store + stub CP, with a log
// observer so tests can prove nothing secret is ever logged.
type testRig struct {
	ext  *uiExtension
	st   *fakeStore
	cp   *stubCP
	logs *observer.ObservedLogs
	ui   *httptest.Server
}

func newRig(t *testing.T) *testRig {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	cp := newStubCP(t)
	st := newFakeStore()
	ext := &uiExtension{
		cfg:       &Config{UIEndpoint: "127.0.0.1:0", IntegrationID: "acme-payments", ConsumerDisplayName: "Cfg Consumer"},
		telemetry: component.TelemetrySettings{Logger: zap.New(core)},
		st:        st,
	}
	ext.stOnce.Do(func() {})
	return &testRig{ext: ext, st: st, cp: cp, logs: logs}
}

func (r *testRig) start(t *testing.T) {
	t.Helper()
	r.ext.cp = promote.NewClient(r.cp.srv.URL, r.cp.deployToken, "v-test")
	r.ui = httptest.NewServer(r.ext.routes())
	t.Cleanup(r.ui.Close)
	// a finding with its source call
	callID := "call_1"
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-08-23T10:00:00Z", Integration: "acme-payments", Direction: "client",
		Method: "POST", URL: "https://api.acme.test/v1/charges", Route: "/v1/charges", StatusCode: 200, RequestBody: "{}", ResponseBody: `{"amount":"10"}`,
		Correlation: model.Correlation{RequestID: "req_abc"}, Redaction: model.Redaction{Patterns: []string{}}})
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_1", Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking, Integration: "acme-payments",
		Endpoint: "POST /v1/charges", Expected: "integer", Actual: "string", Rule: "type", SourceCallID: &callID, DetectedAt: "2026-08-23T10:00:01Z"})
}

// do issues a request with the UI headers unless stripped.
func (r *testRig) do(t *testing.T, method, path string, body any, mutate ...func(*http.Request)) (*http.Response, map[string]any, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.ui.URL+path, rd)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Vinifera-UI", "1")
	}
	for _, m := range mutate {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out, raw
}

func (r *testRig) assertNeverLogged(t *testing.T, secrets ...string) {
	t.Helper()
	for _, entry := range r.logs.All() {
		line := entry.Message + fmt.Sprint(entry.Context)
		for _, s := range secrets {
			if strings.Contains(line, s) {
				t.Errorf("secret %q leaked into a log line: %q", s, line)
			}
		}
	}
}

func TestGuards(t *testing.T) {
	r := newRig(t)
	r.start(t)
	for _, path := range []string{"/api/flag", "/api/connect", "/api/threads/x/open", "/api/threads/x/close", "/api/threads/x/reopen", "/api/threads/x/replace-link"} {
		t.Run(path, func(t *testing.T) {
			// GET (or any non-POST) → 405 with Allow
			resp, _, _ := r.do(t, http.MethodPut, path, nil)
			if resp.StatusCode != 405 || !strings.Contains(resp.Header.Get("Allow"), "POST") {
				t.Errorf("PUT %s = %d Allow=%q, want 405 POST", path, resp.StatusCode, resp.Header.Get("Allow"))
			}
			// missing the UI header → 403
			resp, out, _ := r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Del("X-Vinifera-UI") })
			if resp.StatusCode != 403 || out["error"] != "ui_header_required" {
				t.Errorf("no UI header: %d %v", resp.StatusCode, out)
			}
			// wrong content type → 415
			resp, out, _ = r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Set("Content-Type", "text/plain") })
			if resp.StatusCode != 415 || out["error"] != "json_required" {
				t.Errorf("text/plain: %d %v", resp.StatusCode, out)
			}
			// foreign Origin → 403
			resp, out, _ = r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Set("Origin", "https://evil.example") })
			if resp.StatusCode != 403 || out["error"] != "forbidden_origin" {
				t.Errorf("foreign origin: %d %v", resp.StatusCode, out)
			}
			// no CORS headers, ever
			if resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("CORS header present on %s", path)
			}
			// same-host Origin passes the guard (the route then fails for its own reasons, never 403 forbidden_origin)
			resp, out, _ = r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Set("Origin", "http://"+q.Host) })
			if resp.StatusCode == 403 && out["error"] == "forbidden_origin" {
				t.Errorf("same-host origin rejected on %s", path)
			}
		})
	}
	// OPTIONS preflight is never answered with CORS.
	resp, _, _ := r.do(t, http.MethodOptions, "/api/flag", nil)
	if resp.StatusCode != 405 || resp.Header.Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("OPTIONS = %d with CORS=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Methods"))
	}
}

func TestConnectThenFlagLoop(t *testing.T) {
	r := newRig(t)
	r.start(t)

	// 1. disconnected
	resp, out, _ := r.do(t, http.MethodGet, "/api/connect", nil)
	if resp.StatusCode != 200 || out["status"] != "disconnected" {
		t.Fatalf("GET connect: %d %v", resp.StatusCode, out)
	}
	if out["collector_public_id"] != nil || out["confirmed_at"] != nil {
		t.Errorf("fresh collector must report null public id / dates, got %v", out)
	}
	// health carries the same, from the store only
	_, h, _ := r.do(t, http.MethodGet, "/api/health", nil)
	if h["connect_status"] != "disconnected" {
		t.Errorf("health connect_status=%v", h["connect_status"])
	}

	// 2. flag before Connect → 412 not_connected
	resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_1"})
	if resp.StatusCode != 412 || out["error"] != "not_connected" {
		t.Fatalf("flag before connect: %d %v", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("nothing may leave the collector before Connect; flag calls = %d", r.cp.flagCalls)
	}

	// 3. Connect → 202 pending; key persisted in the store; never in the body
	resp, out, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "ops@acme.test", "contact_display_name": "Dana", "local_ui_url": "http://localhost:5335"})
	if resp.StatusCode != 202 || out["status"] != "pending" || out["contact_email"] != "ops@acme.test" || out["collector_public_id"] != "pub_c1" {
		t.Fatalf("connect: %d %s", resp.StatusCode, raw)
	}
	if bytes.Contains(raw, []byte(r.cp.collectorKey)) {
		t.Fatalf("collector key leaked into the connect response")
	}
	if r.st.settings[settingCollectorKey] != r.cp.collectorKey {
		t.Fatalf("collector key not persisted in the store settings")
	}
	if r.st.settings[settingContactStatus] != "pending" || r.st.settings[settingLocalUIURL] != "http://localhost:5335" {
		t.Errorf("settings = %v", r.st.settings)
	}
	resp, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "pending" || out["contact_display_name"] != "Dana" {
		t.Errorf("GET connect pending: %v", out)
	}

	// 4. flag while pending → 412 contact_unconfirmed, naming the address
	resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_1"})
	if resp.StatusCode != 412 || out["error"] != "contact_unconfirmed" || !strings.Contains(out["message"].(string), "ops@acme.test") {
		t.Fatalf("flag while pending: %d %v", resp.StatusCode, out)
	}

	// 5. resend: same email → replay, same key kept, still pending
	resp, out, _ = r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "ops@acme.test"})
	if resp.StatusCode != 202 || out["status"] != "pending" || r.cp.registerCalls != 2 {
		t.Fatalf("resend: %d %v calls=%d", resp.StatusCode, out, r.cp.registerCalls)
	}
	if r.st.settings[settingCollectorKey] != r.cp.collectorKey {
		t.Fatalf("resend must keep the key")
	}
	// CONTRACTS-CP §5.1: once a key exists, register goes out with the KEY, never the deploy token
	if got := r.cp.registerAuths; len(got) != 2 || got[0] != "Bearer "+r.cp.deployToken || got[1] != "Bearer "+r.cp.collectorKey {
		t.Fatalf("register bearers = %v; want [deploy token, collector key]", got)
	}

	// 6. the contact confirms on the CP → GET connect refreshes via me → connected
	r.cp.mu.Lock()
	r.cp.contactStatus = "confirmed"
	r.cp.mu.Unlock()
	r.ext.me.mu.Lock()
	r.ext.me.at = r.ext.me.at.Add(-meCacheTTL * 2) // expire the cache
	r.ext.me.mu.Unlock()
	resp, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "connected" || out["confirmed_at"] == nil || out["confirmed_at"] == "" {
		t.Fatalf("GET connect after confirm: %v", out)
	}
	if r.st.settings[settingContactStatus] != "confirmed" {
		t.Errorf("confirmed status not persisted")
	}
	// re-POST once connected → 200 connected
	resp, out, _ = r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "ops@acme.test"})
	if resp.StatusCode != 200 || out["status"] != "connected" {
		t.Errorf("re-POST when connected: %d %v", resp.StatusCode, out)
	}

	// 7. flag → 201 {thread_id, thread_public_id, thread_url, state, status}; uses the KEY; record persisted
	resp, out, raw = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_1"})
	if resp.StatusCode != 201 {
		t.Fatalf("flag: %d %s", resp.StatusCode, raw)
	}
	if out["thread_id"] != "thr_1" || out["thread_public_id"] != "pub_thr_1" || out["thread_url"] != "https://cp.test/t/pub_thr_1#k=tok_1" || out["state"] != "open" || out["status"] != "created" {
		t.Errorf("flag body: %s", raw)
	}
	if r.cp.lastAuth != "Bearer "+r.cp.collectorKey {
		t.Errorf("flag must use the collector key, got %q", r.cp.lastAuth)
	}
	if _, has := r.cp.lastFlagBody["invitee_email"]; has {
		t.Errorf("invitee_email must not be sent: %v", r.cp.lastFlagBody)
	}
	if r.cp.lastFlagBody["consumer_display_name"] != "Acme Consumer Ltd" || r.cp.lastFlagBody["provider_display_name"] != "Acme Payments" {
		t.Errorf("flag body names: %v", r.cp.lastFlagBody)
	}
	if len(r.st.promoted) != 1 || r.st.promoted[0] != "call_1" {
		t.Errorf("evict-after-promote not applied: %v", r.st.promoted)
	}
	rec, ok, _ := loadThread(r.st, "fnd_1")
	if !ok || rec.ThreadID != "thr_1" || rec.ThreadURL != "https://cp.test/t/pub_thr_1#k=tok_1" || rec.Endpoint != "POST /v1/charges" || rec.Provider != "Acme Payments" {
		t.Errorf("thread record: %+v ok=%v", rec, ok)
	}
	// re-flag → 200 existing, fresh link persisted
	resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_1"})
	if resp.StatusCode != 200 || out["status"] != "existing" {
		t.Errorf("re-flag: %d %v", resp.StatusCode, out)
	}

	// 8. GET /api/threads lists it with the CP summary
	resp, _, raw = r.do(t, http.MethodGet, "/api/threads", nil)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
		t.Fatalf("threads: %d %s", resp.StatusCode, raw)
	}
	row := list[0]
	if row["thread_id"] != "thr_1" || row["finding_id"] != "fnd_1" || row["thread_url"] != "https://cp.test/t/pub_thr_1#k=tok_2" {
		t.Errorf("row: %v", row)
	}
	sum, _ := row["summary"].(map[string]any)
	if sum == nil || sum["state"] != "open" || sum["turn"] != "waiting_on_provider" || sum["opened_count"] != float64(2) || sum["link"].(map[string]any)["status"] != "active" {
		t.Errorf("summary: %v", row["summary"])
	}

	// 9. summary route
	resp, out, _ = r.do(t, http.MethodGet, "/api/threads/thr_1/summary", nil)
	if resp.StatusCode != 200 || out["turn"] != "waiting_on_provider" {
		t.Errorf("summary: %d %v", resp.StatusCode, out)
	}
	// unknown thread → 404
	resp, out, _ = r.do(t, http.MethodGet, "/api/threads/nope/summary", nil)
	if resp.StatusCode != 404 || out["error"] != "thread_not_found" {
		t.Errorf("unknown summary: %d %v", resp.StatusCode, out)
	}

	// 10. open → owner_url (handoff) — returned, never stored, never logged
	resp, out, raw = r.do(t, http.MethodPost, "/api/threads/thr_1/open", map[string]string{})
	if resp.StatusCode != 200 || !strings.HasPrefix(out["owner_url"].(string), "https://cp.test/o/pub_thr_1#o=") {
		t.Fatalf("open: %d %s", resp.StatusCode, raw)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("open must be no-store")
	}
	for k, v := range r.st.settings {
		if strings.Contains(v, "HANDOFF_SECRET") {
			t.Errorf("handoff persisted under %s", k)
		}
	}

	// 11. close → reopen → close walks the state
	resp, out, _ = r.do(t, http.MethodPost, "/api/threads/thr_1/close", map[string]string{})
	if resp.StatusCode != 200 || out["state"] != "closed" || out["closed_at"] == nil {
		t.Errorf("close: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodPost, "/api/threads/thr_1/reopen", map[string]string{})
	if resp.StatusCode != 200 || out["state"] != "open" || out["reopened_at"] == nil {
		t.Errorf("reopen: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodGet, "/api/threads/thr_1/summary", nil)
	if out["state"] != "open" {
		t.Errorf("summary after reopen: %v", out)
	}

	// 12. replace-link → new thread_url, persisted
	resp, out, _ = r.do(t, http.MethodPost, "/api/threads/thr_1/replace-link", map[string]string{})
	if resp.StatusCode != 200 || out["thread_url"] != "https://cp.test/t/pub_thr_1#k=replaced_1" || out["revoked"] != float64(1) {
		t.Fatalf("replace-link: %d %v", resp.StatusCode, out)
	}
	rec, _, _ = loadThread(r.st, "fnd_1")
	if rec.ThreadURL != "https://cp.test/t/pub_thr_1#k=replaced_1" {
		t.Errorf("replaced link not persisted: %+v", rec)
	}

	// 13. a foreign-origin thread → 403 wrong_origin passes through
	r.cp.mu.Lock()
	r.cp.wrongOrigin = true
	r.cp.mu.Unlock()
	resp, out, _ = r.do(t, http.MethodPost, "/api/threads/thr_1/close", map[string]string{})
	if resp.StatusCode != 403 || out["error"] != "wrong_origin" {
		t.Errorf("wrong_origin: %d %v", resp.StatusCode, out)
	}

	// Nothing secret ever reached a log line.
	r.assertNeverLogged(t, r.cp.collectorKey, "HANDOFF_SECRET", "tok_1", "tok_2", "replaced_1", r.cp.deployToken)
}

func TestConnectValidation(t *testing.T) {
	r := newRig(t)
	r.start(t)
	resp, out, _ := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "", "contact_email": "x@y.z"})
	if resp.StatusCode != 400 || out["error"] != "missing_fields" {
		t.Errorf("missing org: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme", "contact_email": "not-an-email"})
	if resp.StatusCode != 400 || out["error"] != "invalid_email" {
		t.Errorf("bad email: %d %v", resp.StatusCode, out)
	}
	resp, out, _ = r.do(t, http.MethodPost, "/api/connect", nil, func(q *http.Request) { q.Body = io.NopCloser(strings.NewReader("{")) })
	if resp.StatusCode != 400 || out["error"] != "invalid_json" {
		t.Errorf("bad json: %d %v", resp.StatusCode, out)
	}
	if r.cp.registerCalls != 0 {
		t.Errorf("invalid input must not reach the CP")
	}
	// Flag validation
	resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]string{})
	if resp.StatusCode != 400 || out["error"] != "missing_fields" {
		t.Errorf("flag without finding: %d %v", resp.StatusCode, out)
	}
}

func TestCPUnreachable(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.cp.srv.Close() // CP down
	resp, out, _ := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme", "contact_email": "ops@acme.test"})
	if resp.StatusCode != 502 || out["error"] != "cp_unreachable" || out["message"] != msgCPUnreachableSend {
		t.Errorf("connect unreachable: %d %v", resp.StatusCode, out)
	}
	if _, has := r.st.settings[settingCollectorKey]; has {
		t.Errorf("nothing may be persisted when the CP was unreachable")
	}
	// GET /api/connect still answers from the store (disconnected) without failing
	resp, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if resp.StatusCode != 200 || out["status"] != "disconnected" {
		t.Errorf("GET connect with CP down: %d %v", resp.StatusCode, out)
	}
}

func TestThreadsListToleratesCPErrors(t *testing.T) {
	r := newRig(t)
	r.start(t)
	// Seed a connected state + a thread record directly.
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	_ = saveThread(r.st, threadRecord{ThreadID: "thr_missing", ThreadPublicID: "p", FindingID: "fnd_1", Endpoint: "POST /v1/charges", Provider: "Acme Payments", ThreadURL: "https://cp.test/t/p#k=t", CreatedAt: "2026-08-23T10:00:00Z"})
	resp, _, raw := r.do(t, http.MethodGet, "/api/threads", nil)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil || resp.StatusCode != 200 || len(list) != 1 {
		t.Fatalf("threads: %d %s", resp.StatusCode, raw)
	}
	if list[0]["summary"] != nil || list[0]["error"] != "not_found" {
		t.Errorf("expected summary:null + error, got %v", list[0])
	}
	// CP down → cp_unreachable, list still renders
	r.cp.srv.Close()
	_, _, raw = r.do(t, http.MethodGet, "/api/threads", nil)
	_ = json.Unmarshal(raw, &list)
	if len(list) != 1 || list[0]["error"] != "cp_unreachable" {
		t.Errorf("cp down: %s", raw)
	}
}

// TestConnectReplayWithoutKey: the CP already knows the deploy token (replay,
// no key in the answer) but this store never held the key — the relay must say
// so (409 key_missing) rather than persist a keyless "pending" state.
func TestConnectReplayWithoutKey(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.cp.mu.Lock()
	r.cp.registerCalls = 1 // next register = replay without the key
	r.cp.contactEmail = "ops@acme.test"
	r.cp.mu.Unlock()
	resp, out, _ := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme", "contact_email": "ops@acme.test"})
	if resp.StatusCode != 409 || out["error"] != "key_missing" {
		t.Fatalf("replay without key: %d %v", resp.StatusCode, out)
	}
	if _, has := r.st.settings[settingCollectorKey]; has {
		t.Errorf("no key must be persisted")
	}
	resp, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "disconnected" {
		t.Errorf("still disconnected, got %v", out)
	}
}

// TestChangeContactKeepsKeyAndThreads (CONTRACTS-CP §5.1 + §5.3): a Connected
// collector that changes its contact re-registers with the COLLECTOR KEY (never
// the deploy token — that would register a new collector), keeps its key, and
// while the NEW email is pending the previously confirmed contact keeps Create
// thread available (no 412).
func TestChangeContactKeepsKeyAndThreads(t *testing.T) {
	r := newRig(t)
	r.start(t)
	// Connect + confirm ops@acme.test
	resp, out, _ := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "ops@acme.test"})
	if resp.StatusCode != 202 {
		t.Fatalf("connect: %d %v", resp.StatusCode, out)
	}
	r.cp.mu.Lock()
	r.cp.contactStatus = "confirmed"
	r.cp.mu.Unlock()
	r.ext.me.mu.Lock()
	r.ext.me.at = r.ext.me.at.Add(-meCacheTTL * 2)
	r.ext.me.mu.Unlock()
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "connected" || out["confirmed_contact_email"] != "ops@acme.test" {
		t.Fatalf("after confirm: %v", out)
	}

	// Change contact → register with the key; key unchanged; new contact pending; old one still confirmed
	resp, out, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "new@acme.test", "contact_display_name": "Sam"})
	if resp.StatusCode != 202 || out["status"] != "pending" || out["contact_email"] != "new@acme.test" || out["confirmed_contact_email"] != "ops@acme.test" {
		t.Fatalf("change contact: %d %s", resp.StatusCode, raw)
	}
	if got := r.cp.registerAuths; len(got) != 2 || got[1] != "Bearer "+r.cp.collectorKey {
		t.Fatalf("change of contact must re-register with the collector key, bearers = %v", got)
	}
	if r.cp.lastRegisterBody["contact_email"] != "new@acme.test" || r.cp.lastRegisterBody["contact_display_name"] != "Sam" {
		t.Errorf("register body: %v", r.cp.lastRegisterBody)
	}
	if r.st.settings[settingCollectorKey] != r.cp.collectorKey {
		t.Fatalf("the collector key must not change on a contact change")
	}
	if r.st.settings[settingContactStatus] != "pending" || r.st.settings[settingContactEmail] != "new@acme.test" || r.st.settings[settingConfirmedContactEmail] != "ops@acme.test" {
		t.Errorf("settings = %v", r.st.settings)
	}
	// GET /api/connect (me says: new pending, confirmed_contact_email = old) → still pending + confirmed email kept
	r.ext.me.mu.Lock()
	r.ext.me.at = r.ext.me.at.Add(-meCacheTTL * 2)
	r.ext.me.mu.Unlock()
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "pending" || out["contact_email"] != "new@acme.test" || out["confirmed_contact_email"] != "ops@acme.test" {
		t.Fatalf("GET connect while new contact pending: %v", out)
	}

	// Create thread works — the confirmed contact still exists (no 412)
	resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_1"})
	if resp.StatusCode != 201 || out["thread_id"] != "thr_1" {
		t.Fatalf("flag while a new contact is pending: %d %v", resp.StatusCode, out)
	}
	if r.cp.lastAuth != "Bearer "+r.cp.collectorKey {
		t.Errorf("flag must use the (unchanged) collector key")
	}

	// Resend for the pending new contact → key again, same email, still pending
	resp, out, _ = r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "new@acme.test"})
	if resp.StatusCode != 202 || out["status"] != "pending" || r.cp.registerAuths[2] != "Bearer "+r.cp.collectorKey {
		t.Errorf("resend: %d %v auths=%v", resp.StatusCode, out, r.cp.registerAuths)
	}

	// The new contact confirms → connected as new@acme.test
	r.cp.mu.Lock()
	r.cp.contactStatus = "confirmed"
	r.cp.mu.Unlock()
	r.ext.me.mu.Lock()
	r.ext.me.at = r.ext.me.at.Add(-meCacheTTL * 2)
	r.ext.me.mu.Unlock()
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "connected" || out["contact_email"] != "new@acme.test" || out["confirmed_contact_email"] != "new@acme.test" {
		t.Fatalf("after new contact confirmed: %v", out)
	}
	// The deploy token was used exactly once, at the very first Connect.
	deployUses := 0
	for _, a := range r.cp.registerAuths {
		if a == "Bearer "+r.cp.deployToken {
			deployUses++
		}
	}
	if deployUses != 1 {
		t.Errorf("deploy token used %d times in register; want exactly once", deployUses)
	}
	r.assertNeverLogged(t, r.cp.collectorKey, r.cp.deployToken)
}

// TestConnectedAddressUpdate (v0.1b post-Connect nudge): a Connected collector
// that registered without local_ui_url adds it later by re-POSTing /api/connect
// with the SAME contact — the re-register goes out with the collector key
// (idempotent replay: no new pending contact, status stays connected), carries
// local_ui_url, and the address is persisted and returned to the UI.
func TestConnectedAddressUpdate(t *testing.T) {
	r := newRig(t)
	r.start(t)
	// Connect without an address + confirm
	resp, out, _ := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "ops@acme.test"})
	if resp.StatusCode != 202 {
		t.Fatalf("connect: %d %v", resp.StatusCode, out)
	}
	r.cp.mu.Lock()
	r.cp.contactStatus = "confirmed"
	r.cp.mu.Unlock()
	r.ext.me.mu.Lock()
	r.ext.me.at = r.ext.me.at.Add(-meCacheTTL * 2)
	r.ext.me.mu.Unlock()
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "connected" || out["local_ui_url"] != nil {
		t.Fatalf("connected without an address, got %v", out)
	}

	// Add the address: same contact + local_ui_url → 200 connected, key bearer
	resp, out, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{"consumer_display_name": "Acme Consumer Ltd", "contact_email": "ops@acme.test", "local_ui_url": "http://collector.internal:5335"})
	if resp.StatusCode != 200 || out["status"] != "connected" || out["local_ui_url"] != "http://collector.internal:5335" {
		t.Fatalf("address update: %d %s", resp.StatusCode, raw)
	}
	if got := r.cp.registerAuths; len(got) != 2 || got[1] != "Bearer "+r.cp.collectorKey {
		t.Fatalf("address update must re-register with the collector key, bearers = %v", got)
	}
	if r.cp.lastRegisterBody["local_ui_url"] != "http://collector.internal:5335" {
		t.Errorf("register body must carry the address: %v", r.cp.lastRegisterBody)
	}
	if r.st.settings[settingLocalUIURL] != "http://collector.internal:5335" {
		t.Errorf("address not persisted: %v", r.st.settings)
	}
	if r.st.settings[settingCollectorKey] != r.cp.collectorKey || r.st.settings[settingContactStatus] != "confirmed" {
		t.Errorf("key/status must be unchanged by an address update: %v", r.st.settings)
	}

	// GET keeps reporting it (store-backed; me has no say over a locally set address)
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["status"] != "connected" || out["local_ui_url"] != "http://collector.internal:5335" {
		t.Fatalf("GET after address update: %v", out)
	}
	r.assertNeverLogged(t, r.cp.collectorKey, r.cp.deployToken)
}

// TestFlagGateBeforeFirstConfirmation: with a key but NO confirmed contact ever,
// Create thread still 412s (the change-of-contact relaxation must not open the
// gate before the first confirmation).
func TestFlagGateBeforeFirstConfirmation(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "pending"})
	resp, out, _ := r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_1"})
	if resp.StatusCode != 412 || out["error"] != "contact_unconfirmed" {
		t.Fatalf("flag with no confirmed contact: %d %v", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("flag must not reach the CP")
	}
}

// TestConnectRedactsDisplayNames: display names are free text that leaves the
// collector — they pass the redaction floor (like the flag message) before they
// are sent to the CP or persisted. A PAN in a display name is tokenised.
func TestConnectRedactsDisplayNames(t *testing.T) {
	r := newRig(t)
	r.start(t)
	const pan = "4242424242424242"
	resp, out, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{
		"consumer_display_name": "Acme " + pan + " Ltd", "contact_email": "ops@acme.test", "contact_display_name": "Dana " + pan})
	if resp.StatusCode != 202 {
		t.Fatalf("connect: %d %s", resp.StatusCode, raw)
	}
	sent, _ := r.cp.lastRegisterBody["consumer_display_name"].(string)
	sentContact, _ := r.cp.lastRegisterBody["contact_display_name"].(string)
	if strings.Contains(sent, pan) || strings.Contains(sentContact, pan) {
		t.Fatalf("PAN left the collector in a display name: %q / %q", sent, sentContact)
	}
	if !strings.Contains(sent, "⟦REDACTED:") || !strings.Contains(sentContact, "⟦REDACTED:") {
		t.Errorf("display names not tokenised: %q / %q", sent, sentContact)
	}
	if !strings.HasPrefix(sent, "Acme ") || !strings.HasSuffix(sent, " Ltd") {
		t.Errorf("redaction must be surgical, got %q", sent)
	}
	for k, v := range r.st.settings {
		if strings.Contains(v, pan) {
			t.Errorf("PAN persisted under %s", k)
		}
	}
	if bytes.Contains(raw, []byte(pan)) {
		t.Errorf("PAN echoed in the connect response")
	}
	if out["consumer_display_name"] != sent {
		t.Errorf("response name %v != sent %q", out["consumer_display_name"], sent)
	}
}

// TestSaveThreadIndex: two sequential saves both land in threads.index, a
// re-save of the same finding is idempotent, and a concurrent writer clobbering
// the index between our read and write is survived by the re-read retry.
func TestSaveThreadIndex(t *testing.T) {
	st := newFakeStore()
	a := threadRecord{ThreadID: "thr_a", FindingID: "fnd_a", CreatedAt: "2026-08-23T10:00:00Z"}
	b := threadRecord{ThreadID: "thr_b", FindingID: "fnd_b", CreatedAt: "2026-08-23T10:01:00Z"}
	if err := saveThread(st, a); err != nil {
		t.Fatal(err)
	}
	if err := saveThread(st, b); err != nil {
		t.Fatal(err)
	}
	if err := saveThread(st, a); err != nil { // idempotent
		t.Fatal(err)
	}
	recs, err := listThreads(st)
	if err != nil || len(recs) != 2 || recs[0].ThreadID != "thr_b" || recs[1].ThreadID != "thr_a" {
		t.Fatalf("listThreads = %+v err=%v", recs, err)
	}
	ids, _ := loadThreadIndex(st)
	if len(ids) != 2 {
		t.Fatalf("index = %v", ids)
	}

	// A concurrent writer: right after our first index write lands, another
	// pod overwrites the index with its own stale read-modify-write (the
	// lost-update window). The verify re-read notices, the retry re-reads the
	// index immediately before writing again, and the final index holds both.
	st2 := newFakeStore()
	clobbered := false
	st2.afterPut = func(key, value string) {
		if key == settingThreadsIndex && !clobbered {
			clobbered = true
			st2.mu.Lock()
			st2.settings[settingThreadsIndex] = `["fnd_other"]`
			st2.mu.Unlock()
		}
	}
	if err := saveThread(st2, a); err != nil {
		t.Fatalf("saveThread under a one-shot clobber: %v", err)
	}
	ids, _ = loadThreadIndex(st2)
	if len(ids) != 2 || !containsID(ids, "fnd_a") || !containsID(ids, "fnd_other") {
		t.Fatalf("index after retry = %v; want both ids", ids)
	}
	// Clobbered on every attempt → the record is still saved; the index error surfaces.
	st3 := newFakeStore()
	st3.afterPut = func(key, value string) {
		if key == settingThreadsIndex {
			st3.mu.Lock()
			st3.settings[settingThreadsIndex] = `["fnd_other"]`
			st3.mu.Unlock()
		}
	}
	if err := saveThread(st3, a); !errors.Is(err, errIndexRace) {
		t.Errorf("always-clobbered index: err=%v, want errIndexRace", err)
	}
	if _, ok, _ := loadThread(st3, "fnd_a"); !ok {
		t.Fatalf("record must be persisted even when the index write loses")
	}
}
