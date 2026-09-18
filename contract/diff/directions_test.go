package diff

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flanj-io/collector/contract"
)

// The cells of the rule table (contracts/CONTRACTS.md §4), one test case
// each. Every case here defends a defect the drift record surfaced on
// 2026-09-13 (mcp-drift-watch, data/changes.jsonl):
//
//   - any moved type set was BREAKING under one id with no direction —
//     apify's `number` -> `["number","string"]` widenings and
//     `["null","string"]` -> `"string"` narrowings read exactly like swaps;
//   - every input property removal was BREAKING regardless of `required` —
//     58 of 98 removals had never been required;
//   - an enum swap emitted a removed row AND an added row carrying the same
//     two full lists — dimhour's `["city","region"]` -> `["city","state"]`
//     read as two changes;
//   - a property renamed under a normalised spelling read as a removal plus a
//     (required) addition — 86 of neon's 93 removals were `branchId` ->
//     `branch_id` pairs.

func mkTools(t *testing.T, toolsJSON string) *contract.Contract {
	t.Helper()
	tools, err := contract.ParseToolsList(json.RawMessage(`{"tools":` + toolsJSON + `}`))
	if err != nil {
		t.Fatalf("parse tools list: %v", err)
	}
	c, err := contract.FromToolsList(tools, "mcp.provider.test|outbound", "2026-09-13T00:00:00Z", "test")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return c
}

// oneTool builds a one-tool list with the given input/output schemas (either
// may be empty to omit it).
func oneTool(input, output string) string {
	s := `[{"name":"t","inputSchema":` + orObject(input)
	if output != "" {
		s += `,"outputSchema":` + output
	}
	return s + `}]`
}

func orObject(s string) string {
	if s == "" {
		return `{"type":"object"}`
	}
	return s
}

func prop(name, schema string) string {
	return `{"type":"object","properties":{"` + name + `":` + schema + `}}`
}

// wantOne asserts exactly one change, by (severity, rule, fieldPath). The
// direction table's whole point is which SEVERITY a (side, direction) cell
// carries, so severity is what these cases assert; Kind follows from the rule
// and is covered exhaustively by TestRBTableIsTotal.
func wantOne(t *testing.T, got []Change, sev Severity, rule, fieldPath string) Change {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("got %d changes, want exactly 1:\n%s", len(got), dump(got))
	}
	c := got[0]
	if c.Severity != sev || c.Rule != rule || c.FieldPath != fieldPath {
		t.Fatalf("got %s %s at %q, want %s %s at %q", c.Severity, c.Rule, c.FieldPath, sev, rule, fieldPath)
	}
	return c
}

func dump(cs []Change) string {
	var b strings.Builder
	for _, c := range cs {
		bb, _ := json.Marshal(c)
		b.Write(bb)
		b.WriteByte('\n')
	}
	return b.String()
}

func typeFrag(t any) map[string]any { return map[string]any{"type": t} }

