package drift

import (
	"strings"
	"testing"

	"github.com/vinifera-io/collector/internal/model"
)

// The drift detector runs AFTER the redaction floor (privacy first — drift only ever
// sees the redacted body), so a spec constraint can "fail" simply because the floor
// replaced the value with a ⟦REDACTED:…⟧ token. "We redacted it" is not "the provider
// drifted": schema errors whose offending value carries a redaction token are SKIPPED
// (unknown, not violated). Skipping is one-directional — it can never mask drift on a
// value the floor did not touch — making it the drift-side counterpart of the floor's
// never-subtract law.

// tokenSpec declares a charge whose card_number is a 16-digit string, amount an
// integer, and email an RFC email — the three constraint kinds redaction can break
// (pattern, type after PAN-as-number, format).
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
	doc, err := LoadSpecData([]byte(tokenSpec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	findings, err := DetectLiveVsSpec(doc, tokenCall(body))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	return findings
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
