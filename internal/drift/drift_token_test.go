package drift

import (
	"strings"
	"testing"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/redact"
)

// The drift detector runs AFTER the redaction floor (privacy first — drift only ever
// sees the redacted body), so a spec constraint can "fail" simply because the floor
// replaced the value with a ⟦REDACTED:…⟧ token. "We redacted it" is not "the provider
// drifted": schema errors whose offending value carries a redaction token are SKIPPED
// (unknown, not violated). Skipping is one-directional — it can never mask drift on a
// value the floor did not touch — making it the drift-side counterpart of the floor's
// never-subtract law.
//
// The captured-value-properties enhancement (redaction.fields, CONTRACTS §2/§6)
// restores drift signal on WHOLE-VALUE redactions: when the call carries the
// original's props for the erroring path, the DECIDABLE constraints (type,
// min/maxLength) are judged against them; undecidable constraints (pattern/format/
// enum) and values with no matching record keep skipping.

// tokenSpec declares a charge whose card_number is a 16-digit string, amount an
// integer, email an RFC email, and note a short string — the constraint kinds
// redaction can break (pattern, type after PAN-as-number, format, length).
const tokenSpec = `
openapi: 3.0.3
info: { title: t, version: "1.0.0" }
paths:
  /v1/charges:
    post:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  card_number: { type: string, pattern: "^[0-9]{16}$" }
                  amount: { type: integer }
                  email: { type: string, pattern: "^[^@]+@[^@]+$" }
                  status: { type: string }
                  note: { type: string, maxLength: 10 }
`

func tokenCall(responseBody string) model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion: 1,
		Integration:   "acme-payments",
		Method:        "POST",
		URL:           "http://api.acme.test/v1/charges",
		Route:         "/v1/charges",
		StatusCode:    200,
		ResponseBody:  responseBody,
	}
}

func detect(t *testing.T, body string) []model.Finding {
	t.Helper()
	return detectWithFields(t, body, nil)
}

