// Package diff is the transport-neutral definition-diff classifier
// (v0.5 spec §4 Step A). It compares two revisions of a Contract and gives
// every change TWO separate fields — a Kind (what moved) and a Severity (how
// much it matters) — per ruling R-A/R-B (Idan, 2026-09-17) and the rule table
// in contracts/CONTRACTS.md §4. It is the SINGLE implementation, imported by
// the collector's findings pipeline (Step C) and by mcp-drift-watch (Step F).
//
// The single `Class` label it used to emit (BREAKING / NON_BREAKING /
// DESCRIPTION) mixed the two axes in one vocabulary — "DESCRIPTION" named a
// kind while "BREAKING" named a severity — so neither could be read without
// the other. Every severity now comes from the one table in severity.go and
// from nowhere else; see the R-A/R-B notes there for each cell and its
// argument.
//
// The classifier is generic over contract.Contract: it never looks at the
// transport (http vs mcp_tool), only at operations and their JSON Schema
// constraint surface. Technical adherence only — types/shapes/enums — never
// business/economic correctness.
//
// Direction matters (2026-09-13). A change of a property's type SET is judged
// by which way it moved: a union that gained a member (`number` ->
// `["number","string"]`) is a WIDENING, one that lost a member is a NARROWING,
// and a set replaced outright is a CHANGE. The three carry distinct rule ids
// on both sides:
//
//   - input widened is the one additive cell — every argument a caller sends
//     today still validates, so it is not reported at all;
//   - input narrowed / changed keep their own rule ids, and R-B grades the
//     whole input family INFO: a caller controls their own arguments, so a
//     moved input surface is information for them rather than a promise broken
//     to them. The Detail still names the consequence; the severity does not
//     inflate on their behalf. The one input cell above INFO is a new
//     REQUIRED param (WARNING);
//   - EVERY output TYPE cell is breaking. Widened: the consumer may now receive a
//     type it never handled. Narrowed: the product's frozen posture on
//     response enums (internal/drift/versiondiff.go promotes
//     response-property-enum-value-removed to breaking because "a value the
//     consumer's code may branch on has silently disappeared") applies to a
//     type member verbatim — `["null","string"]` -> `["string"]` makes the
//     consumer's null branch dead code, `["null","string"]` -> `["null"]`
//     makes the field's data disappear, and this classifier cannot tell the
//     two apart without a business judgement it is not allowed to make.
//     Changed: both at once.
//
// Not every output cell is breaking, though, and this prose used to say so.
// Two output cells sit below BREAKING in R-B, and the blanket claim
// contradicted the ruled table until 2026-09-17:
//
//   - an output enum that GAINED a value is WARNING (Idan, 2026-09-17): a
//     consumer may now receive a value it has no branch for, which is worth
//     telling them, but nothing they already handle stopped being valid;
//   - an OPTIONAL declared output field removed is WARNING. Before R-B that
//     cell emitted nothing at all, so a provider could stop declaring a field
//     consumers were reading and the diff stayed silent.
//
// JSON Schema's one subtype relation is honoured in the set comparison: every
// `integer` is a `number`, so `number` -> `integer` narrows and `integer` ->
// `number` widens. Two sets that accept the same values under that relation
// (`["number","integer"]` vs `["number"]`) are no change at all.
//
// Removal of an input property still carries its own rule id by whether
// callers were REQUIRED to send it, and the Detail still states the
// consequence (an optional removal under `additionalProperties: false` means a
// caller still sending it now fails validation). R-B grades every one of them
// INFO — see the input-family note above. Optional OUTPUT removals are WARNING
// since R-B; they were unclassified before it, on the standing posture that
// they are a value consumers were never promised.
//
// An enum whose value set moved is ONE change per field per comparison,
// carrying the removed and the added values as its before/after fragments:
// `*-enum-value-removed` when values only left, `*-enum-value-added` when
// values only arrived, `*-enum-value-replaced` when both happened at once — a
// swap used to read as two rows carrying the same two full lists. The
// severities live in severity.go; the replaced cell follows the REMOVED half,
// which is the worse one.
//
// Rename pairing — a DELIBERATE strengthening of spec §4.A: the spec's table
// pairs "removed + added with identical inputSchema" as one rename. This
// implementation additionally requires that shared identical inputSchema to
// declare at least one property (len(properties) > 0). Two schema-less or
// empty-object tools carry no identity to match on — any removed tool would
// pair with any added one — so they always classify as operation-removed +
// operation-added, never as a rename.
//
// The same idea applies one level down: a property removed from a schema node
// with a SAME-TYPED twin added under a name that normalises to the same key
// (camelCase / snake_case / kebab-case fold to one key: `branchId`,
// `branch_id` and `branch-id` are one name) is ONE `*-property-renamed` change
// (breaking — callers and consumers still use the old name), not a removal
// plus an addition. "Same-typed" is a declared, non-empty type set equal on
// both sides; a pair without a declared type never pairs. On the output side
// only a REQUIRED removed property pairs, because an optional output removal
// is not a classified change — its twin stays an optional addition.
package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/flanj-io/collector/contract"
)

