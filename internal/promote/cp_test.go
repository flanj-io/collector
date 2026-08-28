package promote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubCP records one request and answers with the given status + body.
type stubCP struct {
	method, path, query, auth, ctype string
	body                             map[string]any
	srv                              *httptest.Server
}

func newStubCP(t *testing.T, status int, reply string) *stubCP {
	t.Helper()
	s := &stubCP{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method, s.path, s.query = r.Method, r.URL.Path, r.URL.RawQuery
		s.auth = r.Header.Get("Authorization")
		s.ctype = r.Header.Get("Content-Type")
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&s.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// TestRegister proves Connect: POST register with the DEPLOY token (never the
// key), the §5 body, and the once-returned collector key decoded.
func TestRegister(t *testing.T) {
	cp := newStubCP(t, http.StatusCreated, `{"collector_id":"c1","collector_public_id":"pub_c1","collector_key":"ckey_secret","contact_status":"pending"}`)
	c := NewClient(cp.srv.URL, "deploy_tok", "v").WithCollectorKey("old_key_must_not_be_used")
	resp, status, err := c.Register(context.Background(), RegisterRequest{
		ConsumerDisplayName: "Acme Consumer Ltd", ContactEmail: "ops@acme.test", ContactDisplayName: "Dana", LocalUIURL: "http://localhost:5335",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if status != 201 || resp.CollectorKey != "ckey_secret" || resp.CollectorPublicID != "pub_c1" || resp.ContactStatus != "pending" {
		t.Errorf("resp=%+v status=%d", resp, status)
	}
	if cp.method != "POST" || cp.path != "/api/v1/collectors/register" {
		t.Errorf("%s %s", cp.method, cp.path)
	}
	if cp.auth != "Bearer deploy_tok" {
		t.Errorf("register must use the deploy token, got %q", cp.auth)
	}
	if cp.body["consumer_display_name"] != "Acme Consumer Ltd" || cp.body["contact_email"] != "ops@acme.test" ||
		cp.body["contact_display_name"] != "Dana" || cp.body["local_ui_url"] != "http://localhost:5335" {
		t.Errorf("body=%v", cp.body)
	}

	// Idempotent replay: 200 is accepted too.
	cp2 := newStubCP(t, http.StatusOK, `{"collector_id":"c1","collector_public_id":"pub_c1","contact_status":"confirmed"}`)
	resp2, status2, err := NewClient(cp2.srv.URL, "deploy_tok", "v").Register(context.Background(), RegisterRequest{ConsumerDisplayName: "A", ContactEmail: "a@b.c"})
	if err != nil || status2 != 200 || resp2.ContactStatus != "confirmed" || resp2.CollectorKey != "" {
		t.Errorf("replay: resp=%+v status=%d err=%v", resp2, status2, err)
	}
}

// TestRegisterWithKey proves the re-register path (CONTRACTS-CP §5.1): once a
// collector key exists, resend / change-of-contact POST register with Bearer
// <collector key> — never the deploy token — and keep the key.
func TestRegisterWithKey(t *testing.T) {
	cp := newStubCP(t, http.StatusOK, `{"collector_id":"c1","collector_public_id":"pub_c1","contact_status":"pending"}`)
	c := NewClient(cp.srv.URL, "deploy_tok", "v").WithCollectorKey("ckey_secret")
	resp, status, err := c.RegisterWithKey(context.Background(), RegisterRequest{ConsumerDisplayName: "Acme Consumer Ltd", ContactEmail: "new@acme.test", ContactDisplayName: "Dana"})
	if err != nil || status != 200 {
		t.Fatalf("RegisterWithKey: %v (%d)", err, status)
	}
	if cp.method != "POST" || cp.path != "/api/v1/collectors/register" {
		t.Errorf("%s %s", cp.method, cp.path)
	}
	if cp.auth != "Bearer ckey_secret" {
		t.Errorf("re-register must use the collector key, got %q", cp.auth)
	}
	if cp.body["contact_email"] != "new@acme.test" || cp.body["consumer_display_name"] != "Acme Consumer Ltd" || cp.body["contact_display_name"] != "Dana" {
		t.Errorf("body=%v", cp.body)
	}
	if resp.CollectorKey != "" || resp.ContactStatus != "pending" || resp.CollectorPublicID != "pub_c1" {
		t.Errorf("resp=%+v", resp)
	}
	// Without a key the call is refused locally — nothing reaches the CP.
	cp2 := newStubCP(t, http.StatusOK, `{}`)
	if _, _, err := NewClient(cp2.srv.URL, "deploy_tok", "v").RegisterWithKey(context.Background(), RegisterRequest{ContactEmail: "a@b.c"}); err == nil || cp2.method != "" {
		t.Errorf("RegisterWithKey without a key must fail locally: err=%v method=%q", err, cp2.method)
	}
}

// TestMe proves GET me with the collector key, including the
// confirmed_contact_email tri-state (value / null / absent on an older CP).
func TestMe(t *testing.T) {
	cp := newStubCP(t, http.StatusOK, `{"collector_id":"c1","collector_public_id":"pub","consumer_display_name":"Acme","contact_email":"new@acme.test","contact_display_name":"Dana","contact_status":"pending","confirmed_contact_email":"ops@acme.test","registered_at":"2026-08-23T10:00:00Z","confirmed_at":"2026-08-23T10:05:00Z"}`)
	c := NewClient(cp.srv.URL, "deploy_tok", "v").WithCollectorKey("ckey")
	me, status, err := c.Me(context.Background())
	if err != nil || status != 200 {
		t.Fatalf("Me: %v (%d)", err, status)
	}
	if cp.method != "GET" || cp.path != "/api/v1/collectors/me" || cp.auth != "Bearer ckey" {
		t.Errorf("%s %s auth=%q", cp.method, cp.path, cp.auth)
	}
	if me.ContactStatus != "pending" || me.ContactEmail != "new@acme.test" || me.ConfirmedAt == "" || me.ContactDisplayName != "Dana" {
		t.Errorf("me=%+v", me)
	}
	if me.ConfirmedContactEmail == nil || *me.ConfirmedContactEmail != "ops@acme.test" {
		t.Errorf("confirmed_contact_email not decoded: %+v", me.ConfirmedContactEmail)
	}
	// null before the first confirmation
	cpNull := newStubCP(t, http.StatusOK, `{"contact_status":"pending","confirmed_contact_email":null}`)
	me2, _, err := NewClient(cpNull.srv.URL, "d", "v").WithCollectorKey("ckey").Me(context.Background())
	if err != nil || me2.ConfirmedContactEmail != nil {
		t.Errorf("null confirmed_contact_email: %+v err=%v", me2.ConfirmedContactEmail, err)
	}
}

// TestThreadMutations covers close / reopen / handoff / summary paths, methods,
// auth and decoding.
func TestThreadMutations(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		cp := newStubCP(t, 200, `{"state":"closed","closed_at":"2026-08-23T11:00:00Z","reopened_at":null}`)
		out, _, err := NewClient(cp.srv.URL, "d", "v").WithCollectorKey("k").Close(context.Background(), "t1")
		if err != nil || out.State != "closed" || out.ClosedAt == nil || out.ReopenedAt != nil {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		if cp.method != "POST" || cp.path != "/api/v1/threads/t1/close" || cp.auth != "Bearer k" {
			t.Errorf("%s %s %s", cp.method, cp.path, cp.auth)
		}
	})
	t.Run("reopen", func(t *testing.T) {
		cp := newStubCP(t, 200, `{"state":"open","closed_at":"2026-08-23T11:00:00Z","reopened_at":"2026-08-23T12:00:00Z"}`)
		out, _, err := NewClient(cp.srv.URL, "d", "v").WithCollectorKey("k").Reopen(context.Background(), "t1")
		if err != nil || out.State != "open" || out.ReopenedAt == nil {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		if cp.path != "/api/v1/threads/t1/reopen" {
			t.Errorf("path %s", cp.path)
		}
	})
	t.Run("handoff", func(t *testing.T) {
		cp := newStubCP(t, 201, `{"owner_url":"https://cp.test/o/pub#o=handoff123","expires_at":"2026-08-23T11:10:00Z"}`)
		out, status, err := NewClient(cp.srv.URL, "d", "v").WithCollectorKey("k").Handoff(context.Background(), "t1")
		if err != nil || status != 201 || out.OwnerURL != "https://cp.test/o/pub#o=handoff123" {
			t.Fatalf("out=%+v status=%d err=%v", out, status, err)
		}
		if cp.method != "POST" || cp.path != "/api/v1/threads/t1/handoff" {
			t.Errorf("%s %s", cp.method, cp.path)
		}
	})
	t.Run("summary", func(t *testing.T) {
		cp := newStubCP(t, 200, `{"id":"t1","thread_public_id":"pub","state":"open","closed_at":null,"reopened_at":null,"turn":"fix_reported","provider_display_name":"Acme Payments","endpoint":"POST /v1/charges","evidence_count":1,"opened_count":3,"knock_count":0,"message_count":2,"last_reply_at":"2026-08-23T11:00:00Z","fixed_claim":{"display_name":"Dana (Acme)","at":"2026-08-23T11:00:00Z"},"link":{"status":"active","expires_at":"2026-09-22T00:00:00Z"},"archived":false}`)
		out, _, err := NewClient(cp.srv.URL, "d", "v").WithCollectorKey("k").Summary(context.Background(), "t1")
		if err != nil {
			t.Fatalf("Summary: %v", err)
		}
		if cp.method != "GET" || cp.path != "/api/v1/threads/t1/summary" || cp.auth != "Bearer k" {
			t.Errorf("%s %s %s", cp.method, cp.path, cp.auth)
		}
		if out.Turn != "fix_reported" || out.OpenedCount != 3 || out.FixedClaim == nil || out.FixedClaim.DisplayName != "Dana (Acme)" ||
			out.Link == nil || out.Link.Status != "active" || out.Endpoint != "POST /v1/charges" {
			t.Errorf("summary=%+v", out)
		}
	})
}

// TestListThreads is CONTRACTS-CP §5.5a: GET /api/v1/threads?limit=<n>, Bearer
// collector key, an ENVELOPE (never a bare array), rows that are byte-for-byte
// the §5.5 summary object, and never a token.
func TestListThreads(t *testing.T) {
	const body = `{"threads":[` +
		`{"id":"t2","thread_public_id":"pub2","state":"closed","closed_at":"2026-08-24T09:00:00Z","reopened_at":null,"turn":"replied_while_closed",` +
		`"consumer_display_name":"Acme Consumer Ltd","provider_display_name":"Globex","endpoint":"POST /v1/refunds","evidence_count":2,` +
		`"opened_count":5,"knock_count":1,"message_count":4,"last_reply_at":"2026-08-24T08:00:00Z","fixed_claim":null,` +
		`"link":{"status":"expired","expires_at":"2026-08-24T00:00:00Z"},"archived":true,"created_at":"2026-08-20T10:00:00Z","updated_at":"2026-08-24T09:00:00Z"},` +
		`{"id":"t1","thread_public_id":"pub1","state":"open","closed_at":null,"reopened_at":null,"turn":"waiting_on_provider",` +
		`"consumer_display_name":"Acme Consumer Ltd","provider_display_name":"Acme Payments","endpoint":"POST /v1/charges","evidence_count":1,` +
		`"opened_count":3,"knock_count":0,"message_count":2,"last_reply_at":null,"fixed_claim":{"display_name":"Dana (Acme)","at":"x"},` +
		`"link":{"status":"active","expires_at":"2026-09-22T00:00:00Z"},"archived":false,"created_at":"2026-08-23T10:00:00Z","updated_at":"2026-08-23T11:00:00Z"}` +
		`],"count":2,"total":7,"limit":2,"has_more":true}`
	cp := newStubCP(t, 200, body)
	out, status, err := NewClient(cp.srv.URL, "deploy_secret", "v").WithCollectorKey("ckey_secret").ListThreads(context.Background(), 2)
	if err != nil || status != 200 {
		t.Fatalf("ListThreads: %v (%d)", err, status)
	}
	if cp.method != "GET" || cp.path != "/api/v1/threads" || cp.query != "limit=2" {
		t.Errorf("%s %s?%s", cp.method, cp.path, cp.query)
	}
	if cp.auth != "Bearer ckey_secret" {
		t.Errorf("the list is collector-key authorized, got %q", cp.auth)
	}
	if out.Count != 2 || out.Total != 7 || out.Limit != 2 || !out.HasMore || len(out.Threads) != 2 {
		t.Fatalf("envelope=%+v", out)
	}
	// Order is the CP's — most-recently-active first, decoded as sent.
	if out.Threads[0].ID != "t2" || out.Threads[1].ID != "t1" {
		t.Errorf("order not preserved: %s, %s", out.Threads[0].ID, out.Threads[1].ID)
	}
	// An archived row is INCLUDED and flagged, never dropped.
	if !out.Threads[0].Archived || out.Threads[0].Turn != "replied_while_closed" || out.Threads[0].Link == nil || out.Threads[0].Link.Status != "expired" {
		t.Errorf("archived row=%+v", out.Threads[0])
	}
	r1 := out.Threads[1]
	if r1.ConsumerDisplayName != "Acme Consumer Ltd" || r1.ProviderDisplayName != "Acme Payments" || r1.Endpoint != "POST /v1/charges" ||
		r1.EvidenceCount != 1 || r1.OpenedCount != 3 || r1.MessageCount != 2 || r1.LastReplyAt != nil ||
		r1.FixedClaim == nil || r1.FixedClaim.DisplayName != "Dana (Acme)" || r1.CreatedAt != "2026-08-23T10:00:00Z" || r1.UpdatedAt != "2026-08-23T11:00:00Z" {
		t.Errorf("row=%+v", r1)
	}

	// limit <= 0 omits the parameter entirely (the CP applies its default 50).
	cp2 := newStubCP(t, 200, `{"threads":[],"count":0,"total":0,"limit":50,"has_more":false}`)
	if _, _, err := NewClient(cp2.srv.URL, "d", "v").WithCollectorKey("k").ListThreads(context.Background(), 0); err != nil {
		t.Fatalf("ListThreads(0): %v", err)
	}
	if cp2.query != "" {
		t.Errorf("limit<=0 must send no query, got %q", cp2.query)
	}
	if ListThreadsMaxLimit != 200 {
		t.Errorf("§5.5a hard cap is 200, got %d", ListThreadsMaxLimit)
	}

	// A CP error is typed, and the key never reaches the error string.
	cp3 := newStubCP(t, 400, `{"error":"invalid_limit","message":"limit must be 1..200"}`)
	_, _, err = NewClient(cp3.srv.URL, "deploy_secret", "v").WithCollectorKey("ckey_secret").ListThreads(context.Background(), 201)
	ce := AsCPError(err)
	if ce == nil || ce.Status != 400 || ce.Code != "invalid_limit" {
		t.Fatalf("400 not typed: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("bearer leaked into error: %v", err)
	}
}

// TestCPError proves 412/403 bodies become typed errors carrying the CP's
// error code (for the relay to pass through) and never the bearer.
func TestCPError(t *testing.T) {
	cp := newStubCP(t, http.StatusPreconditionFailed, `{"error":"contact_unconfirmed","message":"Confirm your contact first."}`)
	_, status, err := NewClient(cp.srv.URL, "deploy_secret", "v").WithCollectorKey("key_secret").Post(context.Background(), FlagRequest{})
	if status != 412 {
		t.Fatalf("status=%d", status)
	}
	ce := AsCPError(err)
	if ce == nil || ce.Status != 412 || ce.Code != "contact_unconfirmed" || ce.Message != "Confirm your contact first." {
		t.Fatalf("err=%v ce=%+v", err, ce)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("bearer leaked into error: %v", err)
	}

	cp403 := newStubCP(t, http.StatusForbidden, `{"error":"wrong_origin","message":"This key did not create the thread."}`)
	_, _, err = NewClient(cp403.srv.URL, "d", "v").WithCollectorKey("k").Close(context.Background(), "t9")
	if ce := AsCPError(err); ce == nil || ce.Code != "wrong_origin" || ce.Status != 403 {
		t.Errorf("403 not typed: %v", err)
	}

	// Non-JSON body: still a CPError with the status, empty code.
	cp500 := newStubCP(t, 500, `boom`)
	_, _, err = NewClient(cp500.srv.URL, "d", "v").Summary(context.Background(), "t")
	if ce := AsCPError(err); ce == nil || ce.Status != 500 || ce.Code != "" {
		t.Errorf("500 not typed: %v", err)
	}

	// Transport failure: not a CPError.
	c := NewClient("http://127.0.0.1:1", "d", "v")
	_, _, err = c.Me(context.Background())
	if err == nil || AsCPError(err) != nil {
		t.Errorf("transport error should not be a CPError: %v", err)
	}
}
