package flanjui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestSPAFallbackServesFilesAnd404sMissingAssets pins the fallback rule: a
// present file is served as itself with its own content type, an
// extension-less unknown path is a client route and gets index.html, and a
// missing FILE-shaped path (/favicon.ico from a browser that ignores
// <link rel="icon">, a stale hashed bundle) is a 404 — never index.html at 200.
func TestSPAFallbackServesFilesAnd404sMissingAssets(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":             {Data: []byte(`<!doctype html><html lang="en" data-flanj-theme="light"><title>Flanj Collector</title></html>`)},
		"favicon.svg":            {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"></svg>`)},
		"assets/index-abc123.js": {Data: []byte(`console.log("ok")`)},
	}
	srv := httptest.NewServer(spaHandlerFS(dist))
	t.Cleanup(srv.Close)

	get := func(p string) (int, string, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(raw)
	}

	// The entrypoint and a client route both serve index.html.
	for _, p := range []string{"/", "/threads", "/settings/appearance"} {
		status, ct, body := get(p)
		if status != http.StatusOK || !strings.Contains(body, "Flanj Collector") || !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: want index.html 200 text/html, got %d %q %q", p, status, ct, body)
		}
	}

	// The icon is served as the SVG it is, with its own content type.
	status, ct, body := get("/favicon.svg")
	if status != http.StatusOK || !strings.HasPrefix(ct, "image/svg+xml") || !strings.HasPrefix(body, "<svg") {
		t.Errorf("GET /favicon.svg: want 200 image/svg+xml <svg…>, got %d %q %q", status, ct, body)
	}
	status, ct, _ = get("/assets/index-abc123.js")
	if status != http.StatusOK || !strings.Contains(ct, "javascript") {
		t.Errorf("GET /assets/index-abc123.js: want 200 javascript, got %d %q", status, ct)
	}

	// A file-shaped path that does not exist is a 404, not the SPA at 200.
	for _, p := range []string{"/favicon.ico", "/assets/index-stale.js", "/apple-touch-icon.png"} {
		status, _, body := get(p)
		if status != http.StatusNotFound {
			t.Errorf("GET %s: want 404, got %d (body starts %q)", p, status, truncate(body, 40))
		}
		if strings.Contains(body, "Flanj Collector") {
			t.Errorf("GET %s: answered with index.html", p)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
