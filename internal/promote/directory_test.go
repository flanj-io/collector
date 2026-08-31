package promote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetDirectory proves the full-table pull: GET /api/v1/directory with the
// collector key, the conditional If-None-Match round (304 → no body), and the
// ETag echo on 200. The CP answers with the §5.14 ENVELOPE
// `{"entries": …, "count": n}` — and GetDirectory passes the body through
// UNTOUCHED, byte-for-byte (the collector unwraps at read time, never here).
func TestGetDirectory(t *testing.T) {
	const envelope = `{"entries":{"stripe.com":{"name":"Stripe","tier":"curated"}},"count":1}`
	var gotINM, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/directory" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		gotINM = r.Header.Get("If-None-Match")
		gotAuth = r.Header.Get("Authorization")
		if gotINM == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "deploy_tok", "v").WithCollectorKey("ckey_secret")

	body, etag, status, err := c.GetDirectory(context.Background(), "")
	if err != nil || status != 200 {
		t.Fatalf("first pull: %v (%d)", err, status)
	}
	if gotAuth != "Bearer ckey_secret" {
		t.Errorf("pull must use the collector key, got %q", gotAuth)
	}
	if gotINM != "" {
		t.Errorf("first pull sent If-None-Match %q", gotINM)
	}
	if etag != `"v1"` {
		t.Errorf("etag = %q", etag)
	}
	if string(body) != envelope {
		t.Errorf("body = %s, want the envelope passed through untouched", body)
	}
	var env struct {
		Entries map[string]map[string]string `json:"entries"`
		Count   int                          `json:"count"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Entries["stripe.com"]["name"] != "Stripe" || env.Count != 1 {
		t.Errorf("envelope decode = %+v (%v)", env, err)
	}

	body, etag, status, err = c.GetDirectory(context.Background(), `"v1"`)
	if err != nil || status != 304 || body != nil || etag != `"v1"` {
		t.Errorf("conditional pull: body=%q etag=%q status=%d err=%v, want a 304 no-op", body, etag, status, err)
	}
}

// TestGetDirectoryError maps a non-2xx/304 answer to a CPError with the code
// decoded — and the bearer never reaches the error string.
func TestGetDirectoryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","message":"bad key"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "deploy_tok", "v").WithCollectorKey("ckey_secret")
	_, _, status, err := c.GetDirectory(context.Background(), "")
	ce := AsCPError(err)
	if status != 401 || ce == nil || ce.Code != "unauthorized" {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if s := ce.Error(); s == "" || strings.Contains(s, "ckey_secret") || strings.Contains(s, "deploy_tok") {
		t.Errorf("bearer leaked into the error: %q", s)
	}
}

// TestSubmitDirectoryName proves the opt-in submission: POST
// /api/v1/directory/submissions with the collector key and exactly
// {domain, name}.
func TestSubmitDirectoryName(t *testing.T) {
	cp := newStubCP(t, http.StatusAccepted, `{"status":"pending"}`)
	c := NewClient(cp.srv.URL, "deploy_tok", "v").WithCollectorKey("ckey_secret")
	status, err := c.SubmitDirectoryName(context.Background(), DirectorySubmissionRequest{Domain: "stripe.com", Name: "Stripe"})
	if err != nil || status != 202 {
		t.Fatalf("submit: %v (%d)", err, status)
	}
	if cp.method != "POST" || cp.path != "/api/v1/directory/submissions" {
		t.Errorf("%s %s", cp.method, cp.path)
	}
	if cp.auth != "Bearer ckey_secret" {
		t.Errorf("submission must use the collector key, got %q", cp.auth)
	}
	if cp.body["domain"] != "stripe.com" || cp.body["name"] != "Stripe" || len(cp.body) != 2 {
		t.Errorf("body = %v, want exactly {domain, name}", cp.body)
	}
}
