package drift

import (
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// The version-diff deprecation family (CONTRACTS §4). oasdiff computes these
// and grades them INFO; the detector used to drop everything below ERR, so a
// provider could announce a deprecation and the collector said nothing at all
// until the removal — by which time the window the announcement existed to give
// had closed.
//
// These documents are written inline rather than added to contracts/spec-v*.yaml
// on purpose: those two fixtures back the "every version-diff finding is
// breaking" assertion in versiondiff_data_test.go, and a deprecation in them
// would be indistinguishable from this change regressing that one.

// baseDoc is the BEFORE document every case below diffs against: one operation
// with one parameter and a two-property response, nothing deprecated.
const baseDoc = `
openapi: 3.0.3
info: {title: T, version: "1.0.0"}
paths:
  /v1/charges:
    post:
      operationId: createCharge
      parameters:
        - name: legacy_mode
          in: query
          schema: {type: string}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: {type: string}
                  legacy_ref: {type: string}
  /v1/refunds:
    post:
      operationId: createRefund
      responses:
        "200": {description: ok}
`

func findingByRule(t *testing.T, findings []model.Finding, rule string) model.Finding {
	t.Helper()
	for _, f := range findings {
		if f.Rule == rule {
			return f
		}
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.Rule+"/"+f.Severity)
	}
	t.Fatalf("no finding with rule %q; got %v", rule, got)
	return model.Finding{}
}

// TestDeprecatedOperationRaisesAWarning is the headline case: the provider
// marks an operation deprecated and the collector says so, at WARNING — never
// breaking, because nothing has broken yet.
func TestDeprecatedOperationRaisesAWarning(t *testing.T) {
	// v2 deprecates createCharge AND adds a new optional response property.
	// The addition is there to prove the promotion is scoped to the
	// deprecation family: it is sub-ERR too, and it must still be dropped.
	v2 := strings.Replace(baseDoc,
		"      operationId: createCharge\n",
		"      operationId: createCharge\n      deprecated: true\n", 1)
	v2 = strings.Replace(v2,
		"                  legacy_ref: {type: string}\n",
		"                  legacy_ref: {type: string}\n                  added_later: {type: string}\n", 1)
	v2 = strings.Replace(v2, `version: "1.0.0"`, `version: "2.0.0"`, 1)

	findings, err := DetectVersionDiffData([]byte(baseDoc), []byte(v2), "acme-payments")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(findings) != 1 {
		var got []string
		for _, f := range findings {
			got = append(got, f.Rule+"/"+f.Severity)
		}
		t.Fatalf("want exactly one finding (the deprecation); got %d: %v", len(findings), got)
	}

	f := findings[0]
	if f.Rule != "endpoint-deprecated" {
		t.Errorf("rule = %q, want endpoint-deprecated", f.Rule)
	}
	if f.Severity != model.SeverityWarning {
		t.Errorf("severity = %q, want %q — a deprecation is never red", f.Severity, model.SeverityWarning)
	}
	if f.Kind != model.KindDeprecation {
		t.Errorf("kind = %q, want %q — its own kind, not a warning-severity version-diff", f.Kind, model.KindDeprecation)
	}
	if f.Endpoint != "POST /v1/charges" {
		t.Errorf("endpoint = %q, want POST /v1/charges", f.Endpoint)
	}
	if f.SourceCallID != nil {
		t.Error("a version diff is call-less by construction")
	}
	if f.Signature == "" {
		t.Error("no signature — the finding cannot dedup")
	}
	if !f.Flaggable() {
		t.Error("a deprecation must be flaggable: asking the provider when it sunsets is what a thread is for")
	}
}

// TestDeprecatedWithSunsetCarriesTheDate: when the provider publishes a sunset
// date, the finding says when — that date is the whole reason the warning is
// actionable rather than merely true.
func TestDeprecatedWithSunsetCarriesTheDate(t *testing.T) {
	v2 := strings.Replace(baseDoc,
		"      operationId: createCharge\n",
		"      operationId: createCharge\n      deprecated: true\n      x-sunset: \"2099-12-31\"\n", 1)
	v2 = strings.Replace(v2, `version: "1.0.0"`, `version: "2.0.0"`, 1)

	findings, err := DetectVersionDiffData([]byte(baseDoc), []byte(v2), "acme-payments")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	f := findingByRule(t, findings, "endpoint-deprecated-with-sunset")
	if f.Severity != model.SeverityWarning {
		t.Errorf("severity = %q, want warning", f.Severity)
	}
	if !strings.Contains(f.Detail, "2099-12-31") {
		t.Errorf("detail does not carry the sunset date: %q", f.Detail)
	}
}