// Rule identifiers. Stable strings: the drift signature
// (edge, operation.id, rule, fieldPath) hangs off them. The full table, with
// the class of every cell and the argument for it, is contracts/CONTRACTS.md §4.
const (
	RuleOperationRemoved = "operation-removed"
	RuleOperationRenamed = "operation-renamed"
	RuleOperationAdded   = "operation-added"

	RuleInputRequiredPropertyAdded   = "input-required-property-added"
	RuleInputOptionalPropertyAdded   = "input-optional-property-added"
	RuleInputRequiredPropertyRemoved = "input-required-property-removed"
	RuleInputOptionalPropertyRemoved = "input-optional-property-removed"
	RuleInputPropertyRenamed         = "input-property-renamed"
	RuleInputTypeWidened             = "input-type-widened"
	RuleInputTypeNarrowed            = "input-type-narrowed"
	RuleInputTypeChanged             = "input-type-changed"
	RuleInputEnumValueRemoved        = "input-enum-value-removed"
	RuleInputEnumValueAdded          = "input-enum-value-added"
	RuleInputEnumValueReplaced       = "input-enum-value-replaced"

	RuleOutputRequiredPropertyRemoved = "output-required-property-removed"
	// RuleOutputOptionalPropertyRemoved exists only because R-B gives the cell
	// a severity ("OPTIONAL declared output field removed ... WARNING"). Before
	// that ruling this classifier deliberately emitted NOTHING here — the
	// standing posture was that an optional output field is "a value consumers
	// were never promised" — so a consumer reading a field the provider had
	// quietly stopped declaring got no finding at all.
	RuleOutputOptionalPropertyRemoved = "output-optional-property-removed"
	RuleOutputOptionalPropertyAdded   = "output-optional-property-added"
	RuleOutputPropertyRenamed         = "output-property-renamed"
	RuleOutputPropertyTypeWidened     = "output-property-type-widened"
	RuleOutputPropertyTypeNarrowed    = "output-property-type-narrowed"
	RuleOutputPropertyTypeChanged     = "output-property-type-changed"
	RuleOutputEnumValueRemoved        = "output-enum-value-removed"
	RuleOutputEnumValueAdded          = "output-enum-value-added"
	RuleOutputEnumValueReplaced       = "output-enum-value-replaced"
	RuleOutputSchemaRemoved           = "output-schema-removed"
	RuleOutputSchemaDeclared          = "output-schema-declared"

	RuleDescriptionChanged = "description-changed"
)

