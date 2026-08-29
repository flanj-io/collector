// Package diff is the transport-neutral definition-diff classifier
// (v0.5 spec §4 Step A). It compares two revisions of a Contract and
// classifies every change as BREAKING / NON_BREAKING / DESCRIPTION per the
// spec's classification table — the SINGLE implementation, imported by the
// collector's findings pipeline (Step C) and by mcp-drift-watch (Step F).
//
// The classifier is generic over contract.Contract: it never looks at the
// transport (http vs mcp_tool), only at operations and their JSON Schema
// constraint surface. Technical adherence only — types/shapes/enums — never
// business/economic correctness.
//
// Rename pairing — a DELIBERATE strengthening of spec §4.A: the spec's table
// pairs "removed + added with identical inputSchema" as one rename. This
// implementation additionally requires that shared identical inputSchema to
// declare at least one property (len(properties) > 0). Two schema-less or
// empty-object tools carry no identity to match on — any removed tool would
// pair with any added one — so they always classify as operation-removed +
// operation-added, never as a rename.
package diff

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/flanj-io/collector/contract"
)

// Class is the classification of one definition change.
type Class string

const (
	ClassBreaking    Class = "BREAKING"
	ClassNonBreaking Class = "NON_BREAKING"
	ClassDescription Class = "DESCRIPTION"
)

// Rule identifiers. Stable strings: the drift signature
// (edge, operation.id, rule, fieldPath) hangs off them.
const (
	RuleOperationRemoved = "operation-removed"
	RuleOperationRenamed = "operation-renamed"
	RuleOperationAdded   = "operation-added"

	RuleInputRequiredPropertyAdded = "input-required-property-added"
	RuleInputOptionalPropertyAdded = "input-optional-property-added"
	RuleInputPropertyRemoved       = "input-property-removed"
	RuleInputTypeChanged           = "input-type-changed"
	RuleInputEnumValueRemoved      = "input-enum-value-removed"
	RuleInputEnumValueAdded        = "input-enum-value-added"

	RuleOutputRequiredPropertyRemoved = "output-required-property-removed"
	RuleOutputOptionalPropertyAdded   = "output-optional-property-added"
	RuleOutputPropertyTypeChanged     = "output-property-type-changed"
	RuleOutputEnumValueRemoved        = "output-enum-value-removed"
	RuleOutputEnumValueAdded          = "output-enum-value-added"
	RuleOutputSchemaRemoved           = "output-schema-removed"
	RuleOutputSchemaDeclared          = "output-schema-declared"

	RuleDescriptionChanged = "description-changed"
)

// Change is one classified definition change. Before/After carry schema
// FRAGMENTS (the changed keyword or property), never whole schemas.
type Change struct {
	Class       Class  `json:"class"`
	OperationID string `json:"operationId"`
	Rule        string `json:"rule"`
	FieldPath   string `json:"fieldPath"`
	Before      any    `json:"before,omitempty"`
	After       any    `json:"after,omitempty"`
}

