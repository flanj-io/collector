package flanjui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/promote"
)

// stubCP is a minimal control plane for the relay tests: register / me / flags
// / thread routes with just enough state to walk the v0.1a loop.
type stubCP struct {
	mu             sync.Mutex
	srv            *httptest.Server
	deployToken    string
	collectorKey   string
	contactStatus  string
	contactEmail   string // the most recent (possibly pending) contact
	confirmedEmail string // the contact usable for threads ("" until the first confirmation)
	// totalCalls counts EVERY request that reached this stub, on any route.
	totalCalls       int
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
	// CONTRACTS-CP §5.1 (additive): the confirmation-mail outcome the CP
	// reports on register. "" = the CP reports none (already confirmed, or a
	// CP predating the field), which must reach the UI as an ABSENT key.
	confirmationMail           string
	confirmationMailRetryAfter int
	// §5.5a list route: insertion order (the stub lists it reversed, so the
	// most recently touched thread is first), the last query string seen, the
	// number of list calls, and an optional forced status for the error paths.
	order      []string
	listQuery  string
	listCalls  int
	listStatus int
	// listTotal / listHasMore override the envelope's truncation fields, so a
	// test can be a collector with more threads than §5.5a's hard cap.
	listTotal   int
	listHasMore bool
	// Finding-shape sync (POST /api/v1/findings): every raw request body, in
	// order, plus the call count — sync_test.go asserts on the BYTES.
	findingsCalls  int
	findingsBodies [][]byte
	// Edge registration (POST /api/v1/edges/sync — v1 phase 2): the call count
	// plus every raw body in order, so a test can assert on the WIRE BYTES that
	// an internal edge never left.
	edgesCalls  int
	edgesBodies [][]byte
	// Directory pull (GET /api/v1/directory): the served ENTRIES object + ETag,
	// the If-None-Match header of every call, and the call count. Fixtures set
	// the bare `{"<domain>": {"name","tier"}}` map; the stub ALWAYS wraps it in
	// the §5.14 envelope `{"entries": …, "count": n}` itself, so a fixture can
	// never drift back to serving a bare map.
	directoryEntries string
	directoryETag    string
	directoryCalls   int
	directoryINMs    []string
	// Directory submissions (POST /api/v1/directory/submissions): every raw
	// body in order, plus an optional forced status (and error message) for
	// the failure paths.
	submissionBodies  [][]byte
	submissionStatus  int
	submissionMessage string
}

// directoryEnvelopeBody wraps a bare entries object in the §5.14 response
// envelope `{"entries": …, "count": n}` — the ONLY shape the stub (and the
// real CP) ever serves. Tests reuse it to compute expected raw bodies.
func directoryEnvelopeBody(entries string) string {
	if entries == "" {
		entries = "{}"
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(entries), &m)
	return fmt.Sprintf(`{"entries":%s,"count":%d}`, entries, len(m))
}

// summaryRow is the §5.5 summary object — the SAME row §5.5a lists.
func (s *stubCP) summaryRow(id string) map[string]any {
	return map[string]any{"id": id, "thread_public_id": "pub_" + id, "state": s.state[id], "closed_at": nil, "reopened_at": nil,
		"turn": "waiting_on_provider", "consumer_display_name": "Acme Consumer Ltd", "provider_display_name": "Acme Payments",
		"endpoint": "POST /v1/charges", "evidence_count": 1, "opened_count": 2, "knock_count": 0, "message_count": 0,
		"last_reply_at": nil, "fixed_claim": nil, "link": map[string]any{"status": "active", "expires_at": "2026-09-22T00:00:00Z"},
		"archived": false, "created_at": "2026-08-23T10:00:00Z", "updated_at": "2026-08-23T11:00:00Z"}
}

// track records a thread id in list order (once).
func (s *stubCP) track(id string) {
	for _, x := range s.order {
		if x == id {
			return
		}
	}
	s.order = append(s.order, id)
}

