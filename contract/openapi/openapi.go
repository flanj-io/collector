// Package openapi is the OpenAPI loader for the transport-neutral contract
// model (v0.5 spec §4 Step A): it validates an OpenAPI document and
// normalizes it into a contract.Contract.
//
// It is a SUBPACKAGE of contract on purpose: every kin-openapi type stays
// here, so package contract's public surface is transport-lean and an
// MCP-only importer (mcp-drift-watch imports only contract + contract/diff)
// links none of the OpenAPI machinery. The collector's drift detector
// (internal/drift) delegates its spec loading here; behavior is identical to
// the pre-split loader.
package openapi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/vinifera-io/collector/contract"
)

// LoadFile reads and validates an OpenAPI document from disk.
// (Moved verbatim from internal/drift.LoadSpecFile — the collector's drift
// detector delegates here; behavior is identical.)
func LoadFile(path string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("spec invalid: %w", err)
	}
	return doc, nil
}

// LoadData parses an OpenAPI document from bytes.
// (Moved verbatim from internal/drift.LoadSpecData.)
func LoadData(b []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("spec invalid: %w", err)
	}
	return doc, nil
}

// From normalizes a validated OpenAPI document into a Contract.
//
// Per operation:
//   - id    = "<METHOD> <pathTemplate>"
//   - input = path+query parameters and the application/json requestBody,
//     normalized into ONE JSON Schema: when only a body is declared, its
//     schema passes through as-is; when parameters are declared, they become
//     properties of an object schema (required per the parameter), merged
//     with an object body's properties. This is the same "named arguments as
//     one object schema" shape MCP inputSchema already has, which is what
//     makes the two loaders' Operations comparable.
//   - output = the lowest-status 2xx response's application/json schema.
//
// $refs are inlined (the loader resolved them), so the resulting Schema is
// self-contained and deep-comparable.
func From(doc *openapi3.T, edgeRef, observedAt, provenance string) (*contract.Contract, error) {
	c := &contract.Contract{
		Source:  contract.SourceOpenAPI,
		EdgeRef: edgeRef,
		Version: contract.Version{ObservedAt: observedAt, Provenance: provenance},
	}
	if doc.Paths == nil {
		return contract.Finish(c)
	}
	for path, item := range doc.Paths.Map() {
		if item == nil {
			continue
		}
		for method, op := range item.Operations() {
			if op == nil {
				continue
			}
			in, err := inputSchema(item, op)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			out, err := outputSchema(op)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			desc := op.Description
			if desc == "" {
				desc = op.Summary
			}
			c.Operations = append(c.Operations, contract.Operation{
				ID:           strings.ToUpper(method) + " " + path,
				Kind:         contract.KindHTTP,
				Match:        contract.Match{Method: strings.ToUpper(method), PathTemplate: path},
				Description:  desc,
				InputSchema:  in,
				OutputSchema: out,
			})
		}
	}
	return contract.Finish(c)
}

// inputSchema builds the operation's single input JSON Schema from its
// path+query parameters (operation-level over path-item-level) and its
// application/json request body.
func inputSchema(item *openapi3.PathItem, op *openapi3.Operation) (contract.Schema, error) {
	// Effective parameters: path-item params overridden by op params (name+in).
	type paramKey struct{ name, in string }
	eff := map[paramKey]*openapi3.Parameter{}
	var order []paramKey
	add := func(refs openapi3.Parameters) {
		for _, pr := range refs {
			if pr == nil || pr.Value == nil {
				continue
			}
			p := pr.Value
			if p.In != openapi3.ParameterInPath && p.In != openapi3.ParameterInQuery {
				continue
			}
			k := paramKey{p.Name, p.In}
			if _, seen := eff[k]; !seen {
				order = append(order, k)
			}
			eff[k] = p
		}
	}
	add(item.Parameters)
	add(op.Parameters)

	var bodySchema map[string]any
	bodyRequired := false
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		bodyRequired = op.RequestBody.Value.Required
		if mt := op.RequestBody.Value.Content.Get("application/json"); mt != nil && mt.Schema != nil {
			m, err := schemaRefToMap(mt.Schema, map[*openapi3.Schema]bool{})
			if err != nil {
				return nil, err
			}
			bodySchema = m
		}
	}

	if len(eff) == 0 {
		if bodySchema == nil {
			return nil, nil
		}
		// Body only: the request schema IS the input schema.
		return contract.CanonicalizeSchema(bodySchema)
	}

	props := map[string]any{}
	var required []string
	sort.Slice(order, func(i, j int) bool {
		if order[i].name != order[j].name {
			return order[i].name < order[j].name
		}
		return order[i].in < order[j].in
	})
	for _, k := range order {
		p := eff[k]
		var pm map[string]any
		if p.Schema != nil {
			m, err := schemaRefToMap(p.Schema, map[*openapi3.Schema]bool{})
			if err != nil {
				return nil, err
			}
			pm = m
		} else {
			pm = map[string]any{}
		}
		if p.Description != "" && pm["description"] == nil {
			pm["description"] = p.Description
		}
		props[p.Name] = pm
		if p.Required {
			required = append(required, p.Name)
		}
	}

	if bodySchema != nil {
		if bp, ok := bodySchema["properties"].(map[string]any); ok {
			// Object body: merge its named properties with the parameters.
			for name, ps := range bp {
				props[name] = ps
			}
			if br, ok := bodySchema["required"].([]any); ok {
				for _, r := range br {
					if s, ok := r.(string); ok {
						required = append(required, s)
					}
				}
			}
		} else {
			// Non-object body next to parameters: keep it addressable as "body".
			props["body"] = bodySchema
			if bodyRequired {
				required = append(required, "body")
			}
		}
	}

	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return contract.CanonicalizeSchema(out)
}

