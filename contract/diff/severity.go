package diff

import (
	"strings"
	"unicode"
)

// Kind and Severity are the two SEPARATE fields every classified change
// carries (ruling R-A, Idan 2026-09-17). They replace the single `Class`
// label, which mixed the two axes in one vocabulary: "DESCRIPTION" named WHAT
// moved while "BREAKING" named HOW MUCH it mattered, so the two could never be
// read independently and a wording change could not be told from a harmless
// one. Kind NEVER implies Severity; Severity is assigned only by the table in
// this file.

// Kind is WHAT moved.
type Kind string

const (
	// KindWording — a tool or parameter description changed.
	KindWording Kind = "wording"
	// KindInput — the argument surface a caller sends moved.
	KindInput Kind = "input"
	// KindOutput — the DECLARED response surface moved.
	KindOutput Kind = "output"
	// KindCatalog — the set of tools on offer moved.
	KindCatalog Kind = "catalog"
	// KindValue — the MEANING of a value in observed responses moved (units,
	// ID format, enum casing, timestamp format). Collector-only: it needs
	// observed responses, which a declaration diff never sees. Declared here
	// so the whole vocabulary lives in one place; Classify never emits it.
	KindValue Kind = "value"
	// KindObservedFailure — a call that previously worked now fails (-32602 on
	// previously valid arguments; stale_client on a real call). Collector-only,
	// for the same reason as KindValue; Classify never emits it.
	KindObservedFailure Kind = "observed_failure"
)

// Severity is HOW MUCH it matters. The spellings are upper-case because this
// is the classifier's own vocabulary and it replaces the upper-case
// BREAKING / NON_BREAKING / DESCRIPTION that the published drift dataset used.
// The collector's FINDING wire (model.Severity*) stays lower-case and is
// mapped at that boundary — see internal/drift.severityOf — because changing
// the case of a field the control plane, the dashboard and e2e all read would
// be a breaking wire change for no gain.
type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityWarning  Severity = "WARNING"
	SeverityBreaking Severity = "BREAKING"
)

// RuleCatalogMovedBehindMetaTools is the ONE change emitted when a server's
// catalog moves behind discovery meta-tools. It is never a removal per hidden
// tool (R-B). Classify cannot detect it — it is a property of HOW a catalog
// was obtained, which only the snapshot/expansion layer knows — so no code in
// this package emits it. It lives here because the rule table is the single
// place the vocabulary is defined, and a rule missing from the table is a test
// failure rather than a silent unknown.
const RuleCatalogMovedBehindMetaTools = "catalog-moved-behind-meta-tools"

// verdict is one row of R-B's severity table.
type verdict struct {
	kind Kind
	sev  Severity
	// reported is false for the ADDITIVE cells: a new tool, a new optional
	// param, a widened input, a newly declared output schema. R-B's last row
	// reads "additive: not reported (unchanged)". Such a change is real and
	// Classify still returns it — callers that want the whole diff get it —
	// but it is not a finding and must never reach a published count.
	reported bool
}