// requestCount is how many requests have reached the control plane, on any
// route. Its one job is proving a code path made none.
func (s *stubCP) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalCalls
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
		// mailOut is jsonOut plus whatever confirmation-mail outcome this stub
		// is set to report, so every success path below carries it without
		// repeating the merge. The 401 branch stays on bare jsonOut: an
		// unauthorized register never attempted a mail.
		mailOut := func(w http.ResponseWriter, status int, body map[string]any) {
			if s.confirmationMail != "" {
				body["confirmation_mail"] = s.confirmationMail
				if s.confirmationMailRetryAfter != 0 {
					body["confirmation_mail_retry_after_s"] = s.confirmationMailRetryAfter
				}
			}
			jsonOut(w, status, body)
		}
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
				mailOut(w, 201, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "collector_key": s.collectorKey, "contact_status": "pending"})
				return
			}
			if email == s.contactEmail {
				// same email → idempotent replay; the key is returned once, never again
				mailOut(w, 200, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "contact_status": s.contactStatus})
				return
			}
			// a different email with only the deploy token = a NEW collector (CONTRACTS-CP §5.1) —
			// a Connected collector must never land here.
			mailOut(w, 201, map[string]any{"collector_id": "c2", "collector_public_id": "pub_c2", "collector_key": "ckey_OTHER_COLLECTOR", "contact_status": "pending"})
		case "Bearer " + s.collectorKey:
			// re-register with the key: same email = resend; different = new pending contact, same key
			if email != s.contactEmail {
				s.contactEmail, s.contactStatus = email, "pending"
			}
			mailOut(w, 200, map[string]any{"collector_id": "c1", "collector_public_id": "pub_c1", "contact_status": s.contactStatus})
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
		// Reset first: decoding into a non-nil map MERGES, which would let a
		// previous flag's `call` linger and mask a call-less body.
		s.lastFlagBody = nil
		_ = json.NewDecoder(r.Body).Decode(&s.lastFlagBody)
		s.flagCalls++
		s.state["thr_1"] = "open"
		s.track("thr_1")
		status, st := 201, "created"
		if s.flagCalls > 1 {
			status, st = 200, "existing"
		}
		jsonOut(w, status, map[string]any{"thread_id": "thr_1", "thread_public_id": "pub_thr_1", "thread_url": fmt.Sprintf("https://cp.test/t/pub_thr_1#k=tok_%d", s.flagCalls),
			"peek_url": fmt.Sprintf("https://cp.test/t/pub_thr_1#k=tok_%d", s.flagCalls), "magic_token": "x", "state": "open", "status": st})
	})
	// CONTRACTS §5: the shape-only finding sync — collector key required; the
	// stub records the raw body so tests can assert on the wire bytes.
	mux.HandleFunc("POST /api/v1/findings", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		raw, _ := io.ReadAll(r.Body)
		s.findingsCalls++
		s.findingsBodies = append(s.findingsBodies, append([]byte(nil), raw...))
		var b struct {
			Findings []json.RawMessage `json:"findings"`
		}
		_ = json.Unmarshal(raw, &b)
		jsonOut(w, 200, map[string]any{"received": len(b.Findings), "stored": len(b.Findings)})
	})
	// CONTRACTS §5: edge registration — collector key required; the stub
	// records the raw body so tests can assert on the wire bytes that no
	// internal edge, and no peer host, ever left.
	mux.HandleFunc("POST /api/v1/edges/sync", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		raw, _ := io.ReadAll(r.Body)
		s.edgesCalls++
		s.edgesBodies = append(s.edgesBodies, append([]byte(nil), raw...))
		var b struct {
			Edges []json.RawMessage `json:"edges"`
		}
		_ = json.Unmarshal(raw, &b)
		jsonOut(w, 200, map[string]any{"received": len(b.Edges), "stored": len(b.Edges)})
	})
	// v1p1: the directory full-table pull — collector key required, ETag
	// conditional (If-None-Match match → 304, no body).
	mux.HandleFunc("GET /api/v1/directory", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		s.directoryCalls++
		s.directoryINMs = append(s.directoryINMs, r.Header.Get("If-None-Match"))
		if s.directoryETag != "" && r.Header.Get("If-None-Match") == s.directoryETag {
			w.WriteHeader(304)
			return
		}
		if s.directoryETag != "" {
			w.Header().Set("ETag", s.directoryETag)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(directoryEnvelopeBody(s.directoryEntries)))
	})
	// v1p1: opt-in directory submissions — collector key required; the stub
	// records the raw body so tests assert nothing leaves without the opt-in.
	mux.HandleFunc("POST /api/v1/directory/submissions", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		raw, _ := io.ReadAll(r.Body)
		s.submissionBodies = append(s.submissionBodies, append([]byte(nil), raw...))
		if s.submissionStatus != 0 {
			msg := s.submissionMessage
			if msg == "" {
				msg = "try later"
			}
			jsonOut(w, s.submissionStatus, map[string]string{"error": "unavailable", "message": msg})
			return
		}
		jsonOut(w, 202, map[string]any{"status": "pending"})
	})
	// CONTRACTS-CP §5.5a: the collector-key-scoped thread list, an ENVELOPE.
	mux.HandleFunc("GET /api/v1/threads", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !keyed(w, r) {
			return
		}
		s.listCalls++
		s.listQuery = r.URL.RawQuery
		if s.listStatus != 0 {
			jsonOut(w, s.listStatus, map[string]string{"error": "rate_limited", "message": "slow down"})
			return
		}
		rows := make([]map[string]any, 0, len(s.order))
		for i := len(s.order) - 1; i >= 0; i-- { // most-recently-active first
			rows = append(rows, s.summaryRow(s.order[i]))
		}
		total := len(rows)
		if s.listTotal != 0 {
			total = s.listTotal
		}
		jsonOut(w, 200, map[string]any{"threads": rows, "count": len(rows), "total": total, "limit": 200, "has_more": s.listHasMore})
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
			jsonOut(w, 200, s.summaryRow(id))
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
	// Every request, whatever the route, so a test can assert a code path makes
	// NO control-plane call at all — which is the whole ruling for uploaded
	// contracts, and not something a per-route counter can prove.
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.totalCalls++
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
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
		st:        st, // pre-resolved: resolveStore returns it without touching the (nil) host
	}
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
		req.Header.Set("X-Flanj-UI", "1")
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
	for _, path := range []string{"/api/flag", "/api/connect", "/api/threads/x/open", "/api/threads/x/close", "/api/threads/x/reopen", "/api/threads/x/replace-link", "/api/findings/x/ack", "/api/findings/x/unack"} {
		t.Run(path, func(t *testing.T) {
			// GET (or any non-POST) → 405 with Allow
			resp, _, _ := r.do(t, http.MethodPut, path, nil)
			if resp.StatusCode != 405 || !strings.Contains(resp.Header.Get("Allow"), "POST") {
				t.Errorf("PUT %s = %d Allow=%q, want 405 POST", path, resp.StatusCode, resp.Header.Get("Allow"))
			}
			// missing the UI header → 403
			resp, out, _ := r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Del("X-Flanj-UI") })
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
	list := decodeThreadList(t, raw)
	if resp.StatusCode != 200 || len(list.Threads) != 1 {
		t.Fatalf("threads: %d %s", resp.StatusCode, raw)
	}
	row := list.Threads[0]
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
	// A thread id this collector holds no record for is NOT refused locally —
	// resolution is not authorization. It goes to the control plane, which
	// authorizes by collector key AND origin match and answers 404 itself.
	resp, out, _ = r.do(t, http.MethodGet, "/api/threads/nope/summary", nil)
	if resp.StatusCode != 404 || out["error"] != "not_found" {
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

// threadListBody is GET /api/threads as the UI sees it: the collector's own
// envelope (its internal shape, not a published contract) around the merged
// rows, carrying total/has_more so the tab can say when it is showing less than
// everything.
type threadListBody struct {
	Threads []map[string]any `json:"threads"`
	Count   int              `json:"count"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	HasMore bool             `json:"has_more"`
}

func decodeThreadList(t *testing.T, raw []byte) threadListBody {
	t.Helper()
	var out threadListBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode /api/threads: %v (%s)", err, raw)
	}
	return out
}

func TestThreadsListSourceIsTheControlPlane(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	// A local record whose thread the CP does NOT list any more (deleted /
	// tombstoned): the CP is the list, so it must not appear.
	_ = saveThread(r.st, threadRecord{ThreadID: "thr_gone", ThreadPublicID: "p", FindingID: "fnd_gone", Endpoint: "POST /v1/charges",
		Provider: "Acme Payments", ThreadURL: "https://cp.test/t/p#k=t", CreatedAt: "2026-08-23T10:00:00Z"})
	// Two CP rows: one this collector has a local record for, one it does not
	// (a wiped local store — §5.5a exists precisely so that no longer loses the list).
	_ = saveThread(r.st, threadRecord{ThreadID: "thr_1", ThreadPublicID: "pub_thr_1", FindingID: "fnd_1", Endpoint: "POST /v1/charges",
		Provider: "Acme Payments", Integration: "acme-payments", ThreadURL: "https://cp.test/t/pub_thr_1#k=tok_1", CreatedAt: "2026-08-23T10:00:00Z"})
	r.cp.mu.Lock()
	r.cp.state["thr_1"], r.cp.state["thr_2"] = "open", "open"
	r.cp.track("thr_1")
	r.cp.track("thr_2")
	r.cp.mu.Unlock()

	resp, _, raw := r.do(t, http.MethodGet, "/api/threads", nil)
	body := decodeThreadList(t, raw)
	list := body.Threads
	if resp.StatusCode != 200 || len(list) != 2 {
		t.Fatalf("threads: %d %s", resp.StatusCode, raw)
	}
	if body.Count != 2 || body.Total != 2 || body.HasMore || body.Limit != 200 {
		t.Errorf("envelope: %+v", body)
	}
	// ONE list call, not one summary per thread.
	r.cp.mu.Lock()
	calls, query := r.cp.listCalls, r.cp.listQuery
	r.cp.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected exactly one CP list call, got %d", calls)
	}
	if query != "limit=200" {
		t.Errorf("limit must be the §5.5a hard cap, got %q", query)
	}
	// CP order is the order (most-recently-active first): thr_2 then thr_1.
	if list[0]["thread_id"] != "thr_2" || list[1]["thread_id"] != "thr_1" {
		t.Errorf("CP order not preserved: %v", []any{list[0]["thread_id"], list[1]["thread_id"]})
	}
	// The merged row: CP state + the local join fields.
	merged := list[1]
	if merged["finding_id"] != "fnd_1" || merged["integration"] != "acme-payments" || merged["thread_url"] != "https://cp.test/t/pub_thr_1#k=tok_1" {
		t.Errorf("local fields not merged onto the CP row: %v", merged)
	}
	sum, _ := merged["summary"].(map[string]any)
	if sum == nil || sum["state"] != "open" || sum["turn"] != "waiting_on_provider" || sum["opened_count"] != float64(2) ||
		sum["link"].(map[string]any)["status"] != "active" || sum["consumer_display_name"] != "Acme Consumer Ltd" ||
		sum["updated_at"] != "2026-08-23T11:00:00Z" {
		t.Errorf("summary: %v", merged["summary"])
	}
	// A CP row with no local record still renders — only the link copy is
	// missing, so the UI disables Copy thread link for it.
	orphan := list[0]
	if orphan["thread_url"] != "" || orphan["finding_id"] != "" {
		t.Errorf("a CP row with no local record must carry no link: %v", orphan)
	}
	if orphan["endpoint"] != "POST /v1/charges" || orphan["provider"] != "Acme Payments" || orphan["created_at"] != "2026-08-23T10:00:00Z" {
		t.Errorf("CP fields must still render on an orphan row: %v", orphan)
	}
	// …and its operations are NOT stranded, WITHOUT the list having written
	// anything: a thread the CP listed resolves to a minimal record, and the CP
	// authorizes the operation itself.
	resp, out, _ := r.do(t, http.MethodPost, "/api/threads/thr_2/close", map[string]string{})
	if resp.StatusCode != 200 || out["state"] != "closed" {
		t.Errorf("close on a CP row with no local record: %d %v", resp.StatusCode, out)
	}
}

// TestThreadsListWritesNoPointers is the regression for the orphaning blocker:
// GET /api/threads must not write ANY settings key.
//
// It used to Get-then-Put an empty pointer for a thread it could not resolve —
// a check-then-act on a key two pods share. With the 5s Threads poll running
// while the Flag sheet is open, a flag landing between that Get and that Put had
// its real `thread.id.<id> -> <finding id>` pointer overwritten with "", leaving
// the record that holds the live thread link with no key reaching it and no
// repair path (the empty value read as "known"). An absent pointer already means
// exactly what an empty one meant, so the write is gone entirely.
func TestThreadsListWritesNoPointers(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	r.cp.mu.Lock()
	r.cp.state["thr_1"] = "open"
	r.cp.track("thr_1")
	r.cp.mu.Unlock()

	before := r.st.settingsSnapshot()
	resp, _, raw := r.do(t, http.MethodGet, "/api/threads", nil)
	if resp.StatusCode != 200 || len(decodeThreadList(t, raw).Threads) != 1 {
		t.Fatalf("threads: %d %s", resp.StatusCode, raw)
	}
	if after := r.st.settingsSnapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("the list path wrote to the settings KV: before=%v after=%v", before, after)
	}

	// The exact interleaving that used to orphan the record: the poll listed
	// thr_1 (above, finding nothing), then the flag persists the real pointer.
	// A second poll must leave it alone.
	rec := threadRecord{ThreadID: "thr_1", ThreadPublicID: "pub_thr_1", FindingID: "fnd_1", Endpoint: "POST /v1/charges",
		Provider: "Acme Payments", ThreadURL: "https://cp.test/t/pub_thr_1#k=tok_1", CreatedAt: "2026-08-23T10:00:00Z"}
	if err := saveThread(r.st, rec); err != nil {
		t.Fatal(err)
	}
	_, _, raw = r.do(t, http.MethodGet, "/api/threads", nil)
	row := decodeThreadList(t, raw).Threads[0]
	if row["finding_id"] != "fnd_1" || row["thread_url"] != "https://cp.test/t/pub_thr_1#k=tok_1" {
		t.Errorf("the poll orphaned the record it raced: %v", row)
	}
	if got, _, _ := r.st.GetSetting(settingThreadIDPrefix + "thr_1"); got != "fnd_1" {
		t.Errorf("thread.id.thr_1 = %q, want fnd_1", got)
	}
}

// TestThreadsReplaceLinkPersistsOnEveryRow is the regression for the
// revoke-and-drop blocker: the control plane kills every outstanding token the
// moment it mints a new one, so a Replace link the collector does not persist
// leaves a thread with zero working links and no way to make another. It must be
// stored for ANY row — including one with no finding record, which gets its own
// thread.link.<thread_id> key (a single blind write, never a read-modify-write).
func TestThreadsReplaceLinkPersistsOnEveryRow(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	r.cp.mu.Lock()
	r.cp.state["thr_2"] = "open"
	r.cp.track("thr_2")
	r.cp.mu.Unlock()

	// The row as the tab sees it before: no local record, so no link to copy.
	_, _, raw := r.do(t, http.MethodGet, "/api/threads", nil)
	if row := decodeThreadList(t, raw).Threads[0]; row["thread_url"] != "" || row["finding_id"] != "" {
		t.Fatalf("expected a record-less row, got %v", row)
	}
	resp, out, _ := r.do(t, http.MethodPost, "/api/threads/thr_2/replace-link", map[string]string{})
	want := "https://cp.test/t/pub_thr_2#k=replaced_1"
	if resp.StatusCode != 200 || out["thread_url"] != want {
		t.Fatalf("replace-link: %d %v", resp.StatusCode, out)
	}
	if got, ok, _ := r.st.GetSetting(settingThreadLinkPrefix + "thr_2"); !ok || got != want {
		t.Errorf("thread.link.thr_2 = %q ok=%v, want %q", got, ok, want)
	}
	// …and the next list — the one the tab triggers right after Replace link —
	// carries it, so Copy thread link is enabled without a reload.
	_, _, raw = r.do(t, http.MethodGet, "/api/threads", nil)
	if row := decodeThreadList(t, raw).Threads[0]; row["thread_url"] != want {
		t.Errorf("the new link must reach the row: %v", row)
	}
	// No finding record was invented for it, and no empty pointer either.
	if _, ok, _ := r.st.GetSetting(settingThreadIDPrefix + "thr_2"); ok {
		t.Errorf("a record-less row must not gain a pointer")
	}
	// A finding-originated thread still persists onto its record (unchanged).
	_ = saveThread(r.st, threadRecord{ThreadID: "thr_1", FindingID: "fnd_1", ThreadURL: "old", CreatedAt: "2026-08-23T10:00:00Z"})
	r.cp.mu.Lock()
	r.cp.state["thr_1"] = "open"
	r.cp.mu.Unlock()
	if resp, _, _ := r.do(t, http.MethodPost, "/api/threads/thr_1/replace-link", map[string]string{}); resp.StatusCode != 200 {
		t.Fatalf("replace-link on a record row: %d", resp.StatusCode)
	}
	if rec, ok, _ := loadThread(r.st, "fnd_1"); !ok || rec.ThreadURL != "https://cp.test/t/pub_thr_1#k=replaced_2" {
		t.Errorf("record row link: %+v ok=%v", rec, ok)
	}
	if _, ok, _ := r.st.GetSetting(settingThreadLinkPrefix + "thr_1"); ok {
		t.Errorf("a row WITH a record must keep its link on the record only")
	}
}

// TestFindThreadByIDRejectsAStalePointer: thread.finding.<fid> is rewritten in
// place when a finding is re-flagged onto a new thread, and the KV has no
// delete, so the OLD thread.id.<old> pointer survives and still names that
// finding. Following it would hand back a record whose ThreadID is the NEW
// thread — and every caller acts on rec.ThreadID, so a stale tab's Close would
// close the wrong thread and Replace link would revoke the wrong live link.
func TestFindThreadByIDRejectsAStalePointer(t *testing.T) {
	st := newFakeStore()
	// The pre-state: fnd_1 is on thr_old.
	if err := saveThread(st, threadRecord{ThreadID: "thr_old", FindingID: "fnd_1", ThreadURL: "https://cp.test/t/pub_old#k=old"}); err != nil {
		t.Fatal(err)
	}
	// Re-flag moves the SAME record to thr_new (handlers.go sets rec.ThreadID);
	// thread.id.thr_old is never deleted.
	if err := saveThread(st, threadRecord{ThreadID: "thr_new", FindingID: "fnd_1", ThreadURL: "https://cp.test/t/pub_new#k=new"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.GetSetting(settingThreadIDPrefix + "thr_old"); got != "fnd_1" {
		t.Fatalf("the repro needs the stale pointer to survive, got %q", got)
	}
	rec, joined, err := findThreadByID(st, "thr_old")
	if err != nil {
		t.Fatal(err)
	}
	if joined {
		t.Errorf("a stale pointer must not count as a local record: %+v", rec)
	}
	if rec.ThreadID != "thr_old" {
		t.Fatalf("findThreadByID(thr_old).ThreadID = %q — operations would hit the wrong thread", rec.ThreadID)
	}
	if rec.ThreadURL != "" || rec.FindingID != "" {
		t.Errorf("a stale pointer must carry none of the other thread's fields: %+v", rec)
	}
	// The live pointer still resolves normally.
	if got, ok, _ := findThreadByID(st, "thr_new"); !ok || got.ThreadURL != "https://cp.test/t/pub_new#k=new" {
		t.Errorf("findThreadByID(thr_new) = %+v ok=%v", got, ok)
	}
}

// TestStaleThreadPointerOperatesOnTheRequestedThread is the same defect at the
// route: a `#thread=thr_old` deep link (or a stale tab) posting close must close
// thr_old, never the thread the rewritten record now points at.
func TestStaleThreadPointerOperatesOnTheRequestedThread(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	_ = saveThread(r.st, threadRecord{ThreadID: "thr_old", FindingID: "fnd_1", ThreadURL: "https://cp.test/t/pub_old#k=old"})
	_ = saveThread(r.st, threadRecord{ThreadID: "thr_new", FindingID: "fnd_1", ThreadURL: "https://cp.test/t/pub_new#k=new"})
	r.cp.mu.Lock()
	r.cp.state["thr_old"], r.cp.state["thr_new"] = "open", "open"
	r.cp.mu.Unlock()

	resp, out, _ := r.do(t, http.MethodPost, "/api/threads/thr_old/close", map[string]string{})
	if resp.StatusCode != 200 || out["state"] != "closed" {
		t.Fatalf("close thr_old: %d %v", resp.StatusCode, out)
	}
	r.cp.mu.Lock()
	oldState, newState := r.cp.state["thr_old"], r.cp.state["thr_new"]
	r.cp.mu.Unlock()
	if oldState != "closed" || newState != "open" {
		t.Errorf("close hit the wrong thread: thr_old=%s thr_new=%s", oldState, newState)
	}
	// Replace link on the stale id must not touch the live record's link.
	if resp, _, _ := r.do(t, http.MethodPost, "/api/threads/thr_old/replace-link", map[string]string{}); resp.StatusCode != 200 {
		t.Fatalf("replace-link thr_old: %d", resp.StatusCode)
	}
	if rec, _, _ := loadThread(r.st, "fnd_1"); rec.ThreadURL != "https://cp.test/t/pub_new#k=new" {
		t.Errorf("the live record's link was overwritten from a stale id: %+v", rec)
	}
	if got, ok, _ := r.st.GetSetting(settingThreadLinkPrefix + "thr_old"); !ok || got != "https://cp.test/t/pub_thr_old#k=replaced_1" {
		t.Errorf("the stale id's new link must live on its own key, got %q ok=%v", got, ok)
	}
}

// TestThreadsListTruncationIsHonest: §5.5a has no cursor, so a collector with
// more threads than the hard cap gets a short list. That has to be visible —
// total and has_more are relayed, never decoded and dropped.
func TestThreadsListTruncationIsHonest(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	r.cp.mu.Lock()
	r.cp.state["thr_1"] = "open"
	r.cp.track("thr_1")
	r.cp.listTotal, r.cp.listHasMore = 250, true
	r.cp.mu.Unlock()

	_, _, raw := r.do(t, http.MethodGet, "/api/threads", nil)
	body := decodeThreadList(t, raw)
	if body.Count != 1 || body.Total != 250 || !body.HasMore || body.Limit != 200 {
		t.Errorf("truncation must be relayed, got %+v", body)
	}
}

// TestThreadsListNotConnectedIsNotAnEmptyList: a collector that cannot reach or
// authenticate to the control plane has no idea whether it has threads, so it
// must not answer with a list that says it has none. Both cases use the code
// handleThreadSummary already uses.
func TestThreadsListNotConnectedIsNotAnEmptyList(t *testing.T) {
	r := newRig(t)
	// No CP configured at all: e.cp is nil until start().
	r.ui = httptest.NewServer(r.ext.routes())
	t.Cleanup(r.ui.Close)
	resp, out, _ := r.do(t, http.MethodGet, "/api/threads", nil)
	if resp.StatusCode != 503 || out["error"] != "cp_not_configured" || out["message"] != msgCPNotConfigured {
		t.Errorf("no control plane configured: %d %v", resp.StatusCode, out)
	}
	if _, has := out["threads"]; has {
		t.Errorf("a collector that cannot list must not answer with a list: %v", out)
	}

	r2 := newRig(t)
	r2.start(t)
	resp, out, _ = r2.do(t, http.MethodGet, "/api/threads", nil)
	if resp.StatusCode != 412 || out["error"] != "not_connected" || out["message"] != msgThreadsNotConnected {
		t.Errorf("not connected: %d %v", resp.StatusCode, out)
	}
	if r2.cp.listCalls != 0 {
		t.Errorf("not connected must not reach the CP")
	}
}

// TestThreadsListErrorStates: there is no local enumeration any more, so a CP
// failure is an honest error, never a stale list.
func TestThreadsListErrorStates(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	// A typed CP error passes through with its status + code.
	r.cp.mu.Lock()
	r.cp.listStatus = 429
	r.cp.mu.Unlock()
	resp, out, _ := r.do(t, http.MethodGet, "/api/threads", nil)
	if resp.StatusCode != 429 || out["error"] != "rate_limited" {
		t.Errorf("CP error pass-through: %d %v", resp.StatusCode, out)
	}
	// CP unreachable → 502 cp_unreachable, never a stale list.
	r.cp.srv.Close()
	resp, out, _ = r.do(t, http.MethodGet, "/api/threads", nil)
	if resp.StatusCode != 502 || out["error"] != "cp_unreachable" || out["message"] != msgCPUnreachable {
		t.Errorf("CP down: %d %v", resp.StatusCode, out)
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

// TestFlagRefusesLocalOnlyKinds (v0.5 §4.C.4, amended qfix2-2026-08-26): the
// relay REFUSES to flag local-only finding kinds SERVER-SIDE — stale_client,
// and ONLY stale_client — even for a fully Connected collector. Nothing reaches
// the CP for those. Every flaggable kind still flags, including a DESCRIPTION
// definition change, which is also CALL-LESS: no 400 finding_has_no_call, and
// the promoted body carries the finding with NO call.
func TestFlagRefusesLocalOnlyKinds(t *testing.T) {
	r := newRig(t)
	r.start(t)
	// Connected with a confirmed contact — the refusal is about the KIND, not the gate.
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme",
		ContactEmail: "ops@acme.test", ContactStatus: "confirmed", ConfirmedContactEmail: "ops@acme.test"})
	r.cp.mu.Lock()
	r.cp.contactEmail, r.cp.contactStatus, r.cp.confirmedEmail = "ops@acme.test", "confirmed", "ops@acme.test"
	r.cp.mu.Unlock()

	callID := "call_mcp_1"
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-08-24T10:00:00Z",
		Integration: "acme-payments", Direction: "client", Method: "tools/call", Route: "/old_refund",
		URL: "mcp://mcp.acme.test/old_refund", RequestBody: "{}", ResponseBody: "{}",
		Transport: "mcp", MCPToolName: "old_refund", Redaction: model.Redaction{Patterns: []string{}}})
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_stale", Kind: model.KindStaleClient,
		Severity: model.SeverityWarning, Integration: "acme-payments", Endpoint: "old_refund",
		Expected: "a tool declared in the current tools/list", Actual: "tools/call to `old_refund` (not listed)",
		Rule: "tool-not-listed", SourceCallID: &callID, DetectedAt: "2026-08-24T10:00:01Z"})
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_desc", Kind: model.KindDefinitionChange,
		Severity: model.SeverityWarning, Integration: "acme-payments", Endpoint: "create_refund",
		Expected: "\"Refund a charge.\"", Actual: "\"Refund a charge, with fees.\"",
		Rule: model.RuleDescriptionChanged, DetectedAt: "2026-08-24T10:00:01Z"})
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_mismatch", Kind: model.KindOutputMismatch,
		Severity: model.SeverityBreaking, Integration: "acme-payments", Endpoint: "create_refund",
		Expected: "type=integer", Actual: `type=string ("1200")`, Rule: "type-mismatch",
		SourceCallID: &callID, DetectedAt: "2026-08-24T10:00:01Z"})

	// stale_client is the ONLY server-side refusal. Never a flag control
	// anywhere, never a path out of this collector.
	resp, out, _ := r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_stale"})
	if resp.StatusCode != 403 || out["error"] != "not_flaggable" {
		t.Errorf("flag fnd_stale = %d %v, want 403 not_flaggable", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Fatalf("a local-only finding reached the CP (%d flag calls)", r.cp.flagCalls)
	}

	// The flaggable MCP kind goes through unchanged.
	resp, out, raw := r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_mismatch"})
	if resp.StatusCode != 201 || out["thread_url"] == "" {
		t.Fatalf("flag output_mismatch: %d %s", resp.StatusCode, raw)
	}
	if r.cp.flagCalls != 1 {
		t.Errorf("flag calls = %d, want 1", r.cp.flagCalls)
	}
	if kind, _ := r.cp.lastFlagBody["finding"].(map[string]any)["kind"].(string); kind != model.KindOutputMismatch {
		t.Errorf("promoted finding kind = %q", kind)
	}
	if _, has := r.cp.lastFlagBody["call"]; !has {
		t.Errorf("an output_mismatch flag must carry its failing call: %v", r.cp.lastFlagBody)
	}

	// CALL-LESS FLAGGING (ux-design-v2 §2.7.5): a DESCRIPTION definition change
	// has no source call at all. It must NOT 400 finding_has_no_call, and the
	// body must omit `call` rather than invent one.
	resp, out, raw = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_desc"})
	if (resp.StatusCode != 201 && resp.StatusCode != 200) || out["thread_url"] == "" {
		t.Fatalf("flag DESCRIPTION definition change: %d %s", resp.StatusCode, raw)
	}
	if r.cp.flagCalls != 2 {
		t.Errorf("flag calls = %d, want 2", r.cp.flagCalls)
	}
	if _, has := r.cp.lastFlagBody["call"]; has {
		t.Errorf("a call-less flag must not carry a `call` key: %v", r.cp.lastFlagBody)
	}
	fnd, _ := r.cp.lastFlagBody["finding"].(map[string]any)
	if fnd == nil || fnd["kind"] != model.KindDefinitionChange || fnd["rule"] != model.RuleDescriptionChanged {
		t.Errorf("promoted finding = %v, want the DESCRIPTION definition change", fnd)
	}

	// v1p4-2026-09-08: the lift is no longer scoped to definition_change. A
	// finding that has no source call at ALL — a version diff, or an
	// output_mismatch whose call was never stored — now flags CALL-LESS instead
	// of answering 400 finding_has_no_call, because the message carries the ask.
	// This is the 400 gap closed by design (v1-build-spec §3 Step 4, ruling 3).
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_mismatch_nocall", Kind: model.KindOutputMismatch,
		Severity: model.SeverityBreaking, Integration: "acme-payments", Endpoint: "list_transactions",
		Expected: "type=integer", Actual: `type=string ("1200")`, Rule: "type-mismatch",
		DetectedAt: "2026-08-24T10:00:01Z"})
	resp, out, raw = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_mismatch_nocall"})
	if (resp.StatusCode != 201 && resp.StatusCode != 200) || out["thread_url"] == "" {
		t.Fatalf("call-less output_mismatch: %d %s", resp.StatusCode, raw)
	}
	if _, has := r.cp.lastFlagBody["call"]; has {
		t.Errorf("a call-less flag must not carry a `call` key: %v", r.cp.lastFlagBody)
	}
	// It still carries a message — that is what makes it acceptable at the CP.
	if msg, _ := r.cp.lastFlagBody["message"].(string); strings.TrimSpace(msg) == "" {
		t.Errorf("a call-less flag must carry a message: %v", r.cp.lastFlagBody)
	}
}