// TestTypeDirection_Cells pins every (side × direction) cell of the type
// table, including the integer ⊂ number subtype and the equivalent-sets
// no-op.
func TestTypeDirection_Cells(t *testing.T) {
	cases := []struct {
		name          string
		side          side
		before, aft   string // the property's schema on each side
		sev           Severity
		rule          string
		fragBefore    any
		fragAfter     any
		noChangeAtAll bool
	}{
		// ── input: INFO except the additive cells (R-B; Idan 2026-09-17) ────
		// The class of these cells USED to vary (narrowed/changed breaking,
		// widened non-breaking). R-B moved the whole input family to INFO and
		// reserved WARNING for a new REQUIRED param: the caller controls their
		// own arguments, so a moved input surface is information for them, not
		// a promise broken to them. Direction still decides the RULE ID, which
		// is what tells a caller what to do — only the severity collapsed.
		{name: "input widened (string -> [string,null]) is the one additive cell",
			side: sideInput, before: `{"type":"string"}`, aft: `{"type":["string","null"]}`,
			sev: "", rule: RuleInputTypeWidened,
			// the loader canonicalises a type union (sorted), so the fragment is the normalised one
			fragBefore: typeFrag("string"), fragAfter: typeFrag([]any{"null", "string"})},
		{name: "input narrowed ([integer,string] -> integer): a caller sending the dropped type now fails",
			side: sideInput, before: `{"type":["integer","string"]}`, aft: `{"type":"integer"}`,
			sev: SeverityInfo, rule: RuleInputTypeNarrowed,
			fragBefore: typeFrag([]any{"integer", "string"}), fragAfter: typeFrag("integer")},
		{name: "input changed (integer -> string): a swap",
			side: sideInput, before: `{"type":"integer"}`, aft: `{"type":"string"}`,
			sev: SeverityInfo, rule: RuleInputTypeChanged,
			fragBefore: typeFrag("integer"), fragAfter: typeFrag("string")},
		{name: "input number -> integer narrows (every integer is a number; neon list_projects.limit)",
			side: sideInput, before: `{"type":"number"}`, aft: `{"type":"integer"}`,
			sev: SeverityInfo, rule: RuleInputTypeNarrowed,
			fragBefore: typeFrag("number"), fragAfter: typeFrag("integer")},
		{name: "input integer -> number widens",
			side: sideInput, before: `{"type":"integer"}`, aft: `{"type":"number"}`,
			sev: "", rule: RuleInputTypeWidened,
			fragBefore: typeFrag("integer"), fragAfter: typeFrag("number")},
		{name: "input [number,integer] -> number accepts the same values: no change",
			side: sideInput, before: `{"type":["number","integer"]}`, aft: `{"type":"number"}`,
			noChangeAtAll: true},
		{name: "input union reordered is no change",
			side: sideInput, before: `{"type":["string","null"]}`, aft: `{"type":["null","string"]}`,
			noChangeAtAll: true},
		// ── output: every cell BREAKING, each under its own id ──────────────
		{name: "output widened (number -> [number,string]; apify logo.height): the consumer may receive a type it never handled",
			side: sideOutput, before: `{"type":"number"}`, aft: `{"type":["number","string"]}`,
			sev: SeverityBreaking, rule: RuleOutputPropertyTypeWidened,
			fragBefore: typeFrag("number"), fragAfter: typeFrag([]any{"number", "string"})},
		{name: "output narrowed ([null,string] -> string): the null branch is dead code — the response-enum posture, applied to a type member",
			side: sideOutput, before: `{"type":["null","string"]}`, aft: `{"type":"string"}`,
			sev: SeverityBreaking, rule: RuleOutputPropertyTypeNarrowed,
			fragBefore: typeFrag([]any{"null", "string"}), fragAfter: typeFrag("string")},
		{name: "output narrowed ([null,string] -> [null]): the data itself is gone — same id, the classifier cannot tell the two apart",
			side: sideOutput, before: `{"type":["null","string"]}`, aft: `{"type":["null"]}`,
			sev: SeverityBreaking, rule: RuleOutputPropertyTypeNarrowed,
			fragBefore: typeFrag([]any{"null", "string"}), fragAfter: typeFrag([]any{"null"})},
		{name: "output changed (number -> string)",
			side: sideOutput, before: `{"type":"number"}`, aft: `{"type":"string"}`,
			sev: SeverityBreaking, rule: RuleOutputPropertyTypeChanged,
			fragBefore: typeFrag("number"), fragAfter: typeFrag("string")},
		{name: "output integer -> number widens, and widening a response is breaking",
			side: sideOutput, before: `{"type":"integer"}`, aft: `{"type":"number"}`,
			sev: SeverityBreaking, rule: RuleOutputPropertyTypeWidened,
			fragBefore: typeFrag("integer"), fragAfter: typeFrag("number")},
		{name: "output [number,integer] -> number: no change",
			side: sideOutput, before: `{"type":["number","integer"]}`, aft: `{"type":"number"}`,
			noChangeAtAll: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var before, after *contract.Contract
			var path string
			if tc.side == sideInput {
				before = mkTools(t, oneTool(prop("x", tc.before), ""))
				after = mkTools(t, oneTool(prop("x", tc.aft), ""))
				path = "input.x"
			} else {
				before = mkTools(t, oneTool("", prop("x", tc.before)))
				after = mkTools(t, oneTool("", prop("x", tc.aft)))
				path = "output.x"
			}
			got := Classify(before, after)
			if tc.noChangeAtAll {
				if len(got) != 0 {
					t.Fatalf("want no change, got:\n%s", dump(got))
				}
				return
			}
			c := wantOne(t, got, tc.sev, tc.rule, path)
			if !reflect.DeepEqual(c.Before, tc.fragBefore) || !reflect.DeepEqual(c.After, tc.fragAfter) {
				t.Errorf("fragments: got %v -> %v, want %v -> %v", c.Before, c.After, tc.fragBefore, tc.fragAfter)
			}
			if c.Detail != "" {
				t.Errorf("a type cell carries no Detail; got %q", c.Detail)
			}
		})
	}
}

