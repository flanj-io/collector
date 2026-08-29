// Package contract_test is deliberately an EXTERNAL test package: it imports
// BOTH contract (the model + the MCP tools/list loader) and contract/openapi
// (the OpenAPI loader, split out so package contract's public surface carries
// no kin-openapi types), proving normalization parity across the split.
package contract_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flanj-io/collector/contract"
	"github.com/flanj-io/collector/contract/openapi"
)

func fixturesDir() string { return filepath.Join("..", "contracts") }

// loadFixtureContracts loads the Step A normalization pair: the SAME logical
// contract via the OpenAPI fixture and via the MCP tools/list fixture.
func loadFixtureContracts(t *testing.T) (openapiC, mcpC *contract.Contract) {
	t.Helper()
	doc, err := openapi.LoadFile(filepath.Join(fixturesDir(), "contract-normalization-openapi.yaml"))
	if err != nil {
		t.Fatalf("load openapi fixture: %v", err)
	}
	oc, err := openapi.From(doc, "api.provider.test|outbound", "2026-08-24T00:00:00Z", "local file contracts/contract-normalization-openapi.yaml")
	if err != nil {
		t.Fatalf("normalize openapi fixture: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(fixturesDir(), "contract-normalization-tools-list.json"))
	if err != nil {
		t.Fatalf("read tools/list fixture: %v", err)
	}
	tools, err := contract.ParseToolsList(b)
	if err != nil {
		t.Fatalf("parse tools/list fixture: %v", err)
	}
	mc, err := contract.FromToolsList(tools, "mcp.provider.test|outbound", "2026-08-24T00:00:00Z", "observed tools/list at 2026-08-24T00:00:00Z")
	if err != nil {
		t.Fatalf("normalize tools/list fixture: %v", err)
	}
	return oc, mc
}

// TestNormalization_OpenAPIvsMCP is the Step A acceptance battery (spec §4.A
// accept (1)): the same JSON Schema arriving via an OpenAPI fixture and via an
// MCP tools/list fixture must normalize to deep-equal Operations — same
// description, deep-equal inputSchema, deep-equal outputSchema. The transport
// identity (id, kind, match) differs by definition of the transports and is
// asserted explicitly instead.
func TestNormalization_OpenAPIvsMCP(t *testing.T) {
	oc, mc := loadFixtureContracts(t)

	pairs := []struct{ httpID, toolName string }{
		{"GET /accounts/{account_id}/balance", "get_balance"},
		{"POST /refunds", "create_refund"},
	}
	if len(oc.Operations) != len(pairs) || len(mc.Operations) != len(pairs) {
		t.Fatalf("operation counts: openapi=%d mcp=%d want %d each", len(oc.Operations), len(mc.Operations), len(pairs))
	}

	for _, p := range pairs {
		ho := oc.Op(p.httpID)
		mo := mc.Op(p.toolName)
		if ho == nil || mo == nil {
			t.Fatalf("pair %q/%q: missing operation (http=%v mcp=%v)", p.httpID, p.toolName, ho != nil, mo != nil)
		}
		if ho.Description != mo.Description {
			t.Errorf("%s: description mismatch:\n  http: %q\n  mcp:  %q", p.toolName, ho.Description, mo.Description)
		}
		if !reflect.DeepEqual(ho.InputSchema, mo.InputSchema) {
			t.Errorf("%s: inputSchema not deep-equal:\n  http: %#v\n  mcp:  %#v", p.toolName, ho.InputSchema, mo.InputSchema)
		}
		if !reflect.DeepEqual(ho.OutputSchema, mo.OutputSchema) {
			t.Errorf("%s: outputSchema not deep-equal:\n  http: %#v\n  mcp:  %#v", p.toolName, ho.OutputSchema, mo.OutputSchema)
		}
		// Transport identity is per-kind by design.
		if ho.Kind != contract.KindHTTP || mo.Kind != contract.KindMCPTool {
			t.Errorf("%s: kinds: http=%q mcp=%q", p.toolName, ho.Kind, mo.Kind)
		}
		if mo.ID != mo.Match.ToolName || mo.ID != p.toolName {
			t.Errorf("mcp id/match: id=%q toolName=%q", mo.ID, mo.Match.ToolName)
		}
		if ho.ID != ho.Match.Method+" "+ho.Match.PathTemplate {
			t.Errorf("http id/match: id=%q method=%q path=%q", ho.ID, ho.Match.Method, ho.Match.PathTemplate)
		}
	}

	if oc.Source != contract.SourceOpenAPI || mc.Source != contract.SourceMCP {
		t.Errorf("sources: openapi=%q mcp=%q", oc.Source, mc.Source)
	}
}

// TestContentHash_DeterministicAndSurfaceSensitive: same tools/list twice →
// same hash; any operation change → different hash.
func TestContentHash_DeterministicAndSurfaceSensitive(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixturesDir(), "contract-normalization-tools-list.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tools, err := contract.ParseToolsList(b)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	c1, err := contract.FromToolsList(tools, "edge", "t1", "p1")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	c2, err := contract.FromToolsList(tools, "edge", "t2", "p2")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c1.Version.ContentHash == "" || c1.Version.ContentHash != c2.Version.ContentHash {
		t.Errorf("hash not stable across observations: %q vs %q", c1.Version.ContentHash, c2.Version.ContentHash)
	}

	changed := append([]contract.ToolDef(nil), tools...)
	changed[0].Description += " (reworded)"
	c3, err := contract.FromToolsList(changed, "edge", "t3", "p3")
	if err != nil {
		t.Fatalf("normalize changed: %v", err)
	}
	if c3.Version.ContentHash == c1.Version.ContentHash {
		t.Error("hash did not change with the declared surface")
	}
}

// TestCanonicalizeSchema_SetKeywords: required and type-union order never
// affects equality; enum order (semantic for prose/UI) is preserved.
func TestCanonicalizeSchema_SetKeywords(t *testing.T) {
	a, err := contract.CanonicalizeSchema([]byte(`{"type":["string","integer"],"required":["b","a"],"enum":["x","y"]}`))
	if err != nil {
		t.Fatalf("canonicalize a: %v", err)
	}
	b, err := contract.CanonicalizeSchema([]byte(`{"type":["integer","string"],"required":["a","b"],"enum":["x","y"]}`))
	if err != nil {
		t.Fatalf("canonicalize b: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("set-keyword order leaked into equality:\n  a=%#v\n  b=%#v", a, b)
	}
	if got := a["enum"].([]any); got[0] != "x" || got[1] != "y" {
		t.Errorf("enum order was not preserved: %#v", got)
	}
}