// TestFlagEvictedCallStillRefuses: the v1p4 widening lifts the 400 for a finding
// that never had a call. It does NOT quietly downgrade a flag whose call was
// EVICTED — the sheet showed the operator an "Evidence (1)" line for that call,
// so sending a call-less thread instead would create a thread they did not mean
// to create. Only a definition_change is exempt: it never had a call to lose.
func TestFlagEvictedCallStillRefuses(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme",
		ContactEmail: "ops@acme.test", ContactStatus: "confirmed", ConfirmedContactEmail: "ops@acme.test"})
	r.cp.mu.Lock()
	r.cp.contactEmail, r.cp.contactStatus, r.cp.confirmedEmail = "ops@acme.test", "confirmed", "ops@acme.test"
	r.cp.mu.Unlock()

	gone := "call_evicted"
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_evicted", Kind: model.KindOutputMismatch,
		Severity: model.SeverityBreaking, Integration: "acme-payments", Endpoint: "list_transactions",
		Expected: "type=integer", Actual: `type=string ("1200")`, Rule: "type-mismatch",
		SourceCallID: &gone, DetectedAt: "2026-08-24T10:00:01Z"})

	resp, out, _ := r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_evicted"})
	if resp.StatusCode != 404 || out["error"] != "call_not_found" {
		t.Errorf("evicted call = %d %v, want 404 call_not_found", resp.StatusCode, out)
	}
	if r.cp.flagCalls != 0 {
		t.Errorf("flag calls = %d, want 0 — nothing may reach the CP", r.cp.flagCalls)
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

// TestSaveThreadWritesBothKeys replaces the retired TestSaveThreadIndex.
//
// `threads.index` was a JSON array behind a KV with no compare-and-swap, so
// adding an id was a read-modify-write with a documented residual lost-update
// race (errIndexRace). It is gone. saveThread now writes TWO independent
// single-key values — the record and its reverse pointer — and a single-key
// write has no read-modify-write, so the race does not move, it disappears.
func TestSaveThreadWritesBothKeys(t *testing.T) {
	st := newFakeStore()
	a := threadRecord{ThreadID: "thr_a", FindingID: "fnd_a", ThreadURL: "https://cp.test/t/pa#k=t", CreatedAt: "2026-08-23T10:00:00Z"}
	b := threadRecord{ThreadID: "thr_b", FindingID: "fnd_b", CreatedAt: "2026-08-23T10:01:00Z"}
	for _, rec := range []threadRecord{a, b, a /* re-save is idempotent */} {
		if err := saveThread(st, rec); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ key, want string }{
		{settingThreadIDPrefix + "thr_a", "fnd_a"},
		{settingThreadIDPrefix + "thr_b", "fnd_b"},
	} {
		got, ok, _ := st.GetSetting(tc.key)
		if !ok || got != tc.want {
			t.Errorf("%s = %q ok=%v, want %q", tc.key, got, ok, tc.want)
		}
	}
	if _, ok, _ := st.GetSetting(settingThreadsIndex); ok {
		t.Errorf("nothing may write the retired threads.index any more")
	}
	// findThreadByID is a direct pointer lookup, never a scan.
	got, ok, err := findThreadByID(st, "thr_a")
	if err != nil || !ok || got.FindingID != "fnd_a" || got.ThreadURL != a.ThreadURL {
		t.Fatalf("findThreadByID(thr_a) = %+v ok=%v err=%v", got, ok, err)
	}
	// An unknown id resolves to a MINIMAL record, never a miss: the thread may
	// well exist on the control plane, which is the only thing that authorizes
	// an operation on it. `joined` is false because there is no local record.
	got, joined, err := findThreadByID(st, "thr_unknown")
	if err != nil || joined || got.ThreadID != "thr_unknown" || got.FindingID != "" || got.ThreadURL != "" {
		t.Errorf("unknown id = %+v joined=%v err=%v", got, joined, err)
	}
	if got, joined, _ := findThreadByID(st, ""); joined || got.ThreadID != "" {
		t.Errorf("an empty thread id must not resolve: %+v", got)
	}
	// The reverse pointers of two findings are DIFFERENT keys, so two writers
	// racing on them cannot lose each other's entry the way the array could.
	// The extreme: the finding record itself is wiped (a lost local store) but
	// the CP still lists the thread — the row still renders and its operations
	// still work, off the minimal record.
	if err := st.PutSetting(settingThreadIDPrefix+"thr_c", "fnd_wiped"); err != nil {
		t.Fatal(err)
	}
	got, joined, err = findThreadByID(st, "thr_c")
	if err != nil || joined || got.ThreadID != "thr_c" || got.FindingID != "" || got.ThreadURL != "" {
		t.Errorf("record-less pointer = %+v joined=%v err=%v", got, joined, err)
	}
	// A Replace link on such a row parks the new link on its own key, and that
	// is what the next resolution hands back.
	if err := persistThreadLink(st, got, "https://cp.test/t/pc#k=fresh"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := findThreadByID(st, "thr_c"); got.ThreadURL != "https://cp.test/t/pc#k=fresh" {
		t.Errorf("the parked link must come back: %+v", got)
	}
	// A record with a thread id but no finding id must never write an EMPTY
	// pointer — that value is what used to orphan records.
	if err := saveThread(st, threadRecord{ThreadID: "thr_e"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetSetting(settingThreadIDPrefix + "thr_e"); ok {
		t.Errorf("an empty finding id must not create a pointer key")
	}
	// A record with no thread id yet (never flagged) writes no pointer.
	if err := saveThread(st, threadRecord{FindingID: "fnd_d"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetSetting(settingThreadIDPrefix); ok {
		t.Errorf("an empty thread id must not create a pointer key")
	}
}

// TestLegacyThreadIndexNeverCleared: the previous version derives its ENTIRE
// thread list from `threads.index`, so this version must never clear it. It
// reads the array — lazily, only when a listed thread has no pointer — and
// writes back the REAL finding id as a single blind PutSetting.
func TestLegacyThreadIndexNeverCleared(t *testing.T) {
	st := newFakeStore()
	// The pre-upgrade world by hand: records + the array (plus one id the array
	// names but has no record, and one record with no thread id — both skipped,
	// neither an error).
	for _, rec := range []threadRecord{
		{ThreadID: "thr_a", FindingID: "fnd_a", ThreadURL: "https://cp.test/t/pa#k=t"},
		{ThreadID: "thr_b", FindingID: "fnd_b"},
		{FindingID: "fnd_noid"},
	} {
		b, _ := json.Marshal(rec)
		_ = st.PutSetting(settingThreadPrefix+rec.FindingID, string(b))
	}
	const index = `["fnd_a","fnd_b","fnd_noid","fnd_missing"]`
	_ = st.PutSetting(settingThreadsIndex, index)

	var legacy legacyIndex
	for tid, want := range map[string]string{"thr_a": "fnd_a", "thr_b": "fnd_b", "thr_unknown": ""} {
		got, err := legacy.findingFor(st, tid)
		if err != nil || got != want {
			t.Errorf("findingFor(%s) = %q err=%v, want %q", tid, got, err, want)
		}
	}
	if raw, _, _ := st.GetSetting(settingThreadsIndex); raw != index {
		t.Fatalf("threads.index is another version's data and must be left alone, got %q", raw)
	}
	// Corrupt and absent arrays recover nothing and are never errors.
	st2 := newFakeStore()
	_ = st2.PutSetting(settingThreadsIndex, "{not json")
	var l2 legacyIndex
	if got, err := l2.findingFor(st2, "thr_a"); err != nil || got != "" {
		t.Errorf("corrupt index: %q %v", got, err)
	}
	var l3 legacyIndex
	if got, err := l3.findingFor(newFakeStore(), "thr_a"); err != nil || got != "" {
		t.Errorf("absent index: %q %v", got, err)
	}
}

// TestLegacyThreadIndexRecoveryIsLazyAndRepeatable: pointer recovery happens
// when the control plane LISTS a thread no pointer resolves — per thread, per
// request, idempotent.
//
// The retired migration did this once per process behind a sync.Once and then
// cleared the array. Both halves broke a rolling upgrade: an old pod's list went
// empty for the rest of the rollout, and any thread an old pod created in that
// window found the new pod's sync.Once already spent. This has no once and
// clears nothing, so a thread that appears later still heals.
func TestLegacyThreadIndexRecoveryIsLazyAndRepeatable(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme", ContactEmail: "ops@acme.test", ContactStatus: "confirmed"})
	b, _ := json.Marshal(threadRecord{ThreadID: "thr_1", ThreadPublicID: "pub_thr_1", FindingID: "fnd_1", Endpoint: "POST /v1/charges",
		Provider: "Acme Payments", Integration: "acme-payments", ThreadURL: "https://cp.test/t/pub_thr_1#k=tok_1", CreatedAt: "2026-08-23T10:00:00Z"})
	_ = r.st.PutSetting(settingThreadPrefix+"fnd_1", string(b))
	_ = r.st.PutSetting(settingThreadsIndex, `["fnd_1"]`) // the pre-upgrade world, pointer-less
	r.cp.mu.Lock()
	r.cp.state["thr_1"] = "open"
	r.cp.track("thr_1")
	r.cp.mu.Unlock()

	_, _, raw := r.do(t, http.MethodGet, "/api/threads", nil)
	rows := decodeThreadList(t, raw).Threads
	if len(rows) != 1 || rows[0]["finding_id"] != "fnd_1" || rows[0]["thread_url"] != "https://cp.test/t/pub_thr_1#k=tok_1" {
		t.Fatalf("the legacy record must be joined back on: %s", raw)
	}
	// The REAL finding id was written — never an empty placeholder.
	if got, ok, _ := r.st.GetSetting(settingThreadIDPrefix + "thr_1"); !ok || got != "fnd_1" {
		t.Errorf("recovered pointer = %q ok=%v, want fnd_1", got, ok)
	}
	// The array survives: the previous version still lists from it.
	if raw, _, _ := r.st.GetSetting(settingThreadsIndex); raw != `["fnd_1"]` {
		t.Errorf("threads.index must not be cleared, got %q", raw)
	}
	// A thread an OLD pod creates during the rollout (record + index entry, no
	// pointer) heals on the NEXT list — there is no once to have been spent.
	b2, _ := json.Marshal(threadRecord{ThreadID: "thr_2", ThreadPublicID: "pub_thr_2", FindingID: "fnd_2",
		Endpoint: "POST /v1/refunds", Provider: "Acme Payments", ThreadURL: "https://cp.test/t/pub_thr_2#k=tok_2"})
	_ = r.st.PutSetting(settingThreadPrefix+"fnd_2", string(b2))
	_ = r.st.PutSetting(settingThreadsIndex, `["fnd_1","fnd_2"]`)
	r.cp.mu.Lock()
	r.cp.state["thr_2"] = "open"
	r.cp.track("thr_2")
	r.cp.mu.Unlock()

	_, _, raw = r.do(t, http.MethodGet, "/api/threads", nil)
	rows = decodeThreadList(t, raw).Threads
	if len(rows) != 2 || rows[0]["thread_id"] != "thr_2" || rows[0]["thread_url"] != "https://cp.test/t/pub_thr_2#k=tok_2" {
		t.Fatalf("a thread that appeared after the first list must still heal: %s", raw)
	}
	if got, ok, _ := r.st.GetSetting(settingThreadIDPrefix + "thr_2"); !ok || got != "fnd_2" {
		t.Errorf("recovered pointer = %q ok=%v, want fnd_2", got, ok)
	}
}

// TestFindingAckFlow (qfix-2026-08-25): local acknowledge for INFORMATIONAL
// findings only — definition_change with class DESCRIPTION or NON-BREAKING —
// keyed by SIGNATURE in the settings KV, joined onto GET /api/findings, and
// guarded WITHOUT the control-plane check (it works with cp unconfigured, and
// nothing ever reaches the CP). Breaking rows, output_mismatch, stale_client
// and live-vs-spec are never ackable; Flaggable() / 403 not_flaggable are
// untouched by any of this.
func TestFindingAckFlow(t *testing.T) {
	r := newRig(t)
	r.start(t)
	// LOCAL-ONLY: the ack routes must work with NO control plane configured.
	r.ext.cp = nil

	callID := "call_mcp_1"
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-08-25T10:00:00Z",
		Integration: "acme-payments", Direction: "client", Method: "tools/call", Route: "/get_balance",
		Transport: "mcp", MCPToolName: "get_balance", RequestBody: "{}", ResponseBody: "{}",
		Redaction: model.Redaction{Patterns: []string{}}})
	mk := func(id, kind, severity, endpoint, rule string, src *string) model.Finding {
		f := model.Finding{SchemaVersion: 1, ID: id, Kind: kind, Severity: severity, Integration: "acme-payments",
			Endpoint: endpoint, Expected: "x", Actual: "y", Rule: rule, SourceCallID: src, DetectedAt: "2026-08-25T10:00:01Z"}
		f.Signature = f.ComputeSignature()
		return f
	}
	desc := mk("fnd_desc", model.KindDefinitionChange, model.SeverityWarning, "create_refund", model.RuleDescriptionChanged, nil)
	nonbr := mk("fnd_nonbr", model.KindDefinitionChange, model.SeverityInfo, "list_transactions", "output-schema-declared", nil)
	brk := mk("fnd_brk", model.KindDefinitionChange, model.SeverityBreaking, "get_balance", "output-property-type-changed", nil)
	stale := mk("fnd_stale", model.KindStaleClient, model.SeverityWarning, "old_refund", "tool-not-listed", &callID)
	mism := mk("fnd_mism", model.KindOutputMismatch, model.SeverityBreaking, "get_balance", "type-mismatch", &callID)
	for _, f := range []model.Finding{desc, nonbr, brk, stale, mism} {
		_ = r.st.InsertFinding(f)
	}

	ackedOf := func(raw []byte) map[string]map[string]any {
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

	// 1. ack the DESCRIPTION row → 200, KV keyed by signature, index updated
	resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_desc/ack", map[string]string{})
	if resp.StatusCode != 200 || out["acked"] != true || out["acked_at"] == "" || out["acked_at"] == nil {
		t.Fatalf("ack: %d %v", resp.StatusCode, out)
	}
	if raw, ok := r.st.settings[settingAckPrefix+desc.Signature]; !ok || !strings.Contains(raw, desc.Signature) {
		t.Errorf("ack record not persisted by signature: %v", r.st.settings)
	}
	if sigs, _ := loadAckIndex(r.st); len(sigs) != 1 || sigs[0] != desc.Signature {
		t.Errorf("acks index = %v", sigs)
	}
	// re-ack is idempotent
	if resp, _, _ = r.do(t, http.MethodPost, "/api/findings/fnd_desc/ack", map[string]string{}); resp.StatusCode != 200 {
		t.Errorf("re-ack: %d", resp.StatusCode)
	}

	// 2. GET /api/findings joins the ack state (and only for the acked row)
	_, _, raw := r.do(t, http.MethodGet, "/api/findings", nil)
	rows := ackedOf(raw)
	if rows["fnd_desc"]["acked"] != true || rows["fnd_desc"]["acked_at"] == nil {
		t.Errorf("fnd_desc not acked in the join: %v", rows["fnd_desc"])
	}
	for _, id := range []string{"fnd_nonbr", "fnd_brk", "fnd_stale", "fnd_mism"} {
		if _, has := rows[id]["acked"]; has {
			t.Errorf("%s must not carry acked: %v", id, rows[id])
		}
	}

	// 3. NON-BREAKING is ackable too
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_nonbr/ack", map[string]string{}); resp.StatusCode != 200 || out["acked"] != true {
		t.Fatalf("ack non-breaking: %d %v", resp.StatusCode, out)
	}

	// 4. never ackable: BREAKING, stale_client, output_mismatch, live-vs-spec
	for _, id := range []string{"fnd_brk", "fnd_stale", "fnd_mism", "fnd_1"} {
		resp, out, _ = r.do(t, http.MethodPost, "/api/findings/"+id+"/ack", map[string]string{})
		if resp.StatusCode != 403 || out["error"] != "not_ackable" {
			t.Errorf("ack %s = %d %v, want 403 not_ackable", id, resp.StatusCode, out)
		}
	}
	// unknown finding → 404
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/nope/ack", map[string]string{}); resp.StatusCode != 404 || out["error"] != "finding_not_found" {
		t.Errorf("ack unknown: %d %v", resp.StatusCode, out)
	}

	// 5. signature-keyed: a NEW finding id with the SAME signature (store reset)
	// arrives already acked; a different signature arrives un-acked.
	desc2 := desc
	desc2.ID = "fnd_desc_reborn"
	_ = r.st.InsertFinding(desc2)
	_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
	rows = ackedOf(raw)
	if rows["fnd_desc_reborn"]["acked"] != true {
		t.Errorf("same signature must stay acked across ids: %v", rows["fnd_desc_reborn"])
	}

	// 6. unack puts it back → index cleared, join drops the mark
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_desc/unack", map[string]string{}); resp.StatusCode != 200 || out["acked"] != false {
		t.Fatalf("unack: %d %v", resp.StatusCode, out)
	}
	if sigs, _ := loadAckIndex(r.st); len(sigs) != 1 || sigs[0] != nonbr.Signature {
		t.Errorf("index after unack = %v", sigs)
	}
	_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
	rows = ackedOf(raw)
	if _, has := rows["fnd_desc"]["acked"]; has {
		t.Errorf("fnd_desc still acked after unack: %v", rows["fnd_desc"])
	}
	// unack when not acked is idempotent
	if resp, _, _ = r.do(t, http.MethodPost, "/api/findings/fnd_desc/unack", map[string]string{}); resp.StatusCode != 200 {
		t.Errorf("re-unack: %d", resp.StatusCode)
	}

	// 7. with cp unconfigured the RELAY routes still 503 — the local guard
	// relaxation applies to ack/unack only.
	if resp, out, _ = r.do(t, http.MethodPost, "/api/flag", map[string]string{"finding_id": "fnd_mism"}); resp.StatusCode != 503 || out["error"] != "cp_not_configured" {
		t.Errorf("flag with no cp: %d %v", resp.StatusCode, out)
	}
	// and nothing ever reached the CP from the ack flow
	if r.cp.flagCalls != 0 || r.cp.registerCalls != 0 {
		t.Errorf("ack flow must never touch the CP (flags=%d registers=%d)", r.cp.flagCalls, r.cp.registerCalls)
	}
}

// TestAckKeyedOnEvidenceVersion (qfix2-2026-08-26, ux-design-v2 §2.8; §7 risk 2)
// is THE regression test for silent auto-acknowledgement.
//
// Finding.ComputeSignature() is integration|endpoint|kind|rule|field_path —
// identical for a FIRST and a SECOND description change on the same tool and
// field. Under a signature-only key the second change would arrive silently
// pre-acknowledged and could never be seen. The ack therefore also binds to the
// AFTER-snapshot hash, and this test walks exactly that: ack a description
// change, mutate the same field again, assert the finding renders UN-acked.
//
// PRODUCTION SHAPE. The real store deduplicates on signature, so the second
// change never becomes a second row: it lands on the SAME row, keeping the same
// finding id (the flag idempotency key) while the doc is refreshed with the new
// evidence (internal/store.refreshedFindingDoc —
// TestDefinitionChangeRefreshesEvidence is the oracle for that half, and it runs
// against the real store because this fake keys findings by id and could not
// see a signature-dedup bug at all). This test therefore re-inserts the SAME id
// with a moved after-hash, which is exactly what the store's refresh produces.
func TestAckKeyedOnEvidenceVersion(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.ext.cp = nil // local-only, as always

	mkDesc := func(after, actual string) model.Finding {
		f := model.Finding{SchemaVersion: 1, ID: "fnd_desc", Kind: model.KindDefinitionChange,
			Severity: model.SeverityWarning, Integration: "acme-tools", Endpoint: "create_refund",
			FieldPath: model.Ptr("description"), Expected: `"Refund a charge."`,
			Actual: actual, Rule: model.RuleDescriptionChanged,
			SpecVersionFrom: model.Ptr("sha256:aaaa11112222"), SpecVersionTo: model.Ptr(after),
			DetectedAt: "2026-08-26T10:00:01Z"}
		f.Signature = f.ComputeSignature()
		return f
	}
	v1 := mkDesc("sha256:bbbb33334444", `"Refund a charge, with fees."`)
	v2 := mkDesc("sha256:cccc55556666", `"Refund a charge, fees excluded."`) // SAME row, NEW evidence
	if v1.Signature != v2.Signature {
		t.Fatalf("test premise broken: signatures differ (%q vs %q)", v1.Signature, v2.Signature)
	}
	_ = r.st.InsertFinding(v1)

	ackedOf := func(raw []byte) map[string]map[string]any {
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

	// 1. Acknowledge the first description change. The record binds to the
	//    AFTER hash the row rendered, and the response says which one.
	resp, out, _ := r.do(t, http.MethodPost, "/api/findings/fnd_desc/ack", map[string]string{})
	if resp.StatusCode != 200 || out["acked"] != true {
		t.Fatalf("ack: %d %v", resp.StatusCode, out)
	}
	if out["evidence_version"] != "sha256:bbbb33334444" {
		t.Errorf("ack response evidence_version = %v, want the AFTER snapshot hash", out["evidence_version"])
	}
	var rec ackRecord
	if err := json.Unmarshal([]byte(r.st.settings[settingAckPrefix+v1.Signature]), &rec); err != nil {
		t.Fatalf("ack record: %v", err)
	}
	if rec.EvidenceVersion != "sha256:bbbb33334444" {
		t.Errorf("persisted evidence_version = %q", rec.EvidenceVersion)
	}
	_, _, raw := r.do(t, http.MethodGet, "/api/findings", nil)
	if rows := ackedOf(raw); rows["fnd_desc"]["acked"] != true ||
		rows["fnd_desc"]["acked_evidence_version"] != "sha256:bbbb33334444" {
		t.Fatalf("v1 should be acked with its evidence version: %v", rows["fnd_desc"])
	}

	// 2. <P> changes the SAME description again. Same signature → the same row,
	//    same id, refreshed evidence. THE FINDING MUST COME BACK UN-ACKNOWLEDGED.
	_ = r.st.InsertFinding(v2)
	_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
	rows := ackedOf(raw)
	if rows["fnd_desc"]["actual"] != v2.Actual {
		t.Fatalf("test premise broken: the row still carries the FIRST change's text: %v", rows["fnd_desc"])
	}
	if acked, has := rows["fnd_desc"]["acked"]; has && acked == true {
		t.Fatalf("SILENT AUTO-ACK: a NEW description change inherited the old acknowledgement: %v", rows["fnd_desc"])
	}
	if _, has := rows["fnd_desc"]["acked_evidence_version"]; has {
		t.Errorf("un-acked row must not carry acked_evidence_version: %v", rows["fnd_desc"])
	}

	// 3. Acknowledging the new evidence re-binds the record to the new hash.
	if resp, out, _ = r.do(t, http.MethodPost, "/api/findings/fnd_desc/ack", map[string]string{}); resp.StatusCode != 200 {
		t.Fatalf("re-ack: %d %v", resp.StatusCode, out)
	}
	_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
	if rows = ackedOf(raw); rows["fnd_desc"]["acked"] != true {
		t.Errorf("v2 not acked after acknowledging the new evidence: %v", rows["fnd_desc"])
	}
	// The index never grew a second entry — the KEY is still the signature.
	if sigs, _ := loadAckIndex(r.st); len(sigs) != 1 || sigs[0] != v1.Signature {
		t.Errorf("acks index = %v, want exactly the one signature", sigs)
	}

	// 4. MIGRATION: a LEGACY record (written before evidence versions existed)
	//    carries none, so it does NOT match a definition_change and the finding
	//    re-surfaces un-acknowledged. Fail safe — never silently acked.
	legacy, _ := json.Marshal(ackRecord{Signature: v1.Signature, Rule: v1.Rule, AckedAt: "2026-08-25T09:00:00Z"})
	r.st.settings[settingAckPrefix+v1.Signature] = string(legacy)
	_, _, raw = r.do(t, http.MethodGet, "/api/findings", nil)
	rows = ackedOf(raw)
	if acked, has := rows["fnd_desc"]["acked"]; has && acked == true {
		t.Errorf("legacy ack must not cover a definition_change: %v", rows["fnd_desc"])
	}

	// 5. The optional §2.8 wire fields are accepted and PERSISTED (no UI for
	//    them in this slice — the reason set and the person model are next).
	resp, _, _ = r.do(t, http.MethodPost, "/api/findings/fnd_desc/ack", map[string]any{
		"reason": "we_adapt", "note": "we pin tools/list at v1.2.0", "actor_person_id": "per_1",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("ack with reason/note: %d", resp.StatusCode)
	}
	rec = ackRecord{}
	if err := json.Unmarshal([]byte(r.st.settings[settingAckPrefix+v1.Signature]), &rec); err != nil {
		t.Fatalf("ack record: %v", err)
	}
	if rec.Reason != "we_adapt" || rec.Note != "we pin tools/list at v1.2.0" || rec.ActorPersonID != "per_1" {
		t.Errorf("reason/note/actor not persisted: %+v", rec)
	}
	if rec.EvidenceVersion != "sha256:cccc55556666" {
		t.Errorf("evidence_version = %q, want v2's after-hash", rec.EvidenceVersion)
	}
}

// TestAckOccurrenceCountedKeepsSignatureOnlyKey: the evidence-version binding is
// for definition_change ONLY. An occurrence-counted finding (type-mismatch /
// output_mismatch) carries no evidence version, so its ack keys on the signature
// alone — recurrence there is expected and is surfaced as text, not a re-alarm.
func TestAckOccurrenceCountedKeepsSignatureOnlyKey(t *testing.T) {
	callID := "call_1"
	f := model.Finding{SchemaVersion: 1, ID: "fnd_mism", Kind: model.KindOutputMismatch,
		Severity: model.SeverityBreaking, Integration: "acme-tools", Endpoint: "create_refund",
		FieldPath: model.Ptr("refund.amount"), Expected: "type=integer", Actual: `type=string ("1200")`,
		Rule: "type-mismatch", SourceCallID: &callID, DetectedAt: "2026-08-26T10:00:01Z",
		SpecVersionTo: model.Ptr("sha256:bbbb33334444")} // even WITH a snapshot hash
	f.Signature = f.ComputeSignature()
	if got := ackEvidenceVersion(f); got != "" {
		t.Errorf("ackEvidenceVersion(output_mismatch) = %q, want empty", got)
	}
	// A record with no evidence version matches it, at any occurrence count.
	if !ackMatches(f, ackRecord{Signature: f.Signature}) {
		t.Error("an occurrence-counted finding must key on the signature alone")
	}
	f.OccurrenceCount = 47
	if !ackMatches(f, ackRecord{Signature: f.Signature}) {
		t.Error("recurrence must NOT un-acknowledge an occurrence-counted finding")
	}
	// A definition_change with no after-hash at all has nothing to version, so
	// it keeps signature-only behaviour rather than becoming un-ackable.
	d := model.Finding{Kind: model.KindDefinitionChange, Rule: model.RuleDescriptionChanged}
	if !ackMatches(d, ackRecord{}) {
		t.Error("a definition_change with no after-hash must stay signature-keyed")
	}
	if ackMatches(model.Finding{Kind: model.KindDefinitionChange, SpecVersionTo: model.Ptr("sha256:x")}, ackRecord{}) {
		t.Error("a definition_change WITH an after-hash must not match a record without one")
	}
}

// TestHeldPriorDataProbe (qfix2-2026-08-26, ux-design-v2 §3.4): GET /api/health
// reports whether this collector held data before the light-default upgrade —
// the SECOND gate on the one-time theme-flip notice (the first, "no stored
// theme choice", only the browser can answer). A fresh install must answer
// false and keep answering false once traffic starts, because the answer is a
// statement about a moment in the past and is frozen the first time it is asked.
func TestHeldPriorDataProbe(t *testing.T) {
	// Fresh install: empty store, nothing connected → false, and it STAYS false.
	// (r.start seeds a call + finding, so serve the routes directly instead.)
	r := newRig(t)
	r.ui = httptest.NewServer(r.ext.routes())
	t.Cleanup(r.ui.Close)
	_, out, raw := r.do(t, http.MethodGet, "/api/health", nil)
	if out["held_prior_data"] != false {
		t.Fatalf("fresh install must not report prior data: %s", raw)
	}
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: "c1", CapturedAt: "2026-08-26T10:00:00Z",
		Integration: "acme-tools", Direction: "client", Method: "GET", Route: "/x",
		Redaction: model.Redaction{Patterns: []string{}}})
	if _, out, raw = r.do(t, http.MethodGet, "/api/health", nil); out["held_prior_data"] != false {
		t.Errorf("the probe must be frozen, not re-evaluated per request: %s", raw)
	}

	// Upgraded install: the store already held data the first time it is asked.
	r2 := newRig(t)
	_ = r2.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: "c1", CapturedAt: "2026-08-24T10:00:00Z",
		Integration: "acme-tools", Direction: "client", Method: "GET", Route: "/x",
		Redaction: model.Redaction{Patterns: []string{}}})
	r2.ui = httptest.NewServer(r2.ext.routes())
	t.Cleanup(r2.ui.Close)
	if _, out, raw = r2.do(t, http.MethodGet, "/api/health", nil); out["held_prior_data"] != true {
		t.Errorf("a collector that already held calls must report prior data: %s", raw)
	}
}