// outputSchema picks the lowest-status 2xx response's application/json
// schema; nil when the operation declares none (an honest "no output contract").
func outputSchema(op *openapi3.Operation) (contract.Schema, error) {
	if op.Responses == nil {
		return nil, nil
	}
	var codes []int
	byCode := map[int]*openapi3.ResponseRef{}
	for status, rr := range op.Responses.Map() {
		n, err := strconv.Atoi(status)
		if err != nil || n < 200 || n > 299 || rr == nil || rr.Value == nil {
			continue
		}
		codes = append(codes, n)
		byCode[n] = rr
	}
	sort.Ints(codes)
	for _, n := range codes {
		if mt := byCode[n].Value.Content.Get("application/json"); mt != nil && mt.Schema != nil {
			m, err := schemaRefToMap(mt.Schema, map[*openapi3.Schema]bool{})
			if err != nil {
				return nil, err
			}
			return contract.CanonicalizeSchema(m)
		}
	}
	return nil, nil
}

// schemaRefToMap converts a kin-openapi schema into a plain JSON Schema map,
// inlining resolved $refs and translating OpenAPI 3.0 `nullable` into a
// `type` union with "null". Only the constraint surface the drift pipeline
// judges is carried (types/shapes/enums/bounds — technical adherence only).
func schemaRefToMap(ref *openapi3.SchemaRef, seen map[*openapi3.Schema]bool) (map[string]any, error) {
	if ref == nil || ref.Value == nil {
		return nil, nil
	}
	s := ref.Value
	if seen[s] {
		// Cyclic spec: keep the reference opaque rather than recursing forever.
		return map[string]any{"$ref": ref.Ref}, nil
	}
	seen[s] = true
	defer delete(seen, s)

	out := map[string]any{}
	if s.Type != nil {
		types := append([]string(nil), s.Type.Slice()...)
		if s.Nullable && !contains(types, "null") {
			types = append(types, "null")
		}
		if len(types) == 1 {
			out["type"] = types[0]
		} else if len(types) > 1 {
			out["type"] = types
		}
	} else if s.Nullable {
		out["type"] = "null"
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if s.Format != "" {
		out["format"] = s.Format
	}
	if len(s.Enum) > 0 {
		out["enum"] = append([]any(nil), s.Enum...)
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}
	if s.MinLength > 0 {
		out["minLength"] = s.MinLength
	}
	if s.MaxLength != nil {
		out["maxLength"] = *s.MaxLength
	}
	if s.Min != nil {
		out["minimum"] = *s.Min
	}
	if s.Max != nil {
		out["maximum"] = *s.Max
	}
	if b := s.ExclusiveMin.Bool; b != nil && *b {
		out["exclusiveMinimum"] = true // OpenAPI 3.0 boolean modifier
	} else if v := s.ExclusiveMin.Value; v != nil {
		out["exclusiveMinimum"] = *v // OpenAPI 3.1 bound value
	}
	if b := s.ExclusiveMax.Bool; b != nil && *b {
		out["exclusiveMaximum"] = true
	} else if v := s.ExclusiveMax.Value; v != nil {
		out["exclusiveMaximum"] = *v
	}
	if s.MinItems > 0 {
		out["minItems"] = s.MinItems
	}
	if s.MaxItems != nil {
		out["maxItems"] = *s.MaxItems
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, pr := range s.Properties {
			m, err := schemaRefToMap(pr, seen)
			if err != nil {
				return nil, err
			}
			props[name] = m
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = append([]string(nil), s.Required...)
	}
	if s.Items != nil {
		m, err := schemaRefToMap(s.Items, seen)
		if err != nil {
			return nil, err
		}
		out["items"] = m
	}
	if s.AdditionalProperties.Has != nil {
		out["additionalProperties"] = *s.AdditionalProperties.Has
	} else if s.AdditionalProperties.Schema != nil {
		m, err := schemaRefToMap(s.AdditionalProperties.Schema, seen)
		if err != nil {
			return nil, err
		}
		out["additionalProperties"] = m
	}
	for kw, refs := range map[string]openapi3.SchemaRefs{"oneOf": s.OneOf, "anyOf": s.AnyOf, "allOf": s.AllOf} {
		if len(refs) == 0 {
			continue
		}
		arr := make([]any, 0, len(refs))
		for _, r := range refs {
			m, err := schemaRefToMap(r, seen)
			if err != nil {
				return nil, err
			}
			arr = append(arr, m)
		}
		out[kw] = arr
	}
	return out, nil
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
