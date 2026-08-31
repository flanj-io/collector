package flanjdrift

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// fakeStorePod mimics the store extension's contract endpoint
// (extension/flanjstore/specserver.go) so the front's half of the channel is
// testable on its own. The two ends are pinned to the same shape by the paths
// and the payload key asserted here and there.
func fakeStorePod(t *testing.T, token string, docs map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(r *http.Request) bool {
		return token == "" || r.Header.Get("Authorization") == "Bearer "+token
	}
	mux.HandleFunc("GET /internal/contracts", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		infos := make([]model.SpecInfo, 0, len(docs))
		for integration := range docs {
			infos = append(infos, model.SpecInfo{
				Integration: integration,
				Role:        model.SpecRoleProvider,
				Format:      model.SpecFormatOpenAPI,
				PeerHost:    "api." + integration + ".test",
				LoadedAt:    "2026-08-31T10:00:00Z",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"contracts": infos})
	})
	mux.HandleFunc("GET /internal/contracts/doc", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		doc, ok := docs[r.URL.Query().Get("integration")]
		if !ok {
			http.Error(w, "no such contract", http.StatusNotFound)
			return
		}
		_, _ = w.Write(doc)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRemoteSourceFillsTheCache closes the tiered loop: a contract uploaded on
// the store pod reaches a front that has no store of its own, and the front can
// then validate that host's traffic.
func TestRemoteSourceFillsTheCache(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": doc})

	c := newSpecCache()
	if _, errs := c.refresh(newRemoteSpecSource(srv.URL, "")); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Fatal("a contract uploaded on the store pod did not reach the front")
	}
}

// TestRemoteSourcePresentsItsToken: the endpoint is on the cluster interface,
// so the front must authenticate or get nothing.
func TestRemoteSourcePresentsItsToken(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "s3cret", map[string][]byte{"acme": doc})

	if _, errs := newSpecCache().refresh(newRemoteSpecSource(srv.URL, "")); len(errs) == 0 {
		t.Error("an unauthenticated front was served contracts")
	}

	c := newSpecCache()
	if _, errs := c.refresh(newRemoteSpecSource(srv.URL, "s3cret")); len(errs) != 0 {
		t.Fatalf("authenticated refresh failed: %v", errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("authenticated front got no contract")
	}
}

// TestRemoteSourceHidesTheStorePodResponse: an error must carry the status and
// nothing else. The request that produced it carried the shared token, and a
// misconfigured endpoint could be any server at all — neither belongs in a log
// line.
func TestRemoteSourceHidesTheStorePodResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "s3cret leaked in a body", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := newRemoteSpecSource(srv.URL, "s3cret").listSpecs()
	if err == nil {
		t.Fatal("a 500 was not an error")
	}
	if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "leaked") {
		t.Errorf("error echoes the peer's body: %v", err)
	}
}

// TestRemoteSourceMissingDocIsNotAnError: a contract removed between a front's
// list and its fetch is gone, not broken. Treating it as an error would retry
// it forever.
func TestRemoteSourceMissingDocIsNotAnError(t *testing.T) {
	srv := fakeStorePod(t, "", map[string][]byte{})
	raw, err := newRemoteSpecSource(srv.URL, "").specDoc("vanished")
	if err != nil {
		t.Errorf("404 became an error: %v", err)
	}
	if raw != nil {
		t.Errorf("raw = %q, want nil", raw)
	}
}

// TestRemoteSourceNormalisesTheEndpoint: an operator writing a bare host:port
// (the shape they just wrote for `otlphttp`) or a trailing slash gets a working
// front, not a silent one.
func TestRemoteSourceNormalisesTheEndpoint(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": doc})
	bare := strings.TrimPrefix(srv.URL, "http://")

	for _, endpoint := range []string{srv.URL, srv.URL + "/", bare, bare + "/"} {
		c := newSpecCache()
		if _, errs := c.refresh(newRemoteSpecSource(endpoint, "")); len(errs) != 0 {
			t.Errorf("endpoint %q: %v", endpoint, errs)
			continue
		}
		if _, ok := c.lookup("api.acme.test"); !ok {
			t.Errorf("endpoint %q resolved no contract", endpoint)
		}
	}
}

// TestRemoteSourceUnreachableKeepsServing: a store pod restart must not blind
// every front in the deployment. The refresh fails and the cache keeps what it
// has.
func TestRemoteSourceUnreachableKeepsServing(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": doc})

	src := newRemoteSpecSource(srv.URL, "")
	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}

	srv.Close() // the store pod goes away mid-deployment
	if _, errs := c.refresh(src); len(errs) == 0 {
		t.Error("an unreachable store pod refreshed clean")
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("a store pod restart dropped a working contract — detection must ride it out")
	}
}
