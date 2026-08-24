// Package contract is the transport-neutral contract model (v0.5 spec §4 Step A).
//
// A Contract is a set of Operations whose request/response constraints are
// JSON Schema. Transports plug in as LOADERS that normalize their native
// definition format into this one model:
//
//   - MCP tools/list (FromToolsList, in this package): each tool becomes one
//     Operation; inputSchema/outputSchema are already JSON Schema and pass
//     through canonicalization only. (The event-driven loader —
//     contract_snapshot → Contract — arrives with Step C; the normalization
//     seam is this function.)
//   - OpenAPI (subpackage contract/openapi: LoadFile, LoadData, From): each
//     path+method operation becomes one Operation; parameters + requestBody
//     normalize into inputSchema, the 2xx JSON response into outputSchema.
//     The OpenAPI machinery lives in that subpackage ON PURPOSE: this
//     package's public surface carries no kin-openapi types, so an MCP-only
//     importer (mcp-drift-watch) links none of it.
//
// Everything downstream of a loader (the definition-diff classifier in
// contract/diff, and later the findings pipeline) is transport-agnostic.
//
// This package is PUBLIC (not under internal/) by design: mcp-drift-watch
// imports it so the definition-diff classifier has exactly one implementation
// (v0.5 spec §5 "Classifier placement").
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Source identifies the definition format a Contract was loaded from.
type Source string

const (
	SourceOpenAPI Source = "openapi"
	SourceMCP     Source = "mcp"
)

// Kind identifies the transport an Operation is called over.
type Kind string

const (
	KindHTTP    Kind = "http"
	KindMCPTool Kind = "mcp_tool"
)

// Schema is a JSON Schema fragment in canonical decoded form: the result of
// json.Unmarshal into map[string]any (numbers are float64, arrays []any),
// with set-semantics keyword arrays ("required", string-array "type") sorted.
// Two schemas are the same constraint surface iff reflect.DeepEqual.
type Schema = map[string]any

// Version identifies one observed revision of a Contract.
type Version struct {
	// ContentHash is the sha256 (hex) of the canonical JSON of Operations —
	// two Contracts with the same hash declare the same surface.
	ContentHash string `json:"contentHash"`
	// ObservedAt is when this revision was seen (RFC 3339; loader-supplied).
	ObservedAt string `json:"observedAt"`
	// Provenance says where the definition came from:
	// "local file <path>" | "observed tools/list at <ts>" | later: "CP push".
	Provenance string `json:"provenance"`
}

// Match is how a live call is matched to an Operation. Exactly one transport's
// fields are set, per Kind.
type Match struct {
	// HTTP: method (upper-case) + OpenAPI-style path template.
	Method       string `json:"method,omitempty"`
	PathTemplate string `json:"pathTemplate,omitempty"`
	// MCP: the tool name.
	ToolName string `json:"toolName,omitempty"`
}

// Operation is one callable unit of a Contract with JSON Schema constraints.
type Operation struct {
	// ID is the operation's identity within the contract:
	// http: "<METHOD> <pathTemplate>" ; mcp: tool.name.
	// The drift signature (edge, operation.id, rule, fieldPath) hangs off it.
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Match       Match  `json:"match"`
	Description string `json:"description,omitempty"`
	// InputSchema constrains the request: http = params + requestBody,
	// mcp = tool.inputSchema. nil = none declared.
	InputSchema Schema `json:"inputSchema,omitempty"`
	// OutputSchema constrains the result: http = the 2xx JSON response,
	// mcp = tool.outputSchema. nil = none declared (an honest, surfaced limit —
	// never a synthesized schema).
	OutputSchema Schema `json:"outputSchema,omitempty"`
	// Annotations carries transport hints (mcp: readOnlyHint etc.).
	// Stored verbatim; never consulted for findings.
	Annotations map[string]any `json:"annotations,omitempty"`
}

// Contract is a transport-neutral set of Operations.
type Contract struct {
	Source Source `json:"source"`
	// EdgeRef is the existing edge identity this contract is bound to, opaque
	// to this package (collector: peer_host+direction; drift-watch: server name).
	EdgeRef    string      `json:"edgeRef"`
	Version    Version     `json:"version"`
	Operations []Operation `json:"operations"` // sorted by ID
}

// Op returns the operation with the given id, or nil.
func (c *Contract) Op(id string) *Operation {
	for i := range c.Operations {
		if c.Operations[i].ID == id {
			return &c.Operations[i]
		}
	}
	return nil
}

// ComputeContentHash hashes the canonical JSON of the operations set.
// encoding/json marshals map keys sorted, and loaders sort Operations by ID,
// so the hash is deterministic for a given declared surface.
func ComputeContentHash(ops []Operation) (string, error) {
	b, err := json.Marshal(ops)
	if err != nil {
		return "", fmt.Errorf("hash operations: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Finish sorts the operations by ID, stamps the version's content hash and
// returns the contract — the mandatory last step of every loader. Exported so
// loaders outside this package (contract/openapi) finish contracts
// identically to the in-package tools/list loader.
func Finish(c *Contract) (*Contract, error) {
	sort.Slice(c.Operations, func(i, j int) bool { return c.Operations[i].ID < c.Operations[j].ID })
	h, err := ComputeContentHash(c.Operations)
	if err != nil {
		return nil, err
	}
	c.Version.ContentHash = h
	return c, nil
}

// CanonicalizeSchema round-trips v through JSON and normalizes set-semantics
// keyword arrays, so schemas from ANY loader compare with reflect.DeepEqual.
// v may be a json.RawMessage/[]byte (decoded) or any JSON-marshalable value.
func CanonicalizeSchema(v any) (Schema, error) {
	if v == nil {
		return nil, nil
	}
	var raw []byte
	switch t := v.(type) {
	case json.RawMessage:
		raw = t
	case []byte:
		raw = t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("canonicalize schema: %w", err)
		}
		raw = b
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("canonicalize schema: %w", err)
	}
	if out == nil {
		return nil, nil
	}
	canonicalizeNode(out)
	return out, nil
}

// canonicalizeNode sorts the keyword arrays whose order is set-semantics, at
// every nesting level: "required" (always a set) and "type" when it is a
// string array (a type union). Enum order is left as declared.
func canonicalizeNode(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if (k == "required" || k == "type") && isStringArray(child) {
				arr := child.([]any)
				sort.Slice(arr, func(i, j int) bool { return arr[i].(string) < arr[j].(string) })
			}
			canonicalizeNode(child)
		}
	case []any:
		for _, child := range t {
			canonicalizeNode(child)
		}
	}
}

func isStringArray(v any) bool {
	arr, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range arr {
		if _, ok := e.(string); !ok {
			return false
		}
	}
	return true
}
