package promote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReplaceLink proves Replace thread link = POST peek-links with
// revoke_existing:true, Bearer collector key, and that thread_url is read (with
// the deprecated peek_url as fallback for an older CP).
func TestReplaceLink(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"thread_url":"http://cp.test/t/pub123#k=tok456","peek_url":"http://cp.test/t/pub123#k=tok456","magic_token":"tok456","expires_at":"2026-09-04T00:00:00.000Z","revoked":1}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "deploy-token", "v0-test").WithCollectorKey("ckey")
	resp, status, err := c.ReplaceLink(context.Background(), "thread-1")
	if err != nil {
		t.Fatalf("ReplaceLink: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if gotPath != "/api/v1/threads/thread-1/peek-links" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ckey" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["revoke_existing"] != true {
		t.Errorf("body = %v, want revoke_existing:true", gotBody)
	}
	if _, has := gotBody["channel"]; has {
		t.Errorf("channel must not be sent any more: %v", gotBody)
	}
	if resp.ThreadURL != "http://cp.test/t/pub123#k=tok456" || resp.Revoked != 1 {
		t.Errorf("resp = %+v", resp)
	}

	// Older CP: only peek_url → ThreadURL falls back.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"peek_url":"http://cp.test/t/p#k=old","magic_token":"old","expires_at":"x","revoked":0}`))
	}))
	defer srv2.Close()
	resp2, _, err := NewClient(srv2.URL, "d", "v").ReplaceLink(context.Background(), "t")
	if err != nil || resp2.ThreadURL != "http://cp.test/t/p#k=old" {
		t.Errorf("fallback: resp=%+v err=%v", resp2, err)
	}
}

// TestRevokeLinks proves the revoke relay and its revoked-count passthrough.
func TestRevokeLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/threads/thread-9/peek-links/revoke" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"revoked":3}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "deploy-token", "v0-test")
	revoked, status, err := c.RevokeLinks(context.Background(), "thread-9")
	if err != nil {
		t.Fatalf("RevokeLinks: %v", err)
	}
	if status != http.StatusOK || revoked != 3 {
		t.Errorf("revoked=%d status=%d", revoked, status)
	}
}
