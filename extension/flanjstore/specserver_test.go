package flanjstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/extension"

	"github.com/flanj-io/collector/internal/model"
)

// startStorePod boots a store extension with the contract endpoint bound to an
// ephemeral port, and returns its base URL.
func startStorePod(t *testing.T, token string) (*storeExtension, string) {
	t.Helper()
	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = filepath.Join(t.TempDir(), "store.db")
	cfg.SpecEndpoint = "127.0.0.1:0"
	cfg.SpecToken = token

	ext, err := create(context.Background(), extension.Settings{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := ext.(*storeExtension)
	if err := e.Start(context.Background(), nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	return e, "http://" + e.specLn.Addr().String()
}

func get(t *testing.T, url, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func seedContract(t *testing.T, e *storeExtension, integration, host, role, format string, doc []byte) {
	t.Helper()
	err := e.Store().PutSpecInfo(model.SpecInfo{
		Integration: integration,
		Role:        role,
		Format:      format,
		PeerHost:    host,
		LoadedAt:    "2026-08-31T10:00:00Z",
	}, doc)
	if err != nil {
		t.Fatalf("seed %s: %v", integration, err)
	}
}

// TestSpecEndpointServesProviderContracts: a front asks, and gets back the
// metadata for the uploaded provider contracts plus their documents. This is
// the whole tiered channel.
func TestSpecEndpointServesProviderContracts(t *testing.T) {
	e, base := startStorePod(t, "")
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, []byte("openapi: 3.0.3\n"))

	code, body := get(t, base+"/internal/contracts", "")
	if code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", code)
	}
	var listed struct {
		Contracts []model.SpecInfo `json:"contracts"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if len(listed.Contracts) != 1 || listed.Contracts[0].PeerHost != "api.acme.test" {
		t.Fatalf("contracts = %+v, want the one bound to api.acme.test", listed.Contracts)
	}
	if listed.Contracts[0].LoadedAt == "" {
		t.Error("loaded_at missing — it is the change token the front refreshes on")
	}

	code, doc := get(t, base+"/internal/contracts/doc?integration=acme", "")
	if code != http.StatusOK {
		t.Fatalf("doc status = %d, want 200", code)
	}
	if string(doc) != "openapi: 3.0.3\n" {
		t.Errorf("doc = %q, want the stored document verbatim", doc)
	}
}

// TestSpecEndpointExposesOnlyProviderContracts: the channel carries what a
// front needs to validate its dependencies' traffic and nothing else. The self
// contract is the store pod's own config, MCP snapshots are self-delivering
// from traffic the front already sees, and an unbound row cannot be keyed by
// host — none of them may cross the hop.
func TestSpecEndpointExposesOnlyProviderContracts(t *testing.T) {
	e, base := startStorePod(t, "")
	doc := []byte("openapi: 3.0.3\n")
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "self", "api.self.test", model.SpecRoleSelf, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "unbound", "", model.SpecRoleProvider, model.SpecFormatOpenAPI, doc)

	_, body := get(t, base+"/internal/contracts", "")
	var listed struct {
		Contracts []model.SpecInfo `json:"contracts"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Contracts) != 1 {
		t.Fatalf("contracts = %+v, want only the bound provider contract", listed.Contracts)
	}
	if listed.Contracts[0].Integration != "acme" {
		t.Errorf("served %q, want acme", listed.Contracts[0].Integration)
	}
}

// TestSpecEndpointRequiresItsToken: the endpoint is on the cluster interface,
// not loopback, so the token is the thing standing between it and every other
// pod in the namespace.
func TestSpecEndpointRequiresItsToken(t *testing.T) {
	e, base := startStorePod(t, "s3cret")
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, []byte("openapi: 3.0.3\n"))

	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "wrong", http.StatusUnauthorized},
		{"right token", "s3cret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := get(t, base+"/internal/contracts", tc.token); code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
			if code, _ := get(t, base+"/internal/contracts/doc?integration=acme", tc.token); code != tc.want {
				t.Errorf("doc status = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestSpecEndpointIsReadOnly: nothing on this listener mutates. A write verb
// finds no route rather than a handler.
func TestSpecEndpointIsReadOnly(t *testing.T) {
	_, base := startStorePod(t, "")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, base+"/internal/contracts", nil)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s /internal/contracts returned 200 — the contract endpoint must not mutate", method)
		}
	}
}

// TestSpecEndpointSurfacesNothingElse: the store holds calls, findings, edges
// and the settings KV (which carries the collector key). None of it has a route
// here — the surface is two contract reads, full stop.
func TestSpecEndpointSurfacesNothingElse(t *testing.T) {
	_, base := startStorePod(t, "")
	for _, path := range []string{
		"/api/calls", "/api/findings", "/api/edges", "/api/health",
		"/internal/calls", "/internal/findings", "/internal/settings", "/",
	} {
		if code, _ := get(t, base+path, ""); code == http.StatusOK {
			t.Errorf("%s returned 200 — the contract endpoint exposes more than contracts", path)
		}
	}
}

// TestSpecEndpointUnknownContract: a contract removed between a front's list
// and its fetch is a 404, which the front reads as "gone" and drops, rather
// than an error it would retry forever.
func TestSpecEndpointUnknownContract(t *testing.T) {
	_, base := startStorePod(t, "")
	if code, _ := get(t, base+"/internal/contracts/doc?integration=nope", ""); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	if code, _ := get(t, base+"/internal/contracts/doc", ""); code != http.StatusBadRequest {
		t.Errorf("missing integration status = %d, want 400", code)
	}
}

// TestSpecEndpointOffByDefault: every single-pod and shared-postgres
// deployment reads its co-located store directly and must open no port at all.
func TestSpecEndpointOffByDefault(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = filepath.Join(t.TempDir(), "store.db")
	ext, err := create(context.Background(), extension.Settings{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := ext.(*storeExtension)
	if err := e.Start(context.Background(), nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })

	if e.specLn != nil || e.specSrv != nil {
		t.Error("the contract endpoint bound a port with no spec_endpoint configured")
	}
}

// TestSpecTokenNeedsAnEndpoint: a token that guards nothing is a
// misconfiguration an operator would never see at runtime, so it fails config
// validation instead.
func TestSpecTokenNeedsAnEndpoint(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = "/data/flanj.db"
	cfg.SpecToken = "s3cret"
	if err := cfg.Validate(); err == nil {
		t.Error("spec_token with no spec_endpoint validated clean")
	}
}