// TestInputPropertyRemoved_RequiredVsOptional pins the removal cells: a
// required removal is breaking; an optional one is breaking only when the new
// schema forbids the stray argument, and the Detail says which.
func TestInputPropertyRemoved_RequiredVsOptional(t *testing.T) {
	const twoProps = `{"type":"object","properties":{"keep":{"type":"string"},"gone":{"type":"string","description":"v4 only"}},"required":["keep"%s]%s}`
	cases := []struct {
		name         string
		wasRequired  bool
		newTail      string // appended to the NEW schema after required
		sev          Severity
		rule         string
		detailHas    string
		detailAbsent bool
	}{
		// Every cell here is INFO under R-B. The DETAIL still states the
		// consequence, and it is the only thing that separates these cells now
		// — which is the point of R-A: the rule id and the detail say WHAT
		// happened, the severity says how much it matters, and they are not the
		// same question. A caller whose argument now fails validation reads the
		// detail; the published severity does not inflate on their behalf.
		{name: "required removed", wasRequired: true, sev: SeverityInfo, rule: RuleInputRequiredPropertyRemoved, detailAbsent: true},
		{name: "optional removed, new schema additionalProperties:false (aave get_apy_history.reserve): callers still sending it now fail",
			newTail: `,"additionalProperties":false`, sev: SeverityInfo, rule: RuleInputOptionalPropertyRemoved, detailHas: "additionalProperties: false"},
		{name: "optional removed, additionalProperties absent: tolerated, the value just has no effect",
			sev: SeverityInfo, rule: RuleInputOptionalPropertyRemoved, detailHas: "no longer has a declared effect"},
		{name: "optional removed, additionalProperties:true: tolerated",
			newTail: `,"additionalProperties":true`, sev: SeverityInfo, rule: RuleInputOptionalPropertyRemoved, detailHas: "not rejected"},
		{name: "optional removed, additionalProperties is a schema: tolerated",
			newTail: `,"additionalProperties":{"type":"string"}`, sev: SeverityInfo, rule: RuleInputOptionalPropertyRemoved, detailHas: "not rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ""
			if tc.wasRequired {
				req = `,"gone"`
			}
			beforeSchema := strings.Replace(strings.Replace(twoProps, "%s", req, 1), "%s", "", 1)
			afterSchema := `{"type":"object","properties":{"keep":{"type":"string"}},"required":["keep"]` + tc.newTail + `}`
			got := Classify(mkTools(t, oneTool(beforeSchema, "")), mkTools(t, oneTool(afterSchema, "")))
			c := wantOne(t, got, tc.sev, tc.rule, "input.gone")
			if !reflect.DeepEqual(c.Before, map[string]any{"type": "string", "description": "v4 only"}) {
				t.Errorf("before fragment = %v, want the removed property's schema", c.Before)
			}
			if c.After != nil {
				t.Errorf("a removal carries no after fragment; got %v", c.After)
			}
			if tc.detailAbsent && c.Detail != "" {
				t.Errorf("required removal carries no Detail; got %q", c.Detail)
			}
			if tc.detailHas != "" && (!strings.Contains(c.Detail, tc.detailHas) || !strings.Contains(c.Detail, "`gone`")) {
				t.Errorf("Detail %q must name the property and say %q", c.Detail, tc.detailHas)
			}
		})
	}

	// OVERTURNED 2026-09-17. This case used to assert that an optional OUTPUT
	// removal produced no change at all — the standing posture that it is "a
	// value consumers were never promised". R-B gives the cell a severity
	// (WARNING), so the silence was the bug: a provider could stop declaring a
	// field consumers were reading and the diff said nothing. The premise
	// changed, so the assertion changed with it; the case is kept rather than
	// deleted so the reversal stays on the record.
	t.Run("output optional removal is WARNING (R-B; was unclassified before 2026-09-17)", func(t *testing.T) {
		before := mkTools(t, oneTool("", `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"]}`))
		after := mkTools(t, oneTool("", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`))
		wantOne(t, Classify(before, after), SeverityWarning, RuleOutputOptionalPropertyRemoved, "output.b")
	})
}

// TestEnum_OneChangePerField pins that a moved enum is ONE change carrying
// the values that left (before) and the values that arrived (after).
func TestEnum_OneChangePerField(t *testing.T) {
	cases := []struct {
		name        string
		side        side
		before, aft string
		sev         Severity
		rule        string
		removed     []any
		added       []any
	}{
		{name: "input: value removed", side: sideInput,
			before: `["duplicate","fraudulent","requested_by_customer"]`, aft: `["duplicate","requested_by_customer"]`,
			sev: SeverityInfo, rule: RuleInputEnumValueRemoved, removed: []any{"fraudulent"}, added: []any{}},
		{name: "input: value added", side: sideInput,
			before: `["duplicate","fraudulent"]`, aft: `["duplicate","fraudulent","chargeback"]`,
			sev: "", rule: RuleInputEnumValueAdded, removed: []any{}, added: []any{"chargeback"}},
		{name: "input: value replaced (swap) — one row, breaking", side: sideInput,
			before: `["a","b"]`, aft: `["a","c"]`,
			sev: SeverityInfo, rule: RuleInputEnumValueReplaced, removed: []any{"b"}, added: []any{"c"}},
		{name: "output: value removed", side: sideOutput,
			before: `["pending","succeeded","failed"]`, aft: `["pending","succeeded"]`,
			sev: SeverityBreaking, rule: RuleOutputEnumValueRemoved, removed: []any{"failed"}, added: []any{}},
		{name: "output: value added", side: sideOutput,
			before: `["pending","succeeded"]`, aft: `["pending","succeeded","requires_action"]`,
			sev: SeverityWarning, rule: RuleOutputEnumValueAdded, removed: []any{}, added: []any{"requires_action"}},
		{name: "output: value replaced (dimhour list_cities kind: region -> state) — one row, not removed + added", side: sideOutput,
			before: `["city","region"]`, aft: `["city","state"]`,
			sev: SeverityBreaking, rule: RuleOutputEnumValueReplaced, removed: []any{"region"}, added: []any{"state"}},
		{name: "output: two removed, one added is still one replaced row", side: sideOutput,
			before: `["a","b","c"]`, aft: `["a","d"]`,
			sev: SeverityBreaking, rule: RuleOutputEnumValueReplaced, removed: []any{"b", "c"}, added: []any{"d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := `{"type":"string","enum":` + tc.before + `}`
			as := `{"type":"string","enum":` + tc.aft + `}`
			var before, after *contract.Contract
			var path string
			if tc.side == sideInput {
				before, after = mkTools(t, oneTool(prop("k", bs), "")), mkTools(t, oneTool(prop("k", as), ""))
				path = "input.k"
			} else {
				before, after = mkTools(t, oneTool("", prop("k", bs))), mkTools(t, oneTool("", prop("k", as)))
				path = "output.k"
			}
			c := wantOne(t, Classify(before, after), tc.sev, tc.rule, path)
			wantB := map[string]any{"enum": tc.removed}
			wantA := map[string]any{"enum": tc.added}
			if !reflect.DeepEqual(c.Before, wantB) || !reflect.DeepEqual(c.After, wantA) {
				t.Errorf("fragments: got %v -> %v, want %v -> %v", c.Before, c.After, wantB, wantA)
			}
			// An empty side is stated as [] on the wire, never null.
			b, _ := json.Marshal(c)
			if strings.Contains(string(b), "null") {
				t.Errorf("an empty value list must marshal as [], got %s", b)
			}
		})
	}

	t.Run("enum keyword added or dropped is outside the table", func(t *testing.T) {
		before := mkTools(t, oneTool(prop("k", `{"type":"string"}`), ""))
		after := mkTools(t, oneTool(prop("k", `{"type":"string","enum":["a"]}`), ""))
		if got := Classify(before, after); len(got) != 0 {
			t.Fatalf("want no change, got:\n%s", dump(got))
		}
		if got := Classify(after, before); len(got) != 0 {
			t.Fatalf("want no change, got:\n%s", dump(got))
		}
	})
}

// TestPropertyRenamed pins the rename pairing on both sides, its fragments,
// and the pairs that must NOT rename.
func TestPropertyRenamed(t *testing.T) {
	t.Run("input camelCase -> snake_case, required (neon compare_database_schema.branchId): one INFO row", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"branchId":{"type":"string","description":"The ID of the branch"}},"required":["branchId"]}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"branch_id":{"type":"string","description":"The ID of the branch"}},"required":["branch_id"]}`, ""))
		c := wantOne(t, Classify(before, after), SeverityInfo, RuleInputPropertyRenamed, "input.branchId")
		wantB := map[string]any{"name": "branchId", "schema": map[string]any{"type": "string", "description": "The ID of the branch"}}
		wantA := map[string]any{"name": "branch_id", "schema": map[string]any{"type": "string", "description": "The ID of the branch"}}
		if !reflect.DeepEqual(c.Before, wantB) || !reflect.DeepEqual(c.After, wantA) {
			t.Errorf("fragments: got %v -> %v, want %v -> %v", c.Before, c.After, wantB, wantA)
		}
	})
	t.Run("input optional twin (neon databaseName -> database_name): still one rename, never removed + added", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"databaseName":{"type":"string"}}}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"database_name":{"type":"string"}}}`, ""))
		wantOne(t, Classify(before, after), SeverityInfo, RuleInputPropertyRenamed, "input.databaseName")
	})
	t.Run("kebab-case and PascalCase fold to the same key", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"project-id":{"type":"string"}}}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"ProjectID":{"type":"string"}}}`, ""))
		wantOne(t, Classify(before, after), SeverityInfo, RuleInputPropertyRenamed, "input.project-id")
	})
	t.Run("output required removed with a twin: output-property-renamed", func(t *testing.T) {
		before := mkTools(t, oneTool("", `{"type":"object","properties":{"refundId":{"type":"string"}},"required":["refundId"]}`))
		after := mkTools(t, oneTool("", `{"type":"object","properties":{"refund_id":{"type":"string"}},"required":["refund_id"]}`))
		c := wantOne(t, Classify(before, after), SeverityBreaking, RuleOutputPropertyRenamed, "output.refundId")
		if c.Before.(map[string]any)["name"] != "refundId" || c.After.(map[string]any)["name"] != "refund_id" {
			t.Errorf("fragments: %v -> %v", c.Before, c.After)
		}
	})
	// Idan, 2026-09-17: an OPTIONAL output property renamed pairs exactly like
	// an input one — ONE row, output-optional-property-renamed at WARNING,
	// "renamed X → Y". It used to read as a WARNING removal plus an unreported
	// addition, telling the consumer the field was gone but not where it went.
	t.Run("output OPTIONAL removed with a twin: ONE WARNING rename row", func(t *testing.T) {
		before := mkTools(t, oneTool("", `{"type":"object","properties":{"logoUrl":{"type":"string"}}}`))
		after := mkTools(t, oneTool("", `{"type":"object","properties":{"logo_url":{"type":"string"}}}`))
		c := wantOne(t, Classify(before, after), SeverityWarning, RuleOutputOptionalPropertyRenamed, "output.logoUrl")
		if c.Kind != KindOutput || c.Detail != "renamed logoUrl → logo_url" {
			t.Errorf("got kind %q detail %q", c.Kind, c.Detail)
		}
	})
	t.Run("output OPTIONAL rename of a different type does not pair", func(t *testing.T) {
		before := mkTools(t, oneTool("", `{"type":"object","properties":{"logoUrl":{"type":"string"}}}`))
		after := mkTools(t, oneTool("", `{"type":"object","properties":{"logo_url":{"type":"integer"}}}`))
		got := Classify(before, after)
		if len(got) != 2 || got[0].Rule != RuleOutputOptionalPropertyRemoved || got[1].Rule != RuleOutputOptionalPropertyAdded {
			t.Fatalf("want a removal + an addition, got:\n%s", dump(got))
		}
	})
	t.Run("different types never pair: removed + added", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"branchId":{"type":"string"}},"required":["branchId"]}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"branch_id":{"type":"integer"}},"required":["branch_id"]}`, ""))
		got := Classify(before, after)
		if len(got) != 2 || got[0].Rule != RuleInputRequiredPropertyRemoved || got[1].Rule != RuleInputRequiredPropertyAdded {
			t.Fatalf("want removed + added, got:\n%s", dump(got))
		}
	})
	t.Run("no declared type on the pair never pairs blind", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"branchId":{"description":"x"}}}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"branch_id":{"description":"x"}}}`, ""))
		got := Classify(before, after)
		if len(got) != 2 || got[0].Rule != RuleInputOptionalPropertyRemoved || got[1].Rule != RuleInputOptionalPropertyAdded {
			t.Fatalf("want removed + added, got:\n%s", dump(got))
		}
	})
	t.Run("an unrelated spelling is a real removal and a real addition (aave reserve -> reserveId)", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"reserve":{"type":"string"}},"additionalProperties":false}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"reserveId":{"type":"string"}},"additionalProperties":false}`, ""))
		got := Classify(before, after)
		if len(got) != 2 ||
			got[0].Rule != RuleInputOptionalPropertyRemoved || got[0].Severity != SeverityInfo ||
			!strings.Contains(got[0].Detail, "additionalProperties: false") ||
			got[1].Rule != RuleInputOptionalPropertyAdded || got[1].Reported {
			t.Fatalf("want an INFO optional removal whose detail names additionalProperties:false + an unreported addition, got:\n%s", dump(got))
		}
	})
	t.Run("a change that co-occurs with the rename surfaces under the NEW name", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"sortOrder":{"type":"string","enum":["asc","desc"]}}}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"sort_order":{"type":"string","enum":["asc","desc","none"]}}}`, ""))
		got := Classify(before, after)
		if len(got) != 2 {
			t.Fatalf("want rename + enum change, got:\n%s", dump(got))
		}
		if got[0].Rule != RuleInputPropertyRenamed || got[0].FieldPath != "input.sortOrder" {
			t.Errorf("first change: %+v", got[0])
		}
		if got[1].Rule != RuleInputEnumValueAdded || got[1].FieldPath != "input.sort_order" {
			t.Errorf("second change must be the enum addition under the new name: %+v", got[1])
		}
	})
	t.Run("two removed spellings, one twin: the first pairs, the second is a removal", func(t *testing.T) {
		before := mkTools(t, oneTool(`{"type":"object","properties":{"branchId":{"type":"string"},"branch-id":{"type":"string"}}}`, ""))
		after := mkTools(t, oneTool(`{"type":"object","properties":{"branch_id":{"type":"string"}}}`, ""))
		got := Classify(before, after)
		if len(got) != 2 || got[0].Rule != RuleInputPropertyRenamed || got[0].FieldPath != "input.branch-id" ||
			got[1].Rule != RuleInputOptionalPropertyRemoved || got[1].FieldPath != "input.branchId" {
			t.Fatalf("want one rename (name order) + one removal, got:\n%s", dump(got))
		}
	})
	t.Run("nested: a rename inside items pairs too", func(t *testing.T) {
		before := mkTools(t, oneTool("", `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"cityKey":{"type":"string"}},"required":["cityKey"]}}}}`))
		after := mkTools(t, oneTool("", `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"city_key":{"type":"string"}},"required":["city_key"]}}}}`))
		wantOne(t, Classify(before, after), SeverityBreaking, RuleOutputPropertyRenamed, "output.rows.items.cityKey")
	})
}

// TestNormalizeName pins the folding the rename pairing rests on.
func TestNormalizeName(t *testing.T) {
	for _, n := range []string{"branchId", "branch_id", "branch-id", "BranchID", "BRANCH_ID"} {
		if got := normalizeName(n); got != "branchid" {
			t.Errorf("normalizeName(%q) = %q", n, got)
		}
	}
	if normalizeName("reserve") == normalizeName("reserveId") {
		t.Error("reserve and reserveId must NOT fold together")
	}
}