// Change is one classified definition change. Before/After carry schema
// FRAGMENTS (the changed keyword or property), never whole schemas. Detail is
// set only where the class depends on context the rule id alone does not
// state (the optional-removal cells): one sentence naming the consequence for
// a caller, in the vocabulary of the schema, never of the business.
type Change struct {
	// Kind is WHAT moved and Severity is HOW MUCH it matters — two separate
	// fields per ruling R-A. Reported is false for R-B's additive cells. All
	// three are stamped from the single table in severity.go; no construction
	// site in this file sets them, so none can disagree with the ruling.
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity,omitempty"`
	Reported bool     `json:"reported"`

	OperationID string `json:"operationId"`
	Rule        string `json:"rule"`
	FieldPath   string `json:"fieldPath"`
	Before      any    `json:"before,omitempty"`
	After       any    `json:"after,omitempty"`
	Detail      string `json:"detail,omitempty"`
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
					OperationID: id, Rule: RuleOperationRenamed,
					Before: id, After: newID,
				})
			} else {
				out = append(out, Change{
					OperationID: id, Rule: RuleOperationRemoved,
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
					OperationID: id, Rule: RuleOperationAdded,
					After: id,
				})
			}
		}
	}
	// R-A/R-B are applied HERE and only here: every change above carries a
	// rule id and nothing else about its gravity, and the table in
	// severity.go turns that into (Kind, Severity, Reported).
	stamp(out)
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
			OperationID: id, Rule: RuleDescriptionChanged,
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
			OperationID: id, Rule: RuleOutputSchemaDeclared,
			FieldPath: "output", After: typeFragment(n.OutputSchema),
		})
	case n.OutputSchema == nil:
		*out = append(*out, Change{
			OperationID: id, Rule: RuleOutputSchemaRemoved,
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
	diffType(opID, s, path, old, new, out)
	diffEnum(opID, s, path, old, new, out)

	// properties removed / renamed / added / recursed
	oldProps := propMap(old)
	newProps := propMap(new)
	newReq := requiredSet(new)
	oldReq := requiredSet(old)
	renamedTo := pairRenamedProperties(s, oldProps, newProps, oldReq)
	renamedFrom := map[string]string{}
	for oldName, newName := range renamedTo {
		renamedFrom[newName] = oldName
	}

	for _, name := range sortedKeys(oldProps) {
		child := path + "." + name
		if newName, ok := renamedTo[name]; ok {
			rule := RuleInputPropertyRenamed
			if s == sideOutput {
				rule = RuleOutputPropertyRenamed
			}
			*out = append(*out, Change{
				OperationID: opID, Rule: rule, FieldPath: child,
				Before: map[string]any{"name": name, "schema": oldProps[name]},
				After:  map[string]any{"name": newName, "schema": newProps[newName]},
			})
			// Changes that co-occur with the rename (an enum, a nested
			// property) surface under the NEW name — the surviving surface —
			// exactly as an operation rename diffs its description + output
			// under the new id. The type is identical by pairing construction.
			if op, ok := oldProps[name].(map[string]any); ok {
				if np, ok := newProps[newName].(map[string]any); ok {
					diffSchema(opID, s, path+"."+newName, op, np, out)
				}
			}
			continue
		}
		if _, ok := newProps[name]; !ok {
			if s == sideInput {
				*out = append(*out, inputPropertyRemoved(opID, child, name, oldProps[name], oldReq[name], new))
			} else {
				// R-B grades an output removal by whether consumers were
				// PROMISED the value: required is BREAKING, optional is
				// WARNING. Before R-B the optional cell emitted nothing at
				// all, so a provider could quietly stop declaring a field
				// consumers were reading and the diff stayed silent.
				rule := RuleOutputOptionalPropertyRemoved
				if oldReq[name] {
					rule = RuleOutputRequiredPropertyRemoved
				}
				*out = append(*out, Change{
					OperationID: opID, Rule: rule,
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
		if _, ok := renamedFrom[name]; ok {
			continue // the surviving half of a rename, reported above
		}
		child := path + "." + name
		if s == sideInput {
			if newReq[name] {
				*out = append(*out, Change{
					OperationID: opID, Rule: RuleInputRequiredPropertyAdded,
					FieldPath: child, After: newProps[name],
				})
			} else {
				*out = append(*out, Change{
					OperationID: opID, Rule: RuleInputOptionalPropertyAdded,
					FieldPath: child, After: newProps[name],
				})
			}
		} else {
			*out = append(*out, Change{
				OperationID: opID, Rule: RuleOutputOptionalPropertyAdded,
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

// diffType classifies a moved type SET by direction — widened / narrowed /
// changed — with the class of each (side, direction) cell from the table in
// the package comment. Union order is not semantic; the sets are compared
// under JSON Schema's integer ⊂ number, and two sets that accept the same
// values are no change.
func diffType(opID string, s side, path string, old, new map[string]any, out *[]Change) {
	oldT, newT := typeSet(old), typeSet(new)
	if len(oldT) == 0 || len(newT) == 0 || sameStringSet(oldT, newT) {
		return
	}
	oldCoversNew := typeCovers(oldT, newT)
	newCoversOld := typeCovers(newT, oldT)
	if oldCoversNew && newCoversOld {
		return // equivalent under integer ⊂ number, e.g. ["number","integer"] vs ["number"]
	}
	var rule string
	switch {
	case newCoversOld: // widened: the new set accepts everything the old one did, and more
		if s == sideInput {
			rule = RuleInputTypeWidened
		} else {
			rule = RuleOutputPropertyTypeWidened
		}
	case oldCoversNew: // narrowed: the new set accepts a strict subset
		if s == sideInput {
			rule = RuleInputTypeNarrowed
		} else {
			rule = RuleOutputPropertyTypeNarrowed
		}
	default: // changed: neither covers the other (a swap)
		if s == sideInput {
			rule = RuleInputTypeChanged
		} else {
			rule = RuleOutputPropertyTypeChanged
		}
	}
	*out = append(*out, Change{
		OperationID: opID, Rule: rule, FieldPath: path,
		Before: map[string]any{"type": old["type"]},
		After:  map[string]any{"type": new["type"]},
	})
}

// diffEnum emits ONE change for a field whose enum value set moved (only when
// both revisions constrain by enum; adding or dropping the enum keyword itself
// is outside the table). The before fragment carries the values that left,
// the after fragment the values that arrived — an empty list on either side is
// stated as `[]`, never omitted.
func diffEnum(opID string, s side, path string, old, new map[string]any, out *[]Change) {
	oldE, oldHas := old["enum"].([]any)
	newE, newHas := new["enum"].([]any)
	if !oldHas || !newHas {
		return
	}
	removed := enumMissingFrom(oldE, newE)
	added := enumMissingFrom(newE, oldE)
	var rule string
	switch {
	case len(removed) > 0 && len(added) > 0:
		rule = RuleInputEnumValueReplaced
		if s == sideOutput {
			rule = RuleOutputEnumValueReplaced
		}
	case len(removed) > 0:
		rule = RuleInputEnumValueRemoved
		if s == sideOutput {
			rule = RuleOutputEnumValueRemoved
		}
	case len(added) > 0:
		rule = RuleInputEnumValueAdded
		if s == sideOutput {
			rule = RuleOutputEnumValueAdded
		}
	default:
		return
	}
	*out = append(*out, Change{
		OperationID: opID, Rule: rule, FieldPath: path,
		Before: map[string]any{"enum": removed}, After: map[string]any{"enum": added},
	})
}

// inputPropertyRemoved classifies the removal of one input property by whether
// callers were required to send it, and — for an optional one — by whether
// the new schema still tolerates it (`additionalProperties: false` turns a
// stray argument into a validation failure). Detail states the consequence,
// because the class of the optional cell is not readable off the rule id.
func inputPropertyRemoved(opID, child, name string, before any, wasRequired bool, new map[string]any) Change {
	if wasRequired {
		return Change{
			OperationID: opID, Rule: RuleInputRequiredPropertyRemoved,
			FieldPath: child, Before: before,
		}
	}
	if additionalPropertiesFalse(new) {
		return Change{
			OperationID: opID, Rule: RuleInputOptionalPropertyRemoved,
			FieldPath: child, Before: before,
			Detail: fmt.Sprintf("callers still sending `%s` now fail validation: the new schema declares additionalProperties: false", name),
		}
	}
	return Change{
		OperationID: opID, Rule: RuleInputOptionalPropertyRemoved,
		FieldPath: child, Before: before,
		Detail: fmt.Sprintf("callers still sending `%s` are not rejected (the new schema does not declare additionalProperties: false), but the value no longer has a declared effect", name),
	}
}

// pairRenamedProperties pairs each removed property with the first added
// property (both in name order, deterministic) whose name normalises to the
// same key and whose declared type set is equal and non-empty. This is the
// rule Idan ruled on 2026-09-17: same tool, same comparison, same side, same
// declared type set, names equal after folding case and dropping `_`/`-`.
//
// On the output side only a REQUIRED removed property pairs. That restriction
// used to follow from optional output removals being unclassified; R-B ended
// that (they are WARNING now), so it survives as a DELIBERATE conservatism:
// grading an optional output rename would need a cell R-B does not state
// (BREAKING for the "renamed" row vs WARNING for the "optional removed" row),
// and inventing one would publish a severity nobody ruled. An optional output
// property that is in fact renamed therefore surfaces as
// output-optional-property-removed (WARNING) plus an unreported addition:
// the consumer is still told the field they read is no longer declared, they
// are just not told the new name. Recorded as an open question, not a bug.
func pairRenamedProperties(s side, oldProps, newProps map[string]any, oldReq map[string]bool) map[string]string {
	var removed, added []string
	for _, name := range sortedKeys(oldProps) {
		if _, ok := newProps[name]; !ok {
			removed = append(removed, name)
		}
	}
	for _, name := range sortedKeys(newProps) {
		if _, ok := oldProps[name]; !ok {
			added = append(added, name)
		}
	}
	renamedTo := map[string]string{}
	taken := map[string]bool{}
	for _, oldName := range removed {
		if s == sideOutput && !oldReq[oldName] {
			continue
		}
		op, ok := oldProps[oldName].(map[string]any)
		if !ok {
			continue
		}
		oldT := typeSet(op)
		if len(oldT) == 0 {
			continue // no declared type to be "same-typed" on — never pair blind
		}
		key := normalizeName(oldName)
		for _, newName := range added {
			if taken[newName] || normalizeName(newName) != key {
				continue
			}
			np, ok := newProps[newName].(map[string]any)
			if !ok || !sameStringSet(oldT, typeSet(np)) {
				continue
			}
			renamedTo[oldName] = newName
			taken[newName] = true
			break
		}
	}
	return renamedTo
}

// normalizeName folds camelCase, snake_case and kebab-case spellings of one
// name onto one key: case is dropped and `_` / `-` are removed, so `branchId`,
// `branch_id`, `branch-id` and `BranchID` are all `branchid`.
func normalizeName(name string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
}

// additionalPropertiesFalse reports whether the schema node forbids properties
// it does not declare. Only the literal `false` counts: an absent keyword, a
// `true` or a schema-valued `additionalProperties` all tolerate a stray key.
func additionalPropertiesFalse(s map[string]any) bool {
	v, ok := s["additionalProperties"].(bool)
	return ok && !v
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

// typeCovers reports whether type set a accepts every value type set b
// accepts: each member of b is in a, or is `integer` while a has `number` —
// JSON Schema's one subtype relation (every integer is a number).
func typeCovers(a, b []string) bool {
	have := map[string]bool{}
	for _, t := range a {
		have[t] = true
	}
	for _, t := range b {
		if have[t] {
			continue
		}
		if t == "integer" && have["number"] {
			continue
		}
		return false
	}
	return true
}

// enumMissingFrom returns the values of a that are absent from b, compared by
// canonical JSON encoding (enum members may be any JSON value). Never nil: an
// empty result is an empty list, so a fragment states `[]` rather than `null`.
func enumMissingFrom(a, b []any) []any {
	have := map[string]bool{}
	for _, v := range b {
		have[jsonKey(v)] = true
	}
	out := []any{}
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