// TestDeprecatedParameterAndFieldRaiseWarnings: the family is not only
// operations. A deprecated parameter and a deprecated response property are the
// same announcement about a smaller surface.
func TestDeprecatedParameterAndFieldRaiseWarnings(t *testing.T) {
	v2 := strings.Replace(baseDoc,
		"          schema: {type: string}\n",
		"          deprecated: true\n          schema: {type: string}\n", 1)
	v2 = strings.Replace(v2,
		"                  legacy_ref: {type: string}\n",
		"                  legacy_ref: {type: string, deprecated: true}\n", 1)
	v2 = strings.Replace(v2, `version: "1.0.0"`, `version: "2.0.0"`, 1)

	findings, err := DetectVersionDiffData([]byte(baseDoc), []byte(v2), "acme-payments")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	for _, rule := range []string{"request-parameter-deprecated", "response-property-deprecated"} {
		f := findingByRule(t, findings, rule)
		if f.Severity != model.SeverityWarning {
			t.Errorf("%s severity = %q, want warning", rule, f.Severity)
		}
	}
}

// TestBreakingChangesStayBreakingAlongsideADeprecation: the promotion must not
// reach the ERR path. A document that both deprecates one operation and removes
// another produces one of each, graded independently.
func TestBreakingChangesStayBreakingAlongsideADeprecation(t *testing.T) {
	v2 := strings.Replace(baseDoc,
		"      operationId: createCharge\n",
		"      operationId: createCharge\n      deprecated: true\n", 1)
	v2 = strings.Replace(v2, `  /v1/refunds:
    post:
      operationId: createRefund
      responses:
        "200": {description: ok}
`, "", 1)
	v2 = strings.Replace(v2, `version: "1.0.0"`, `version: "2.0.0"`, 1)

	findings, err := DetectVersionDiffData([]byte(baseDoc), []byte(v2), "acme-payments")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	var warnings, breakings int
	for _, f := range findings {
		switch f.Severity {
		case model.SeverityWarning:
			warnings++
			if f.Kind != model.KindDeprecation {
				t.Errorf("warning %s has kind %q, want %q", f.Rule, f.Kind, model.KindDeprecation)
			}
		case model.SeverityBreaking:
			breakings++
			// The ERR path is untouched, and that includes its KIND: a
			// breaking change is still a version-diff finding.
			if f.Kind != model.KindVersionDiff {
				t.Errorf("breaking %s has kind %q — the ERR path must be unchanged", f.Rule, f.Kind)
			}
		default:
			t.Errorf("finding %s has severity %q — only breaking and warning are emitted", f.Rule, f.Severity)
		}
	}
	if warnings != 1 {
		t.Errorf("want 1 warning (the deprecation), got %d", warnings)
	}
	if breakings == 0 {
		t.Error("removing an operation produced no breaking finding — the ERR path regressed")
	}
}

// TestReactivationRaisesNothing: a deprecation being LIFTED constrains nobody.
// CONTRACTS §4 does not report additive changes, and this is that rule's mirror
// image — a warning carrying good news costs the tier its meaning.
func TestReactivationRaisesNothing(t *testing.T) {
	deprecated := strings.Replace(baseDoc,
		"      operationId: createCharge\n",
		"      operationId: createCharge\n      deprecated: true\n", 1)
	reactivated := strings.Replace(deprecated, `version: "1.0.0"`, `version: "2.0.0"`, 1)
	reactivated = strings.Replace(reactivated, "      deprecated: true\n", "", 1)

	findings, err := DetectVersionDiffData([]byte(deprecated), []byte(reactivated), "acme-payments")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Rule, "reactivated") {
			t.Errorf("reactivation reported as %s/%s — it breaks nothing and must stay silent", f.Rule, f.Severity)
		}
	}
}

// TestDeprecationFamilyIsExactlyTheAnnouncements pins the promoted set against
// the ids oasdiff actually ships at the pinned version, so a dependency bump
// that renames or adds one is a test failure here rather than a silent gap in
// what the collector reports.
func TestDeprecationFamilyIsExactlyTheAnnouncements(t *testing.T) {
	want := map[string]bool{
		"endpoint-deprecated":                      true,
		"endpoint-deprecated-with-sunset":          true,
		"request-parameter-deprecated":             true,
		"request-property-deprecated":              true,
		"request-property-deprecated-with-sunset":  true,
		"response-property-deprecated":             true,
		"response-property-deprecated-with-sunset": true,
	}
	if len(deprecationAnnouncements) != len(want) {
		t.Fatalf("family has %d ids, want %d", len(deprecationAnnouncements), len(want))
	}
	for id := range want {
		if !deprecationAnnouncements[id] {
			t.Errorf("%s is not promoted", id)
		}
	}
	// The reactivations and the already-ERR sunset violations must stay out.
	for _, id := range []string{
		"endpoint-reactivated", "request-parameter-reactivated",
		"request-property-reactivated", "response-property-reactivated",
		"api-deprecated-sunset-missing", "api-sunset-date-too-small",
		"request-parameter-deprecated-sunset-missing",
		"response-property-deprecated-sunset-missing",
	} {
		if deprecationAnnouncements[id] {
			t.Errorf("%s must not be promoted to warning", id)
		}
	}
}
