package diff

import (
	"reflect"
	"strings"
	"testing"
)

// allRules is every rule id this package can put on a Change, listed by hand.
// Listing them here rather than ranging over rbTable is the point: the table
// cannot vouch for its own completeness, so the two lists are independent and
// TestRBTableIsTotal fails when a new rule is added to one and not the other.
var allRules = []string{
	RuleOperationRemoved, RuleOperationRenamed, RuleOperationAdded,
	RuleCatalogMovedBehindMetaTools,

	RuleInputRequiredPropertyAdded, RuleInputOptionalPropertyAdded,
	RuleInputRequiredPropertyRemoved, RuleInputOptionalPropertyRemoved,
	RuleInputPropertyRenamed,
	RuleInputTypeWidened, RuleInputTypeNarrowed, RuleInputTypeChanged,
	RuleInputEnumValueRemoved, RuleInputEnumValueAdded, RuleInputEnumValueReplaced,

	RuleOutputRequiredPropertyRemoved, RuleOutputOptionalPropertyRemoved,
	RuleOutputOptionalPropertyAdded, RuleOutputPropertyRenamed,
	RuleOutputPropertyTypeWidened, RuleOutputPropertyTypeNarrowed, RuleOutputPropertyTypeChanged,
	RuleOutputEnumValueRemoved, RuleOutputEnumValueAdded, RuleOutputEnumValueReplaced,
	RuleOutputSchemaRemoved, RuleOutputSchemaDeclared,

	RuleDescriptionChanged,
}

// TestRBTableIsTotal: every rule has exactly one ruled (kind, severity), and
// the table holds no row for a rule that does not exist. A rule with no ruled
// severity must never be published as though it had one, so the gap is a test
// failure rather than a runtime default.
func TestRBTableIsTotal(t *testing.T) {
	for _, r := range allRules {
		v, ok := lookup(r)
		if !ok {
			t.Errorf("rule %q has no row in R-B's table", r)
			continue
		}
		if v.kind == "" {
			t.Errorf("rule %q has no kind", r)
		}
		if v.reported && v.sev == "" {
			t.Errorf("rule %q is reported but carries no severity", r)
		}
		if !v.reported && v.sev != "" {
			t.Errorf("rule %q is additive (not reported) but carries severity %q", r, v.sev)
		}
	}
	known := map[string]bool{}
	for _, r := range allRules {
		known[r] = true
	}
	for r := range rbTable {
		if !known[r] {
			t.Errorf("R-B's table has a row for %q, which is not a rule this package emits", r)
		}
	}
	if len(rbTable) != len(allRules) {
		t.Errorf("table has %d rows, %d rules exist", len(rbTable), len(allRules))
	}
}

// TestKindNeverImpliesSeverity is R-A stated as a test: knowing the kind must
// not let you infer the severity. If every kind collapsed onto a single
// severity the two fields would be one field again, which is the state R-A
// was issued to end.
func TestKindNeverImpliesSeverity(t *testing.T) {
	sevsByKind := map[Kind]map[Severity]bool{}
	for _, r := range allRules {
		v, _ := lookup(r)
		if !v.reported {
			continue
		}
		if sevsByKind[v.kind] == nil {
			sevsByKind[v.kind] = map[Severity]bool{}
		}
		sevsByKind[v.kind][v.sev] = true
	}
	// input and output each span more than one severity; catalog spans INFO
	// (moved behind meta-tools) and BREAKING (tool removed).
	for _, k := range []Kind{KindInput, KindOutput, KindCatalog} {
		if len(sevsByKind[k]) < 2 {
			t.Errorf("kind %q maps onto only %d severity(ies) %v — kind would imply severity",
				k, len(sevsByKind[k]), sevsByKind[k])
		}
	}
}