// brokenStore is a store whose every READ fails with the error a stopped
// postgres actually produces — the pgx connect error, DSN and all. The reads a
// test drives are the ones the read API makes; everything else falls through to
// the embedded fake.
type brokenStore struct {
	*fakeStore
	err error
}

// storeDSNError is the verbatim shape of a pgx failure against a stopped
// database: user, database and host, in prose. It is what the read routes used
// to hand to the browser.
const storeDSNError = "failed to connect to `user=flanj database=flanj`: " +
	"[::1]:5432 (localhost): dial error: dial tcp [::1]:5432: connect: connection refused, " +
	"lookup postgres on 127.0.0.11:53: no such host"

func newBrokenStore() *brokenStore {
	return &brokenStore{fakeStore: newFakeStore(), err: errors.New(storeDSNError)}
}

func (b *brokenStore) Stats() (int, int64, error) { return 0, 0, b.err }
func (b *brokenStore) ListEdges(bool) ([]model.Edge, error) {
	return nil, b.err
}
func (b *brokenStore) EdgeCallCountsSince(string) (map[string]int, error) { return nil, b.err }
func (b *brokenStore) ListCalls(int) ([]model.RedactedCall, error)        { return nil, b.err }
func (b *brokenStore) ListFindings(int) ([]model.Finding, error)          { return nil, b.err }
func (b *brokenStore) ListSpecInfos() ([]model.SpecInfo, error)           { return nil, b.err }
func (b *brokenStore) GetSpecDoc(string) ([]byte, string, bool, error)    { return nil, "", false, b.err }
func (b *brokenStore) CallPeerHosts([]string) (map[string]string, error)  { return nil, b.err }
func (b *brokenStore) GetSetting(string) (string, bool, error)            { return "", false, b.err }

