package flanjstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/extension"

	"github.com/flanj-io/collector/internal/model"
)

// testSpecToken: the endpoint refuses to bind without one, so every test that
// drives it presents this. The unauthenticated cases are asserted explicitly in
// TestSpecEndpointRequiresItsToken and TestSpecAuthFailsClosedOnAnEmptyToken.
const testSpecToken = "tok_test_01"

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
	e, base := startStorePod(t, testSpecToken)
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, []byte("openapi: 3.0.3\n"))

	code, body := get(t, base+"/internal/contracts", testSpecToken)
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

	code, doc := get(t, base+"/internal/contracts/doc?integration=acme", testSpecToken)
	if code != http.StatusOK {
		t.Fatalf("doc status = %d, want 200", code)
	}
	if string(doc) != "openapi: 3.0.3\n" {
		t.Errorf("doc = %q, want the stored document verbatim", doc)
	}
}

// TestSpecEndpointExposesOnlyProviderContracts: the channel carries what a
// front needs to validate its dependencies' traffic and nothing else — a
// PROVIDER's contract bound to an edge, whether an uploaded OpenAPI document
// or an observed MCP tools/list snapshot. The self contract is the store pod's
// own config and an unbound row cannot be keyed by host; neither crosses.
func TestSpecEndpointExposesOnlyProviderContracts(t *testing.T) {
	e, base := startStorePod(t, testSpecToken)
	doc := []byte("openapi: 3.0.3\n")
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP, []byte(`{"tools":[]}`))
	seedContract(t, e, "self", "api.self.test", model.SpecRoleSelf, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "unbound", "", model.SpecRoleProvider, model.SpecFormatOpenAPI, doc)

	_, body := get(t, base+"/internal/contracts", testSpecToken)
	var listed struct {
		Contracts []model.SpecInfo `json:"contracts"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range listed.Contracts {
		got[c.Integration] = c.Format
	}
	if len(got) != 2 || got["acme"] != model.SpecFormatOpenAPI || got["acme-tools"] != model.SpecFormatMCP {
		t.Fatalf("contracts = %+v, want exactly the two bound provider contracts (openapi + mcp)", listed.Contracts)
	}
}

// TestSpecEndpointServesMCPSnapshots: the MCP half of the channel. Until
// 2026-09-07 an observed tools/list was withheld here as "self-delivering from
// traffic the front already sees" — true for the ONE front that saw it, and
// the reason every other front's baseline was its own memory: a rename
// observed through front-a raised nothing when the stale client called
// through front-b, and a restarted front forgot the baseline. The store holds
// the org-wide baseline; a front reads it back through this route, metadata
// first and the raw snapshot document verbatim.
func TestSpecEndpointServesMCPSnapshots(t *testing.T) {
	e, base := startStorePod(t, testSpecToken)
	snapshot := []byte(`{"tools":[{"name":"get_balance","inputSchema":{"type":"object"}}],"serverInfo":{"name":"acme-tools-mcp","version":"1.2.0"}}`)
	if err := e.Store().PutSpecInfo(model.SpecInfo{
		Integration: "acme-tools",
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatMCP,
		PeerHost:    "mcp.acme.test",
		EdgeClass:   "external",
		Title:       "acme-tools-mcp",
		Version:     "1.2.0",
		Endpoints:   1,
		Source:      model.SpecSourceObserved,
		LoadedAt:    "2026-09-07T10:00:00.000Z",
	}, snapshot); err != nil {
		t.Fatal(err)
	}

	_, body := get(t, base+"/internal/contracts", testSpecToken)
	var listed struct {
		Contracts []model.SpecInfo `json:"contracts"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Contracts) != 1 {
		t.Fatalf("contracts = %+v, want the one MCP snapshot", listed.Contracts)
	}
	row := listed.Contracts[0]
	if row.Format != model.SpecFormatMCP || row.PeerHost != "mcp.acme.test" || row.LoadedAt != "2026-09-07T10:00:00.000Z" || row.EdgeClass != "external" {
		t.Errorf("row = %+v: the front seeds by peer_host and refreshes on loaded_at, and the UI tells stdio twins apart by edge_class", row)
	}

	code, doc := get(t, base+"/internal/contracts/doc?integration=acme-tools", testSpecToken)
	if code != http.StatusOK {
		t.Fatalf("doc status = %d, want 200", code)
	}
	if string(doc) != string(snapshot) {
		t.Errorf("doc = %q, want the stored snapshot verbatim — the front hashes it for versioning", doc)
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
	_, base := startStorePod(t, testSpecToken)
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
	_, base := startStorePod(t, testSpecToken)
	for _, path := range []string{
		"/api/calls", "/api/findings", "/api/edges", "/api/health",
		"/internal/calls", "/internal/findings", "/internal/settings", "/",
	} {
		if code, _ := get(t, base+path, testSpecToken); code == http.StatusOK {
			t.Errorf("%s returned 200 — the contract endpoint exposes more than contracts", path)
		}
	}
}

