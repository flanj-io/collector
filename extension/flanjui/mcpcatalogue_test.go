package flanjui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/integration"
	"github.com/flanj-io/collector/internal/model"
)

// The 2026-09-19 split, at the UI API: a host with BOTH an uploaded REST
// contract and an observed MCP server is two rows, never one, whatever order
// they arrive in. Before it, an MCP snapshot for api.acme.test and a REST
// contract for api.acme.test shared one store row: a snapshot after the upload
// replaced the contract, and an upload after the snapshot was refused 409.

const mcpToolsDoc = `{"tools":[{"name":"charge","inputSchema":{"type":"object"}}]}`

func seedCatalogue(t *testing.T, r *testRig, host string) {
	t.Helper()
	if err := r.st.PutMCPCatalogue(model.SpecInfo{
		Integration: integration.ForHost(host), Role: model.SpecRoleProvider, PeerHost: host,
		Format: model.SpecFormatMCP, Source: model.SpecSourceObserved, EdgeClass: model.EdgeClassExternal,
		Title: "acme-tools-mcp", LoadedAt: "2026-09-19T10:00:00Z",
	}, []byte(mcpToolsDoc)); err != nil {
		t.Fatal(err)
	}
}

// assertBothCards: /api/contracts lists one openapi row and one mcp row for the
// host (the UI renders a card per row), and /api/contracts/spec hands back each
// row's own document by format.
func assertBothCards(t *testing.T, r *testRig, host, wantOpenAPI string) {
	t.Helper()
	integration := integration.ForHost(host)
	resp, out, raw := r.do(t, http.MethodGet, "/api/contracts", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/contracts = %d: %s", resp.StatusCode, raw)
	}
	rows, _ := out["contracts"].([]any)
	formats := map[string]int{}
	for _, row := range rows {
		m, _ := row.(map[string]any)
		if m["integration"] == integration && m["peer_host"] == host {
			f, _ := m["format"].(string)
			formats[f]++
		}
	}
	if formats[model.SpecFormatOpenAPI] != 1 || formats[model.SpecFormatMCP] != 1 {
		t.Fatalf("/api/contracts rows for %s = %v, want one openapi and one mcp: %s", host, formats, raw)
	}
	spec := func(q string) (int, string) {
		resp, _, raw := r.do(t, http.MethodGet, "/api/contracts/spec?"+q, nil)
		return resp.StatusCode, string(raw)
	}
	if code, body := spec("integration=" + integration + "&format=openapi"); code != http.StatusOK || body != wantOpenAPI {
		t.Errorf("spec format=openapi = %d, want the uploaded document; got %.60q", code, body)
	}
	if code, body := spec("integration=" + integration + "&format=mcp"); code != http.StatusOK || body != mcpToolsDoc {
		t.Errorf("spec format=mcp = %d %.60q, want the MCP catalogue", code, body)
	}
	// No format (e2e and bookmarks from before the split): the contract.
	if code, body := spec("integration=" + integration); code != http.StatusOK || body != wantOpenAPI {
		t.Errorf("spec without format = %d %.60q, want the REST contract", code, body)
	}
}

// TestUploadBesideAnMCPServerOnTheSameHost is the reverse-order half of the
// bug: the snapshot first, then the upload — refused 409 integration_conflict
// while the two shared a row. Proved red by pointing specInfoFor back at the
// combined listing (the conflict check sees the MCP row again).
func TestUploadBesideAnMCPServerOnTheSameHost(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedCatalogue(t, r, "api.acme.test")
	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test", "document": specV1Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload beside an MCP server = %d: %s", resp.StatusCode, raw)
	}
	assertBothCards(t, r, "api.acme.test", specV1Doc(t))

	// And the forward order: a later snapshot leaves the contract alone.
	seedCatalogue(t, r, "api.acme.test")
	assertBothCards(t, r, "api.acme.test", specV1Doc(t))
}

// TestFetchBesideAnMCPServerOnTheSameHost: the URL bind path had the same
// conflict check and the same 409.
func TestFetchBesideAnMCPServerOnTheSameHost(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newEdgeSpecServer(t, r)
	spec.serve("/openapi.json", specV1Doc(t))
	seedCatalogue(t, r, spec.host())

	resp, out := fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json"), "peer_host": spec.host()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch preview = %d: %v", resp.StatusCode, out)
	}
	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": out["token"]})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch bind beside an MCP server = %d: %s", resp.StatusCode, raw)
	}
	assertBothCards(t, r, spec.host(), specV1Doc(t))
}

// TestContractSpecRefusesAnUnknownFormat: the format is a closed set, and a
// typo answers as one rather than as "not loaded".
func TestContractSpecRefusesAnUnknownFormat(t *testing.T) {
	r := newRig(t)
	r.start(t)
	seedCatalogue(t, r, "api.acme.test")
	resp, out, _ := r.do(t, http.MethodGet, "/api/contracts/spec?integration=api-acme-test&format=MCP", nil)
	if resp.StatusCode != http.StatusBadRequest || out["error"] != "format_invalid" {
		t.Errorf("format=MCP = %d %v, want 400 format_invalid", resp.StatusCode, out)
	}
	// An MCP-only host without a format still answers with its catalogue.
	resp, _, raw := r.do(t, http.MethodGet, "/api/contracts/spec?integration=api-acme-test", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"charge"`) {
		t.Errorf("MCP-only host without format = %d %s, want its catalogue", resp.StatusCode, raw)
	}
}
