package contract

import (
	"encoding/json"
	"fmt"
)

// ToolDef mirrors one entry of an MCP `tools/list` result, with the MCP wire
// field names (spec §1: name, description, inputSchema, optional outputSchema,
// annotations). Schemas stay raw JSON here — the server's own words — and are
// canonicalized only when normalized into an Operation.
//
// Two callers share this seam: mcp-drift-watch hands it already-decoded tool
// lists (stored snapshots, fixtures), and the collector's Step C event loader
// (internal/drift MCPDetector.LoadSnapshot) decodes `contract_snapshot`
// records through ParseToolsList into the same []ToolDef — the normalization
// below is final either way.
type ToolDef struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

// ParseToolsList decodes a tools/list JSON payload: either the full result
// object {"tools": [...]} or a bare tool array [...].
func ParseToolsList(b []byte) ([]ToolDef, error) {
	var wrapped struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(b, &wrapped); err == nil && wrapped.Tools != nil {
		return wrapped.Tools, nil
	}
	var bare []ToolDef
	if err := json.Unmarshal(b, &bare); err != nil {
		return nil, fmt.Errorf("parse tools/list: %w", err)
	}
	return bare, nil
}

// FromToolsList normalizes an MCP tool list into a Contract: one Operation per
// tool, id = tool.name, schemas canonicalized (never re-inferred or
// synthesized — a tool without outputSchema keeps a nil OutputSchema, the
// honest "no output contract declared" state).
func FromToolsList(tools []ToolDef, edgeRef, observedAt, provenance string) (*Contract, error) {
	c := &Contract{
		Source:  SourceMCP,
		EdgeRef: edgeRef,
		Version: Version{ObservedAt: observedAt, Provenance: provenance},
	}
	for _, t := range tools {
		if t.Name == "" {
			return nil, fmt.Errorf("tools/list entry without a name")
		}
		in, err := CanonicalizeSchema(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s inputSchema: %w", t.Name, err)
		}
		out, err := CanonicalizeSchema(t.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s outputSchema: %w", t.Name, err)
		}
		var ann map[string]any
		if len(t.Annotations) > 0 {
			if err := json.Unmarshal(t.Annotations, &ann); err != nil {
				return nil, fmt.Errorf("tool %s annotations: %w", t.Name, err)
			}
		}
		c.Operations = append(c.Operations, Operation{
			ID:           t.Name,
			Kind:         KindMCPTool,
			Match:        Match{ToolName: t.Name},
			Description:  t.Description,
			InputSchema:  in,
			OutputSchema: out,
			Annotations:  ann,
		})
	}
	return Finish(c)
}
