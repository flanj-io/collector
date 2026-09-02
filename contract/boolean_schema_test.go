package contract

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A boolean JSON Schema is legal at any schema position, and became reachable at
// the ROOT when MCP revision 2026-07-28 dropped outputSchema's root-type
// restriction. Schema is map-shaped, so booleans map to their exact object
// equivalents rather than erroring — because erroring here failed the WHOLE
// contract, and the collector's snapshot loader turned that into a dropped
// tools/list and an edge frozen at its last contract forever.
func TestCanonicalizeSchemaAcceptsBooleanSchemas(t *testing.T) {
	got, err := CanonicalizeSchema(json.RawMessage(`true`))
	if err != nil {
		t.Fatalf("true: %v", err)
	}
	if !reflect.DeepEqual(got, Schema{}) {
		t.Fatalf("true must canonicalize to the empty (accept-anything) schema, got %v", got)
	}

	got, err = CanonicalizeSchema(json.RawMessage(`false`))
	if err != nil {
		t.Fatalf("false: %v", err)
	}
	if !reflect.DeepEqual(got, Schema{"not": map[string]any{}}) {
		t.Fatalf("false must canonicalize to the accept-nothing schema, got %v", got)
	}
}

// The root-type restriction is gone: a scalar- or array-rooted schema object is
// a legitimate outputSchema now.
func TestCanonicalizeSchemaAcceptsNonObjectRootTypes(t *testing.T) {
	for _, raw := range []string{
		`{"type":"string"}`,
		`{"type":"number"}`,
		`{"type":"array","items":{"type":"number"}}`,
		`{"type":["null","string"]}`,
	} {
		if _, err := CanonicalizeSchema(json.RawMessage(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

// What is left over is genuinely not a schema in any draft, and still errors —
// the caller (the collector's loader) degrades that one tool rather than the
// catalog.
func TestCanonicalizeSchemaRejectsNonSchemaValues(t *testing.T) {
	for _, raw := range []string{`[1,2,3]`, `"a string"`, `42`} {
		if _, err := CanonicalizeSchema(json.RawMessage(raw)); err == nil {
			t.Fatalf("%s: want an error, got none", raw)
		}
	}
}

// FromToolsList is what mcp-drift-watch pins; a boolean schema must not fail it.
func TestFromToolsListAcceptsABooleanOutputSchema(t *testing.T) {
	c, err := FromToolsList([]ToolDef{
		{Name: "anything_goes", OutputSchema: json.RawMessage(`true`)},
		{Name: "nothing_goes", OutputSchema: json.RawMessage(`false`)},
	}, "edge", "2026-09-02T10:00:00Z", "test")
	if err != nil {
		t.Fatalf("FromToolsList: %v", err)
	}
	if len(c.Operations) != 2 {
		t.Fatalf("want 2 operations, got %d", len(c.Operations))
	}
	// `true` is a DECLARED schema that accepts anything — distinct from a tool
	// that declared nothing at all, which keeps a nil OutputSchema.
	if c.Op("anything_goes").OutputSchema == nil {
		t.Fatal("a `true` outputSchema is a declaration, not an absence")
	}
}
