package promote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPostPeekLink proves the copy-link relay hits the CP thread endpoint with the deploy-token
// auth and the channel payload (channel rides the request body / token record — never the URL).
func TestPostPeekLink(t *testing.T) {
	detail := true
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(PeekLinkResponse{
			PeekURL:    "http://cp.test/t/pub123#k=tok456",
			MagicToken: "tok456",
			Channel:    "slack",
			ExpiresAt:  "2026-09-04T00:00:00.000Z",
			Revoked:    1,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "deploy-token", "v0-test")
	resp, status, err := c.PostPeekLink(context.Background(), "thread-1", PeekLinkRequest{
		Channel:            "slack",
		RevokeExisting:     true,
		CardEndpointDetail: &detail,
	})
	if err != nil {
		t.Fatalf("PostPeekLink: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if gotPath != "/api/v1/threads/thread-1/peek-links" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer deploy-token" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["channel"] != "slack" || gotBody["revoke_existing"] != true || gotBody["card_endpoint_detail"] != true {
		t.Errorf("body = %v", gotBody)
	}
	if resp.PeekURL != "http://cp.test/t/pub123#k=tok456" || resp.Revoked != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

// TestRevokePeekLinks proves the revoke relay and its revoked-count passthrough.
func TestRevokePeekLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/threads/thread-9/peek-links/revoke" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"revoked":3}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "deploy-token", "v0-test")
	revoked, status, err := c.RevokePeekLinks(context.Background(), "thread-9")
	if err != nil {
		t.Fatalf("RevokePeekLinks: %v", err)
	}
	if status != http.StatusOK || revoked != 3 {
		t.Errorf("revoked=%d status=%d", revoked, status)
	}
}