// detectWithFields runs the detector over a call carrying redaction.fields records.
func detectWithFields(t *testing.T, body string, fields []model.RedactedFieldRecord) []model.Finding {
	t.Helper()
	doc, err := LoadSpecData([]byte(tokenSpec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	call := tokenCall(body)
	call.Redaction.Fields = fields
	findings, err := DetectLiveVsSpec(doc, call)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	return findings
}

// responseField builds one response-part fields record.
func responseField(path, pattern string, props redact.ValueProps) model.RedactedFieldRecord {
	return model.RedactedFieldRecord{
		Part:          "response",
		RedactedField: redact.RedactedField{Path: path, Pattern: pattern, Props: props},
	}
}

// stringProps are captured props of an original string of n code points.
func stringProps(n int) redact.ValueProps {
	return redact.ValueProps{
		Type:                        "string",
		Length:                      n,
		ContainsDigits:              true,
		ContainsASCIIPrintableChars: true,
	}
}

// integerProps are captured props of an original integer literal of n digits.
func integerProps(n int) redact.ValueProps {
	yes := true
	return redact.ValueProps{
		Type:                        "number",
		Length:                      n,
		Integer:                     &yes,
		ContainsDigits:              true,
		ContainsASCIIPrintableChars: true,
	}
}

// A fully-redacted sensitive field violates its pattern/format only because of the
// token — no finding may be produced for it.
func TestRedactedValuesDoNotDrift(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","email":"⟦REDACTED:EMAIL⟧","amount":1200,"status":"ok"}`
	if findings := detect(t, body); len(findings) != 0 {
		t.Fatalf("redacted values produced findings: %+v", findings)
	}
}

// A PAN sent as a JSON number is redacted to a STRING token; the resulting
// integer-vs-string mismatch is redaction's doing, not the provider's.
func TestRedactedPanAsNumberDoesNotDriftOnType(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":"⟦REDACTED:PAN⟧","status":"ok"}`
	if findings := detect(t, body); len(findings) != 0 {
		t.Fatalf("redacted PAN-as-number produced findings: %+v", findings)
	}
}

// Skipping token-carrying values must never mask GENUINE drift on untouched values:
// the drifting amount (string "1200", not a token) still yields exactly its finding.
func TestGenuineDriftStillFiresNextToTokens(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":"1200","status":"ok"}`
	findings := detect(t, body)
	if len(findings) != 1 {
		t.Fatalf("want exactly the amount finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.FieldPath == nil || *f.FieldPath != "amount" || f.Rule != "type-mismatch" {
		t.Errorf("wrong finding: %+v", f)
	}
	if !strings.Contains(f.Actual, "string") {
		t.Errorf("actual should describe the string: %q", f.Actual)
	}
}

// A token embedded INSIDE surrounding text (span redaction) also skips — the token
// bytes corrupt any length/pattern judgement of the host string.
func TestTokenInsideTextSkips(t *testing.T) {
	body := `{"card_number":"prefix ⟦REDACTED:PAN⟧ suffix","amount":7,"status":"ok"}`
	if findings := detect(t, body); len(findings) != 0 {
		t.Fatalf("token-carrying text produced findings: %+v", findings)
	}
}

// (a) A PATTERN constraint stays undecidable even WITH a fields record: the props
// deliberately capture nothing that could re-judge a pattern (non-reversible by
// design), so the redacted card_number still never drifts on its pattern.
func TestPatternStaysUndecidableWithFieldsRecord(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":7,"status":"ok"}`
	fields := []model.RedactedFieldRecord{responseField("/card_number", "PAN", stringProps(16))}
	if findings := detectWithFields(t, body, fields); len(findings) != 0 {
		t.Fatalf("pattern on a redacted value produced findings: %+v", findings)
	}
}

// (b) Spec wants integer; the props prove the ORIGINAL was an integer number — the
// type error was the PAN-as-number rewrite's doing, not the provider's. Skip.
func TestTypeSatisfiedByPropsSkips(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":"⟦REDACTED:PAN⟧","status":"ok"}`
	fields := []model.RedactedFieldRecord{
		responseField("/card_number", "PAN", stringProps(16)),
		responseField("/amount", "PAN", integerProps(16)),
	}
	if findings := detectWithFields(t, body, fields); len(findings) != 0 {
		t.Fatalf("props-satisfied type produced findings: %+v", findings)
	}
}

// (c) Spec wants integer; the props prove the ORIGINAL was a STRING — a real
// type-mismatch, reported with the props-built actual (the value is gone by design).
func TestTypeViolatedByPropsFires(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":"⟦REDACTED:PAN⟧","status":"ok"}`
	fields := []model.RedactedFieldRecord{
		responseField("/card_number", "PAN", stringProps(16)),
		responseField("/amount", "PAN", stringProps(16)),
	}
	findings := detectWithFields(t, body, fields)
	if len(findings) != 1 {
		t.Fatalf("want exactly the amount finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.FieldPath == nil || *f.FieldPath != "amount" || f.Rule != "type-mismatch" {
		t.Errorf("wrong finding: %+v", f)
	}
	if want := "type=string (redacted; length=16)"; f.Actual != want {
		t.Errorf("actual = %q, want %q", f.Actual, want)
	}
}

// (d) maxLength is decidable from the captured length: an original of 25 code points
// violates maxLength 10 (finding, with the length-based actual); one of 8 satisfied
// it — the token alone broke the constraint — so it skips.
func TestMaxLengthJudgedFromProps(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":7,"status":"ok","note":"⟦REDACTED:EMAIL⟧"}`
	cardField := responseField("/card_number", "PAN", stringProps(16))

	long := []model.RedactedFieldRecord{cardField, responseField("/note", "EMAIL", stringProps(25))}
	findings := detectWithFields(t, body, long)
	if len(findings) != 1 {
		t.Fatalf("want exactly the note finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.FieldPath == nil || *f.FieldPath != "note" || f.Rule != "maxLength-mismatch" {
		t.Errorf("wrong finding: %+v", f)
	}
	if want := "length=25 (redacted)"; f.Actual != want {
		t.Errorf("actual = %q, want %q", f.Actual, want)
	}

	short := []model.RedactedFieldRecord{cardField, responseField("/note", "EMAIL", stringProps(8))}
	if findings := detectWithFields(t, body, short); len(findings) != 0 {
		t.Fatalf("props-satisfied maxLength produced findings: %+v", findings)
	}
}

// (e) A token value with NO matching fields record falls back to the plain skip —
// which also covers older SDKs in the compatibility window that emit no fields.
func TestTokenWithoutMatchingRecordStillSkips(t *testing.T) {
	body := `{"card_number":"⟦REDACTED:PAN⟧","amount":"⟦REDACTED:PAN⟧","status":"ok"}`
	// A record exists, but for a different path and for the request part.
	fields := []model.RedactedFieldRecord{
		{Part: "request", RedactedField: redact.RedactedField{Path: "/amount", Pattern: "PAN", Props: stringProps(16)}},
		responseField("/other", "PAN", stringProps(16)),
	}
	if findings := detectWithFields(t, body, fields); len(findings) != 0 {
		t.Fatalf("unmatched token values produced findings: %+v", findings)
	}
}