// TestReadRoutesNeverLeakTheStoreError is the regression for the postgres-lane
// walk (2026-09-02): with the database stopped, every read route answered
// `{"error": "failed to connect to user=flanj database=flanj … lookup postgres
// …"}` — the connection string, in prose, to an unauthenticated localhost GET,
// at 500, while every mutating route answered the deck's one sentence at the
// same moment. Two envelopes and two statuses for one condition.
//
// Every read route now answers the SAME 503 {error, message} the rest of the
// relay speaks, and the raw error goes to the log instead.
func TestReadRoutesNeverLeakTheStoreError(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	ext := &uiExtension{
		cfg:       &Config{UIEndpoint: "127.0.0.1:0", IntegrationID: "acme-payments"},
		telemetry: component.TelemetrySettings{Logger: zap.New(core)},
		st:        newBrokenStore(),
	}
	ui := httptest.NewServer(ext.routes())
	t.Cleanup(ui.Close)

	for _, path := range []string{
		"/api/health",
		"/api/edges",
		"/api/calls",
		"/api/findings",
		"/api/contracts",
		"/api/contracts/spec?integration=acme-payments",
	} {
		resp, err := http.Get(ui.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status = %d, want 503 (the store is a dependency that is down)", path, resp.StatusCode)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("GET %s: body is not JSON: %v (%s)", path, err, raw)
		}
		if body["error"] != "store_error" {
			t.Errorf("GET %s: error = %v, want the stable code store_error", path, body["error"])
		}
		if body["message"] != msgStoreUnavailable {
			t.Errorf("GET %s: message = %v, want the deck's sentence %q", path, body["message"], msgStoreUnavailable)
		}
		// The leak itself: never the DSN, in any fragment, on any route.
		for _, secret := range []string{"user=", "database=", "postgres", "5432", storeDSNError} {
			if bytes.Contains(bytes.ToLower(raw), []byte(strings.ToLower(secret))) {
				t.Errorf("GET %s leaked %q to the browser: %s", path, secret, raw)
			}
		}
	}

	// The operator still gets the real cause — in the log, where it belongs.
	var logged bool
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "user=flanj") {
			logged = true
		}
	}
	if !logged {
		t.Error("the raw store error must reach the log — it is diagnosis, not a browser payload")
	}
}