// TestSpecEndpointUnknownContract: a contract removed between a front's list
// and its fetch is a 404, which the front reads as "gone" and drops, rather
// than an error it would retry forever.
func TestSpecEndpointUnknownContract(t *testing.T) {
	_, base := startStorePod(t, testSpecToken)
	if code, _ := get(t, base+"/internal/contracts/doc?integration=nope", testSpecToken); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	if code, _ := get(t, base+"/internal/contracts/doc", testSpecToken); code != http.StatusBadRequest {
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

// TestSpecEndpointRefusesToBindWithoutAToken: the endpoint is the ONE
// deliberate exception to "outbound-only, loopback UI" (Non-negotiable 5), so
// it must fail CLOSED.
//
// BUG (cross-repo review, 2026-09-01): authSpec ran the bearer check only when
// SpecToken != "", and Validate enforced the reverse direction only. The
// shipped config/config.store.example.yaml pairs `spec_endpoint: 0.0.0.0:5337`
// with `spec_token: ${env:FLANJ_SPEC_TOKEN}` — and an undefined env var expands
// to the empty string — so deploying the store-pod config as shipped, without
// exporting that variable, bound a listener on the cluster interface that
// answered every contract request unauthenticated. The default failure mode of
// the documented config was an open door.
func TestSpecEndpointRefusesToBindWithoutAToken(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = "/data/flanj.db"
	cfg.SpecEndpoint = "0.0.0.0:5337"
	cfg.SpecToken = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("spec_endpoint with an empty spec_token validated clean — an unset " +
			"FLANJ_SPEC_TOKEN would bind an unauthenticated listener on the cluster interface")
	}
}

// TestSpecAuthFailsClosedOnAnEmptyToken: belt to the config braces above. Even
// if an empty token reached the runtime, the guard must refuse rather than wave
// callers through.
func TestSpecAuthFailsClosedOnAnEmptyToken(t *testing.T) {
	e := &storeExtension{cfg: &Config{SpecToken: ""}}
	var reached bool
	h := e.authSpec(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/contracts", nil))
	if reached {
		t.Error("an empty configured token let an unauthenticated request through")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}

	// And a caller cannot satisfy it by sending an empty bearer either.
	reached = false
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/contracts", nil)
	req.Header.Set("Authorization", "Bearer ")
	h.ServeHTTP(rec, req)
	if reached {
		t.Error("`Bearer ` matched an empty configured token")
	}
}

// TestSpecDocServesOnlyWhatTheListAdmits: the doc route must apply the SAME
// filter as the list route.
//
// BUG (cross-repo review, 2026-09-01): handleSpecList filtered to
// `Role == provider && PeerHost != ""` with the explicit rationale that the
// self contract has "no business crossing this hop", while handleSpecDoc
// passed the caller-supplied integration straight to GetSpecDoc — a bare
// lookup over the same table. So a token-holding front could fetch the
// ORGANISATION'S OWN OpenAPI document with
// `GET /internal/contracts/doc?integration=self`, which the list route was
// written specifically to withhold. A filter on the index and none on the
// item is not a filter. (MCP snapshots were withheld too at the time; they
// are served since 2026-09-07 — see TestSpecEndpointServesMCPSnapshots — and
// the one rule still governs both routes.)
func TestSpecDocServesOnlyWhatTheListAdmits(t *testing.T) {
	const token = "s3cret"
	e, base := startStorePod(t, token)
	doc := []byte("openapi: 3.0.3\ninfo:\n  title: our own API\n")
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "self", "api.self.test", model.SpecRoleSelf, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "unbound", "", model.SpecRoleProvider, model.SpecFormatOpenAPI, doc)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP, []byte(`{"tools":[]}`))

	// What the list admits is fetchable, in either format.
	for _, integration := range []string{"acme", "acme-tools"} {
		if code, _ := get(t, base+"/internal/contracts/doc?integration="+integration, token); code != http.StatusOK {
			t.Errorf("%s doc: status %d, want 200", integration, code)
		}
	}
	// Everything the list withholds must be unfetchable by id.
	for _, integration := range []string{"self", "unbound"} {
		code, body := get(t, base+"/internal/contracts/doc?integration="+integration, token)
		if code == http.StatusOK {
			t.Errorf("GET doc?integration=%s returned 200 — the list route deliberately "+
				"withholds this contract, so the doc route must too (body %d bytes)", integration, len(body))
		}
	}
}