// TestSeverityIsStampedNotHandWritten: no construction site may set Kind,
// Severity or Reported itself — the table is the only source. Asserted by
// running a real diff and checking every change agrees with the table.
func TestSeverityIsStampedNotHandWritten(t *testing.T) {
	before := mkTools(t, oneTool(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`, `{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`))
	after := mkTools(t, oneTool(`{"type":"object","properties":{"b":{"type":"number"}}}`, `{"type":"object","properties":{"y":{"type":"string"}}}`))
	got := Classify(before, after)
	if len(got) == 0 {
		t.Fatal("expected changes")
	}
	for _, c := range got {
		v, ok := lookup(c.Rule)
		if !ok {
			t.Fatalf("Classify emitted rule %q with no table row", c.Rule)
		}
		if c.Kind != v.kind || c.Severity != v.sev || c.Reported != v.reported {
			t.Errorf("rule %s stamped (%s,%s,%v), table says (%s,%s,%v)",
				c.Rule, c.Kind, c.Severity, c.Reported, v.kind, v.sev, v.reported)
		}
	}
}

// TestOutputOptionalPropertyRemoved_IsWarning is the cell R-B added. Before
// the ruling this emitted NOTHING: the posture was that an optional output
// field is a value consumers were never promised, so a provider could stop
// declaring a field consumers were reading and the diff stayed silent.
func TestOutputOptionalPropertyRemoved_IsWarning(t *testing.T) {
	before := mkTools(t, oneTool("", `{"type":"object","properties":{"keep":{"type":"string"},"gone":{"type":"string"}},"required":["keep"]}`))
	after := mkTools(t, oneTool("", `{"type":"object","properties":{"keep":{"type":"string"}},"required":["keep"]}`))
	c := wantOne(t, Classify(before, after), SeverityWarning, RuleOutputOptionalPropertyRemoved, "output.gone")
	if c.Kind != KindOutput {
		t.Errorf("kind = %q, want %q", c.Kind, KindOutput)
	}
	if !c.Reported {
		t.Error("an optional output removal is reported under R-B")
	}
	if !reflect.DeepEqual(c.Before, map[string]any{"type": "string"}) {
		t.Errorf("before fragment = %v, want the removed property's schema", c.Before)
	}
}

// TestOutputEnumValueAdded_IsWarning: Idan 2026-09-17. It used to be
// NON_BREAKING here while the package comment asserted the opposite ("EVERY
// output cell is breaking"); the ruling settles it at WARNING and the prose
// was corrected to match.
func TestOutputEnumValueAdded_IsWarning(t *testing.T) {
	before := mkTools(t, oneTool("", `{"type":"object","properties":{"s":{"type":"string","enum":["a","b"]}}}`))
	after := mkTools(t, oneTool("", `{"type":"object","properties":{"s":{"type":"string","enum":["a","b","c"]}}}`))
	c := wantOne(t, Classify(before, after), SeverityWarning, RuleOutputEnumValueAdded, "output.s")
	if c.Kind != KindOutput || !c.Reported {
		t.Errorf("got kind=%q reported=%v, want output/reported", c.Kind, c.Reported)
	}
}

// TestInputFamilyIsInfoExceptNewRequired is Idan's ruling of 2026-09-17 in one
// place: every input change is INFO except a new REQUIRED param (WARNING) and
// the additive cells (unreported). In particular an input removal is INFO even
// when the new schema declares additionalProperties:false — the caller
// controls their own arguments, so a moved input surface is information for
// them, not a promise broken to them.
func TestInputFamilyIsInfoExceptNewRequired(t *testing.T) {
	for _, r := range allRules {
		if !strings.HasPrefix(r, "input-") {
			continue
		}
		v, _ := lookup(r)
		if v.kind != KindInput {
			t.Errorf("rule %q is not kind input", r)
		}
		switch r {
		case RuleInputRequiredPropertyAdded:
			if v.sev != SeverityWarning {
				t.Errorf("%q = %q, want WARNING", r, v.sev)
			}
		case RuleInputOptionalPropertyAdded, RuleInputTypeWidened, RuleInputEnumValueAdded:
			if v.reported {
				t.Errorf("%q is additive and must not be reported", r)
			}
		default:
			if v.sev != SeverityInfo {
				t.Errorf("%q = %q, want INFO", r, v.sev)
			}
		}
	}
}

// TestTrivialWordingChange is R-B's "ignore diffs that are whitespace-, case-
// or punctuation-only". The first case is the real one it was written for:
// a2awire-weather / data_session_open on 2026-09-08, whose entire published
// wording finding was an em dash becoming a hyphen — and which was that
// server's only finding that day, so one substitution became a whole day of
// drift in the headline rate.
func TestTrivialWordingChange(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
		trivial       bool
	}{
		{"em dash -> hyphen (a2awire data_session_open, 2026-09-08)",
			"Buy per-query access — first taste free via data_preview.",
			"Buy per-query access - first taste free via data_preview.", true},
		{"re-wrapped paragraph", "one two\nthree", "one two three", true},
		{"case only", "Cancel an open order.", "cancel an open ORDER.", true},
		{"gained a full stop", "Cancel an open order", "Cancel an open order.", true},
		{"a number changed", "max 10 queries", "max 100 queries", false},
		{"a word changed", "Cancel an open order", "Cancel a closed order", false},
		{"a word removed", "Cancel an open order now", "Cancel an open order", false},
		{"non-latin script is compared, not stripped", "בדיקה אחת", "בדיקה שתיים", false},
		{"non-latin punctuation only", "בדיקה, אחת", "בדיקה אחת", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrivialWordingChange(tc.before, tc.after); got != tc.trivial {
				t.Errorf("TrivialWordingChange(%q, %q) = %v, want %v", tc.before, tc.after, got, tc.trivial)
			}
		})
	}
}

// TestReportableAndHighest: published counts are computed over Reportable, and
// an R-D change event carries the HIGHEST severity of its findings.
func TestReportableAndHighest(t *testing.T) {
	cs := []Change{
		{Rule: RuleOperationAdded}, {Rule: RuleInputPropertyRenamed},
		{Rule: RuleDescriptionChanged}, {Rule: RuleOutputPropertyTypeChanged},
		{Rule: RuleInputOptionalPropertyAdded},
	}
	stamp(cs)
	rep := Reportable(cs)
	if len(rep) != 3 {
		t.Errorf("Reportable kept %d of 5, want 3 (two are additive):\n%s", len(rep), dump(cs))
	}
	if got := Highest(cs); got != SeverityBreaking {
		t.Errorf("Highest = %q, want BREAKING", got)
	}
	if got := Highest([]Change{{Rule: RuleOperationAdded}}); got != "" {
		t.Errorf("Highest over additive-only = %q, want empty", got)
	}
	infoOnly := []Change{{Rule: RuleInputPropertyRenamed}, {Rule: RuleOperationAdded}}
	stamp(infoOnly)
	if got := Highest(infoOnly); got != SeverityInfo {
		t.Errorf("Highest = %q, want INFO", got)
	}
}