// TestStoreOrErrorSpeaksTheSameEnvelope covers the OTHER half of the condition:
// no store extension resolved at all. It answered a third shape —
// `{"error": "store extension not available"}`, no message — so a UI switching
// on the code saw two different stories about one outage.
func TestStoreOrErrorSpeaksTheSameEnvelope(t *testing.T) {
	ext := &uiExtension{
		cfg:       &Config{UIEndpoint: "127.0.0.1:0"},
		telemetry: component.TelemetrySettings{Logger: zap.NewNop()},
	}
	ui := httptest.NewServer(ext.routes())
	t.Cleanup(ui.Close)

	resp, err := http.Get(ui.URL + "/api/findings")
	if err != nil {
		t.Fatalf("GET /api/findings: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, raw)
	}
	if body["error"] != "store_error" || body["message"] != msgStoreUnavailable {
		t.Errorf("unresolved store answered %v, want the same {store_error, %q} a failed store call answers", body, msgStoreUnavailable)
	}
}

// TestFindingsCarryTheirSourceCallHost is the regression for the Contracts-tab
// split (postgres lane, 2026-09-02): one provider rendered TWICE — the uploaded
// contract with a green CONFORMING pill, and directly beneath it a second card
// for the same host saying "No contract for this provider" while carrying the
// BREAKING finding.
//
// The SPA joins a finding to its contract card by HOST (the ids never match: an
// uploaded contract's integration is derived from the host, a finding's comes
// from the call), and it used to resolve that host by looking source_call_id up
// in GET /api/calls — the 200 newest rows. source_call_id is frozen at the
// FIRST occurrence, so the join broke the moment the evidence call fell off
// that page, which ordinary traffic does in minutes.
//
// The store still HAS the call (a finding pins it), so the host is resolved
// here and shipped on the row.
func TestFindingsCarryTheirSourceCallHost(t *testing.T) {
	r := newRig(t)
	r.start(t)

	// The evidence call — pinned by its finding, and deliberately NOT among the
	// rows /api/calls would return in the live stack (there it has aged out of
	// the newest 200; here the point is that the finding row no longer needs it).
	callID := "call_evidence"
	if err := r.st.InsertCall(model.RedactedCall{
		SchemaVersion: 1, ID: callID, CapturedAt: "2026-08-23T09:00:00Z", Integration: "acme-payments",
		Direction: "client", PeerHost: "api.acme.test", Method: "POST", URL: "https://api.acme.test/v1/charges",
		Route: "/v1/charges", StatusCode: 200, RequestBody: "{}", ResponseBody: `{"amount":"10"}`,
		Redaction: model.Redaction{Patterns: []string{}},
	}); err != nil {
		t.Fatalf("seed call: %v", err)
	}
	if err := r.st.InsertFinding(model.Finding{
		SchemaVersion: 1, ID: "fnd_host", Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
		Integration: "acme-payments", Endpoint: "POST /v1/charges", Expected: "integer", Actual: "string",
		Rule: "type", SourceCallID: &callID, DetectedAt: "2026-08-23T09:00:01Z",
	}); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	// A CALL-LESS finding (an MCP definition_change): no source call, so no
	// host — the SPA falls back to integration for these, and a host invented
	// here would be a lie about which provider the finding is against.
	if err := r.st.InsertFinding(model.Finding{
		SchemaVersion: 1, ID: "fnd_callless", Kind: model.KindDefinitionChange, Severity: model.SeverityInfo,
		Integration: "acme-tools", Endpoint: "charge", Rule: "description-changed", DetectedAt: "2026-08-23T09:00:02Z",
	}); err != nil {
		t.Fatalf("seed call-less finding: %v", err)
	}

	_, _, raw := r.do(t, http.MethodGet, "/api/findings", nil)
	var out struct {
		Findings []struct {
			ID       string `json:"id"`
			PeerHost string `json:"peer_host"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode findings: %v (%s)", err, raw)
	}
	hosts := map[string]string{}
	for _, f := range out.Findings {
		hosts[f.ID] = f.PeerHost
	}
	if got := hosts["fnd_host"]; got != "api.acme.test" {
		t.Errorf("finding peer_host = %q, want api.acme.test — without it the finding detaches from its contract card", got)
	}
	if got, ok := hosts["fnd_callless"]; ok && got != "" {
		t.Errorf("a call-less finding must carry no host, got %q", got)
	}
	// The seeded rig finding references a call stored WITHOUT a peer host: an
	// absent host must stay absent rather than become "".
	if got := hosts["fnd_1"]; got != "" {
		t.Errorf("a call with no peer host must not invent one, got %q", got)
	}
}

// TestConnectRelaysTheConfirmationMailOutcome pins the middle of the honesty
// seam (CONTRACTS-CP §5.1). The CP answers 200/201 whether or not the mail left
// the box, so a 2xx alone can never justify "Check your inbox" — the collector
// must relay the CP's own verdict, unchanged, and say nothing when there is
// none. A regression here is silent: the panel keeps rendering, just lying.
func TestConnectRelaysTheConfirmationMailOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mail       string
		retryAfter int
		wantMail   any // nil = the key must be ABSENT
		wantRetry  any
	}{
		{name: "sent", mail: "sent", wantMail: "sent"},
		{name: "failed", mail: "failed", wantMail: "failed"},
		{name: "cooldown", mail: "cooldown", retryAfter: 360, wantMail: "cooldown", wantRetry: float64(360)},
		{name: "no outcome reported", mail: "", wantMail: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.start(t)
			r.cp.confirmationMail = tc.mail
			r.cp.confirmationMailRetryAfter = tc.retryAfter

			resp, out, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{
				"consumer_display_name": "Acme Consumer Ltd",
				"contact_email":         "ops@acme.test",
			})
			if resp.StatusCode != 202 {
				t.Fatalf("connect: %d %s", resp.StatusCode, raw)
			}
			got, present := out["confirmation_mail"]
			if tc.wantMail == nil {
				if present {
					t.Fatalf("no outcome reported, but the view carries confirmation_mail=%v — an absent key is the only honest answer", got)
				}
			} else if got != tc.wantMail {
				t.Fatalf("confirmation_mail = %v (present=%v); want %v", got, present, tc.wantMail)
			}
			gotRetry, retryPresent := out["confirmation_mail_retry_after_s"]
			if tc.wantRetry == nil {
				if retryPresent {
					t.Errorf("confirmation_mail_retry_after_s = %v; it rides only on cooldown", gotRetry)
				}
			} else if gotRetry != tc.wantRetry {
				t.Errorf("confirmation_mail_retry_after_s = %v; want %v", gotRetry, tc.wantRetry)
			}

			// The outcome describes ONE request. It is never persisted, and a
			// later GET (which attempts no send) must not replay it.
			for k := range r.st.settings {
				if strings.Contains(k, "confirmation_mail") {
					t.Errorf("the mail outcome was persisted as %q — it is transient", k)
				}
			}
			_, after, _ := r.do(t, http.MethodGet, "/api/connect", nil)
			if _, present := after["confirmation_mail"]; present {
				t.Errorf("GET /api/connect must report no mail outcome, got %v", after["confirmation_mail"])
			}
		})
	}
}