// Classify diffs two revisions of a contract (before -> after) and returns the
// classified changes in deterministic order (operations sorted by id,
// properties sorted by name). A nil result means the declared surfaces are
// identical under this table.
//
// Rename detection: an operation removed and an operation added with a
// deep-equal inputSchema that declares at least one property are reported as
// ONE rename (BREAKING for callers of the old name), not as a removed+added
// pair — see the package comment for why schema-less pairs never rename.
// Changes that co-occur with a rename still surface: the paired operations'
// description and output schemas are also diffed, under the NEW id (the
// surviving surface); the input side is skipped — identical by pairing
// construction.
func Classify(before, after *contract.Contract) []Change {
	oldOps := opIndex(before)
	newOps := opIndex(after)

	var removed, added []string
	for id := range oldOps {
		if _, ok := newOps[id]; !ok {
			removed = append(removed, id)
		}
	}
	for id := range newOps {
		if _, ok := oldOps[id]; !ok {
			added = append(added, id)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)

	// Pair renames: removed + added with identical inputSchema that declares
	// at least one property (see the package comment — schema-less or
	// empty-object pairs never rename).
	renamedTo := map[string]string{}   // old id -> new id
	renamedFrom := map[string]string{} // new id -> old id
	for _, oldID := range removed {
		o := oldOps[oldID]
		if !hasDeclaredProperty(o.InputSchema) {
			continue // no property to be "identical" on — never pair blind
		}
		for _, newID := range added {
			if _, taken := renamedFrom[newID]; taken {
				continue
			}
			if reflect.DeepEqual(o.InputSchema, newOps[newID].InputSchema) {
				renamedTo[oldID] = newID
				renamedFrom[newID] = oldID
				break
			}
		}
	}

	ids := map[string]bool{}
	for id := range oldOps {
		ids[id] = true
	}
	for id := range newOps {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	var out []Change
	for _, id := range sorted {
		o, inOld := oldOps[id]
		n, inNew := newOps[id]
		switch {
		case inOld && inNew:
			diffOperation(id, o, n, &out)
		case inOld:
			if newID, ok := renamedTo[id]; ok {
				out = append(out, Change{
					Class: ClassBreaking, OperationID: id, Rule: RuleOperationRenamed,
					Before: id, After: newID,
				})
			} else {
				out = append(out, Change{
					Class: ClassBreaking, OperationID: id, Rule: RuleOperationRemoved,
					Before: id,
				})
			}
		default:
			if oldID, ok := renamedFrom[id]; ok {
				// The rename itself is emitted under the OLD id (inOld branch);
				// the changes that co-occur with it surface here, under the
				// NEW id — the surviving surface. The input side is skipped:
				// identical by pairing construction.
				diffDescription(id, oldOps[oldID], n, &out)
				diffOutput(id, oldOps[oldID], n, &out)
			} else {
				out = append(out, Change{
					Class: ClassNonBreaking, OperationID: id, Rule: RuleOperationAdded,
					After: id,
				})
			}
		}
	}
	return out
}

func opIndex(c *contract.Contract) map[string]*contract.Operation {
	m := map[string]*contract.Operation{}
	if c == nil {
		return m
	}
	for i := range c.Operations {
		m[c.Operations[i].ID] = &c.Operations[i]
	}
	return m
}

func diffOperation(id string, o, n *contract.Operation, out *[]Change) {
	diffDescription(id, o, n, out)

	// Input: a missing schema diffs as an empty one, so declaring or dropping
	// named arguments classifies property-by-property.
	if o.InputSchema != nil || n.InputSchema != nil {
		diffSchema(id, sideInput, "input", orEmpty(o.InputSchema), orEmpty(n.InputSchema), out)
	}

	diffOutput(id, o, n, out)
}

// diffDescription and diffOutput are split out of diffOperation so a renamed
// pair can diff exactly its description + output sides (its input is
// identical by pairing construction).

func diffDescription(id string, o, n *contract.Operation, out *[]Change) {
	if o.Description != n.Description {
		*out = append(*out, Change{
			Class: ClassDescription, OperationID: id, Rule: RuleDescriptionChanged,
			FieldPath: "description", Before: o.Description, After: n.Description,
		})
	}
}

// diffOutput: presence transitions are contract-level per the spec's table.
func diffOutput(id string, o, n *contract.Operation, out *[]Change) {
	switch {
	case o.OutputSchema == nil && n.OutputSchema == nil:
		// no output contract declared, before or after — nothing to judge
	case o.OutputSchema == nil:
		*out = append(*out, Change{
			Class: ClassNonBreaking, OperationID: id, Rule: RuleOutputSchemaDeclared,
			FieldPath: "output", After: typeFragment(n.OutputSchema),
		})
	case n.OutputSchema == nil:
		*out = append(*out, Change{
			Class: ClassBreaking, OperationID: id, Rule: RuleOutputSchemaRemoved,
			FieldPath: "output", Before: typeFragment(o.OutputSchema),
		})
	default:
		diffSchema(id, sideOutput, "output", o.OutputSchema, n.OutputSchema, out)
	}
}

// hasDeclaredProperty reports whether the schema declares at least one named
// property — the identity a rename pairing is allowed to match on.
func hasDeclaredProperty(s contract.Schema) bool {
	if s == nil {
		return false
	}
	props, ok := s["properties"].(map[string]any)
	return ok && len(props) > 0
}

type side int

const (
	sideInput side = iota
	sideOutput
)

// diffSchema walks one schema node pair (old vs new) at fieldPath, emitting
// the table's classifications, then recurses into shared properties and items.
func diffSchema(opID string, s side, path string, old, new map[string]any, out *[]Change) {
	// type changed/narrowed (compared as a set — union order is not semantic)
	oldT, newT := typeSet(old), typeSet(new)
	if len(oldT) > 0 && len(newT) > 0 && !sameStringSet(oldT, newT) {
		rule := RuleInputTypeChanged
		if s == sideOutput {
			rule = RuleOutputPropertyTypeChanged
		}
		*out = append(*out, Change{
			Class: ClassBreaking, OperationID: opID, Rule: rule, FieldPath: path,
			Before: map[string]any{"type": old["type"]},
			After:  map[string]any{"type": new["type"]},
		})
	}

	// enum values removed / added (only when both revisions constrain by enum;
	// adding or dropping the enum keyword itself is outside the table)
	oldE, oldHas := old["enum"].([]any)
	newE, newHas := new["enum"].([]any)
	if oldHas && newHas {
		removed := enumMissingFrom(oldE, newE)
		added := enumMissingFrom(newE, oldE)
		if len(removed) > 0 {
			rule := RuleInputEnumValueRemoved
			if s == sideOutput {
				rule = RuleOutputEnumValueRemoved
			}
			*out = append(*out, Change{
				Class: ClassBreaking, OperationID: opID, Rule: rule, FieldPath: path,
				Before: map[string]any{"enum": oldE}, After: map[string]any{"enum": newE},
			})
		}
		if len(added) > 0 {
			rule := RuleInputEnumValueAdded
			if s == sideOutput {
				rule = RuleOutputEnumValueAdded
			}
			*out = append(*out, Change{
				Class: ClassNonBreaking, OperationID: opID, Rule: rule, FieldPath: path,
				Before: map[string]any{"enum": oldE}, After: map[string]any{"enum": newE},
			})
		}
	}

	// properties removed / added / recursed
	oldProps := propMap(old)
	newProps := propMap(new)
	newReq := requiredSet(new)
	oldReq := requiredSet(old)
	for _, name := range sortedKeys(oldProps) {
		child := path + "." + name
		if _, ok := newProps[name]; !ok {
			if s == sideInput {
				*out = append(*out, Change{
					Class: ClassBreaking, OperationID: opID, Rule: RuleInputPropertyRemoved,
					FieldPath: child, Before: oldProps[name],
				})
			} else if oldReq[name] {
				// spec table: OUTPUT property removal is classified when the
				// property was required (a value consumers were promised)
				*out = append(*out, Change{
					Class: ClassBreaking, OperationID: opID, Rule: RuleOutputRequiredPropertyRemoved,
					FieldPath: child, Before: oldProps[name],
				})
			}
			continue
		}
		op, oOK := oldProps[name].(map[string]any)
		np, nOK := newProps[name].(map[string]any)
		if oOK && nOK {
			diffSchema(opID, s, child, op, np, out)
		}
	}
	for _, name := range sortedKeys(newProps) {
		if _, ok := oldProps[name]; ok {
			continue
		}
		child := path + "." + name
		if s == sideInput {
			if newReq[name] {
				*out = append(*out, Change{
					Class: ClassBreaking, OperationID: opID, Rule: RuleInputRequiredPropertyAdded,
					FieldPath: child, After: newProps[name],
				})
			} else {
				*out = append(*out, Change{
					Class: ClassNonBreaking, OperationID: opID, Rule: RuleInputOptionalPropertyAdded,
					FieldPath: child, After: newProps[name],
				})
			}
		} else {
			*out = append(*out, Change{
				Class: ClassNonBreaking, OperationID: opID, Rule: RuleOutputOptionalPropertyAdded,
				FieldPath: child, After: newProps[name],
			})
		}
	}

	// array items
	if oi, ok := old["items"].(map[string]any); ok {
		if ni, ok := new["items"].(map[string]any); ok {
			diffSchema(opID, s, path+".items", oi, ni, out)
		}
	}
}

func orEmpty(s contract.Schema) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	return s
}

func typeFragment(s contract.Schema) any {
	if s == nil {
		return nil
	}
	if t, ok := s["type"]; ok {
		return map[string]any{"type": t}
	}
	return nil
}

func typeSet(s map[string]any) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// enumMissingFrom returns the values of a that are absent from b, compared by
// canonical JSON encoding (enum members may be any JSON value).
func enumMissingFrom(a, b []any) []any {
	have := map[string]bool{}
	for _, v := range b {
		have[jsonKey(v)] = true
	}
	var out []any
	for _, v := range a {
		if !have[jsonKey(v)] {
			out = append(out, v)
		}
	}
	return out
}

func jsonKey(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}

func propMap(s map[string]any) map[string]any {
	if p, ok := s["properties"].(map[string]any); ok {
		return p
	}
	return map[string]any{}
}

func requiredSet(s map[string]any) map[string]bool {
	out := map[string]bool{}
	if r, ok := s["required"].([]any); ok {
		for _, v := range r {
			if str, ok := v.(string); ok {
				out[str] = true
			}
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