// rbTable IS ruling R-B. Every rule this package can emit has exactly one row
// here, and TestRBTableIsTotal asserts that. Severity is read off this table
// and nowhere else, so no construction site can disagree with it.
//
// Where R-B names a cell outright the comment says "R-B"; where the cell had
// to be read off the nearest row the comment says "R-B, by family" and names
// the row it follows. Those readings were put to Idan on 2026-09-17 and the
// input family (every input change is INFO except a new REQUIRED param) was
// ruled explicitly.
var rbTable = map[string]verdict{
	// --- catalog -----------------------------------------------------------
	RuleOperationRemoved: {KindCatalog, SeverityBreaking, true}, // R-B: "tool removed"
	// A tool under a new name breaks callers of the old name exactly as a
	// removal does. R-B, by family: the "tool removed" row.
	RuleOperationRenamed:            {KindCatalog, SeverityBreaking, true},
	RuleCatalogMovedBehindMetaTools: {KindCatalog, SeverityInfo, true}, // R-B: the meta-tools row
	RuleOperationAdded:              {KindCatalog, "", false},          // R-B: additive ("new tool")

	// --- input -------------------------------------------------------------
	// R-B: "new REQUIRED param" is the ONE input cell above INFO.
	RuleInputRequiredPropertyAdded: {KindInput, SeverityWarning, true},
	RuleInputOptionalPropertyAdded: {KindInput, "", false}, // R-B: additive ("new optional param")
	// R-B: "param renamed" — and ruled explicitly on 2026-09-17, including
	// that a rename stays INFO even when the new name is REQUIRED: it is the
	// same parameter under a new spelling, not a new obligation.
	RuleInputPropertyRenamed: {KindInput, SeverityInfo, true},
	// Idan, 2026-09-17: a removed input property is input/INFO. R-B's input
	// rows name "param renamed, type narrowed, enum value removed" and a bare
	// removal is the unpaired half of the first of those.
	RuleInputRequiredPropertyRemoved: {KindInput, SeverityInfo, true},
	RuleInputOptionalPropertyRemoved: {KindInput, SeverityInfo, true},
	RuleInputTypeNarrowed:            {KindInput, SeverityInfo, true}, // R-B: "type narrowed"
	// R-B, by family: the "type narrowed" row. A swapped type set is a
	// narrowing in every direction a caller can observe.
	RuleInputTypeChanged: {KindInput, SeverityInfo, true},
	// A widened input accepts everything it accepted before: additive.
	// R-B, by family: the additive row.
	RuleInputTypeWidened:      {KindInput, "", false},
	RuleInputEnumValueRemoved: {KindInput, SeverityInfo, true}, // R-B: "enum value removed"
	// R-B, by family: the additive row — a caller's existing value still
	// validates.
	RuleInputEnumValueAdded: {KindInput, "", false},
	// Contains a removal, so it follows the removal row, not the additive one.
	RuleInputEnumValueReplaced: {KindInput, SeverityInfo, true},

	// --- output ------------------------------------------------------------
	// R-B: "declared output field removed/renamed ... BREAKING".
	RuleOutputRequiredPropertyRemoved: {KindOutput, SeverityBreaking, true},
	RuleOutputPropertyRenamed:         {KindOutput, SeverityBreaking, true},
	// R-B: "OPTIONAL declared output field removed ... WARNING". This cell is
	// why RuleOutputOptionalPropertyRemoved exists at all — before R-B the
	// classifier deliberately emitted NOTHING for it ("a value consumers were
	// never promised"), so the cell had no rule to hang on.
	RuleOutputOptionalPropertyRemoved: {KindOutput, SeverityWarning, true},
	// Idan, 2026-09-17: an OPTIONAL output property renamed is ONE row at
	// WARNING — the grade of that property being removed, which is what it is
	// to a consumer still reading the old name — never a removal plus an
	// addition.
	RuleOutputOptionalPropertyRenamed: {KindOutput, SeverityWarning, true},
	RuleOutputOptionalPropertyAdded:   {KindOutput, "", false}, // R-B, by family: additive
	// R-B: "output type changed". Direction does not matter on the output
	// side: widened hands the consumer a type it never handled, narrowed makes
	// a branch it wrote dead, and this classifier cannot tell which hurts more
	// without a business judgement it is not allowed to make.
	RuleOutputPropertyTypeWidened:  {KindOutput, SeverityBreaking, true},
	RuleOutputPropertyTypeNarrowed: {KindOutput, SeverityBreaking, true},
	RuleOutputPropertyTypeChanged:  {KindOutput, SeverityBreaking, true},
	// R-B, by family: the "output type changed" row — a value the consumer's
	// code may branch on has disappeared from the declared set.
	RuleOutputEnumValueRemoved: {KindOutput, SeverityBreaking, true},
	// Idan, 2026-09-17: output/WARNING. A consumer may now receive a value it
	// has no branch for, which is worth telling them; it is not BREAKING
	// because nothing they already handle stopped being valid.
	RuleOutputEnumValueAdded: {KindOutput, SeverityWarning, true},
	// Both at once; follows the removal, which is the worse half.
	RuleOutputEnumValueReplaced: {KindOutput, SeverityBreaking, true},
	// The whole declared response surface withdrawn — every declared field
	// removed at once. R-B, by family: the "declared output field removed" row.
	RuleOutputSchemaRemoved: {KindOutput, SeverityBreaking, true},
	// A surface that was never declared becoming declared promises more, not
	// less. R-B, by family: additive.
	RuleOutputSchemaDeclared: {KindOutput, "", false},

	// --- wording -----------------------------------------------------------
	// R-B: "tool or param description changed ... WARNING". The one-per-tool-
	// per-day cap and the whitespace/case/punctuation filter are NOT applied
	// here: this package diffs ONE pair of revisions and has no notion of a
	// day. TrivialWordingChange below is the filter; the cap belongs to
	// whatever aggregates a day's comparisons.
	RuleDescriptionChanged: {KindWording, SeverityWarning, true},
}

// lookup returns R-B's verdict for a rule, and whether the rule is known.
func lookup(rule string) (verdict, bool) {
	v, ok := rbTable[rule]
	return v, ok
}

// stamp applies R-B to every change in place. Classify builds its changes
// carrying a rule id and calls this once, so Kind, Severity and Reported are
// derived from the table at exactly one point in the program.
func stamp(changes []Change) {
	for i := range changes {
		v, ok := lookup(changes[i].Rule)
		if !ok {
			// Unreachable while TestRBTableIsTotal passes. Left explicit
			// rather than defaulting to a severity: a rule with no ruled
			// severity must not be published as though it had one.
			changes[i].Kind, changes[i].Severity, changes[i].Reported = "", "", false
			continue
		}
		changes[i].Kind, changes[i].Severity, changes[i].Reported = v.kind, v.sev, v.reported
	}
}

// Reportable filters a change list down to the findings R-B reports, dropping
// the additive cells. Published counts are computed over this, never over the
// raw diff.
func Reportable(changes []Change) []Change {
	out := make([]Change, 0, len(changes))
	for _, c := range changes {
		if c.Reported {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Rank orders severities for "the highest severity in this set" questions —
// the R-D change event carries the highest severity of its findings.
func Rank(s Severity) int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityBreaking:
		return 3
	}
	return 0
}

// Highest returns the highest severity among reportable changes, or "" when
// there are none.
func Highest(changes []Change) Severity {
	var best Severity
	for _, c := range changes {
		if c.Reported && Rank(c.Severity) > Rank(best) {
			best = c.Severity
		}
	}
	return best
}

// TrivialWordingChange reports whether two descriptions differ ONLY in
// whitespace, letter case or punctuation — the diffs R-B says to ignore.
//
// The comparison keeps letters and digits (in any script — a Unicode category
// test, not an ASCII one) and drops everything else, then folds case. So an em
// dash becoming a hyphen is trivial, and so is a re-wrapped paragraph or a
// sentence that gained a full stop; "max 10" becoming "max 100" is not.
//
// This exists because the drift dataset published a wording finding whose
// entire content was `—` becoming `-` (a2awire-weather / data_session_open,
// 2026-09-08). It was that server's only finding that day, so one typographic
// substitution became a whole day of "drift" in the headline rate.
func TrivialWordingChange(before, after string) bool {
	return foldWording(before) == foldWording(after)
}

func foldWording(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
