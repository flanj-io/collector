package drift

import (
	"errors"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// The response media type is the first thing the contract lookup sees, and
// kin-openapi resolves it verbatim, then `type/*`, then `*/*` — never by its
// RFC 6839 structured suffix. So `application/problem+json` (RFC 7807, the
// standard error payload) used to miss a contract that declares
// `application/json` for that status, and the miss was swallowed: no finding,
// no verdict, a call that read clean. These pin the three outcomes that replace
// that silence — the suffix resolves to its base for the LOOKUP and the body
// still validates as JSON; a media type declared under no name is a finding;
// everything else kin-openapi refuses to judge is an explicit NotValidatedError.

const contentTypeSpec = `
openapi: 3.0.3
info: { title: t, version: "1.0.0" }
components:
  schemas:
    Problem:
      type: object
      required: [type, title, status]
      properties:
        type: { type: string }
        title: { type: string }
        status: { type: integer }
        detail: { type: string }
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
                  amount: { type: integer }
        "422":
          description: rejected
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Problem" }
  /v1/refunds:
    post:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object }
        "4XX":
          description: rejected, declared as a range
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Problem" }
  /v1/legacy:
    post:
      responses:
        "422":
          description: rejected, declared under the suffixed name AND the base
          content:
            application/problem+json:
              schema:
                type: object
                required: [code]
                properties:
                  code: { type: integer }
            application/json:
              schema: { $ref: "#/components/schemas/Problem" }
  /v1/vendor:
    post:
      responses:
        "200":
          description: ok, declared only under a vendor +json name
          content:
            application/vnd.acme.v2+json:
              schema:
                type: object
                properties:
                  amount: { type: integer }
`

const driftingProblem = `{"type":"https://api.acme.test/errors/card-declined","title":"Card declined","status":"422"}`
const conformingProblem = `{"type":"https://api.acme.test/errors/card-declined","title":"Card declined","status":422}`

func contentTypeCall(route string, status int, contentType, body string) model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion:       1,
		ID:                  "call_ct_1",
		Integration:         "acme-payments",
		Method:              "POST",
		URL:                 "http://api.acme.test" + route,
		Route:               route,
		StatusCode:          status,
		ResponseContentType: contentType,
		ResponseBody:        body,
	}
}

func detectContentType(t *testing.T, call model.RedactedCall) ([]model.Finding, error) {
	t.Helper()
	doc, err := LoadSpecData([]byte(contentTypeSpec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return DetectLiveVsSpec(doc, call)
}

func onlyFinding(t *testing.T, call model.RedactedCall) model.Finding {
	t.Helper()
	findings, err := detectContentType(t, call)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly one finding, got %d: %+v", len(findings), findings)
	}
	return findings[0]
}

func TestLiveVsSpec_ProblemJSONValidatesAsJSONAgainstTheBaseType(t *testing.T) {
	call := contentTypeCall("/v1/charges", 422, "application/problem+json; charset=utf-8", driftingProblem)

	f := onlyFinding(t, call)

	if f.Rule != "type-mismatch" || f.Location == nil || *f.Location != "$.response.body.status" {
		t.Fatalf("the problem document was not held against the 422 schema: %+v", f)
	}
	if f.Expected != "type=integer" || f.Actual != `type=string ("422")` {
		t.Fatalf("expected/actual: %q / %q", f.Expected, f.Actual)
	}
	if f.Endpoint != "POST /v1/charges" || f.Severity != model.SeverityBreaking {
		t.Fatalf("endpoint/severity: %q / %q", f.Endpoint, f.Severity)
	}
}

func TestLiveVsSpec_ConformingProblemJSONIsClean(t *testing.T) {
	call := contentTypeCall("/v1/charges", 422, "application/problem+json; charset=utf-8", conformingProblem)

	findings, err := detectContentType(t, call)

	if err != nil || len(findings) != 0 {
		t.Fatalf("want clean, got findings=%+v err=%v", findings, err)
	}
}

func TestLiveVsSpec_EveryStructuredSuffixResolvesToJSON(t *testing.T) {
	for _, ct := range []string{
		"application/vnd.api+json",
		"application/hal+json",
		"application/ld+json",
		"application/merge-patch+json",
		"application/vnd.acme.v3+json; charset=utf-8",
		"Application/Problem+JSON",
	} {
		t.Run(ct, func(t *testing.T) {
			f := onlyFinding(t, contentTypeCall("/v1/charges", 422, ct, driftingProblem))
			if f.Rule != "type-mismatch" {
				t.Fatalf("%s: want type-mismatch on status, got %+v", ct, f)
			}
		})
	}
}

func TestLiveVsSpec_RangeStatusKeyResolvesTheSuffixToo(t *testing.T) {
	f := onlyFinding(t, contentTypeCall("/v1/refunds", 422, "application/problem+json", driftingProblem))
	if f.Rule != "type-mismatch" || f.Endpoint != "POST /v1/refunds" {
		t.Fatalf("4XX-declared problem not judged: %+v", f)
	}
}

func TestLiveVsSpec_ADeclaredSuffixedNameWinsOverItsBase(t *testing.T) {
	// /v1/legacy declares application/problem+json (requires `code`) beside
	// application/json (the Problem shape). The wire name is declared verbatim,
	// so its own schema judges the body — the base is a fallback, not a rewrite.
	body := `{"type":"https://acme.test/e","title":"Card declined","status":422}`
	f := onlyFinding(t, contentTypeCall("/v1/legacy", 422, "application/problem+json", body))
	if f.Rule != "missing-required" || !strings.Contains(f.Detail, "code") {
		t.Fatalf("want the suffixed name's own schema (missing code), got %+v", f)
	}
}

func TestLiveVsSpec_AVendorSuffixDeclaredVerbatimDecodesAsJSON(t *testing.T) {
	// kin-openapi ships decoders for the common +json names only; a vendor
	// name the contract spells out must not fail as "unsupported content type"
	// after its lookup succeeded.
	f := onlyFinding(t, contentTypeCall("/v1/vendor", 200, "application/vnd.acme.v2+json; charset=utf-8", `{"amount":"1200"}`))
	if f.Rule != "type-mismatch" || f.Location == nil || *f.Location != "$.response.body.amount" {
		t.Fatalf("vendor +json body not judged: %+v", f)
	}
}

func TestLiveVsSpec_AnUndeclaredMediaTypeIsAFinding(t *testing.T) {
	call := contentTypeCall("/v1/charges", 422, "text/html; charset=utf-8", "<html>declined</html>")

	f := onlyFinding(t, call)

	if f.Rule != RuleContentTypeMismatch || f.Kind != model.KindLiveVsSpec || f.Severity != model.SeverityBreaking {
		t.Fatalf("rule/kind/severity: %+v", f)
	}
	if f.Location == nil || *f.Location != "$.response.headers.content-type" {
		t.Fatalf("location: %v", f.Location)
	}
	if f.Expected != "content-type=application/json" || f.Actual != "content-type=text/html; charset=utf-8" {
		t.Fatalf("expected/actual: %q / %q", f.Expected, f.Actual)
	}
	if !strings.Contains(f.Detail, "text/html") || !strings.Contains(f.Detail, "422") || !strings.Contains(f.Detail, "application/json") {
		t.Fatalf("detail names neither the answer nor the promise: %q", f.Detail)
	}
	if f.SourceCallID == nil || *f.SourceCallID != "call_ct_1" {
		t.Fatalf("source call: %v", f.SourceCallID)
	}
	if f.Signature != "acme-payments|POST /v1/charges|live-vs-spec|content-type-mismatch|" {
		t.Fatalf("signature: %q", f.Signature)
	}
}

func TestLiveVsSpec_UndeclaredMediaTypesDedupPerEndpoint(t *testing.T) {
	html := onlyFinding(t, contentTypeCall("/v1/charges", 422, "text/html", "<html>"))
	xml := onlyFinding(t, contentTypeCall("/v1/charges", 422, "application/xml", "<e/>"))
	if html.Signature != xml.Signature {
		t.Fatalf("one drift per endpoint: %q vs %q", html.Signature, xml.Signature)
	}
}

func TestLiveVsSpec_BinaryWhereJSONIsDeclaredIsAFinding(t *testing.T) {
	// The SDK captures no body for octet-stream; the header alone is the evidence.
	f := onlyFinding(t, contentTypeCall("/v1/charges", 200, "application/octet-stream", ""))
	if f.Rule != RuleContentTypeMismatch || f.Actual != "content-type=application/octet-stream" {
		t.Fatalf("octet-stream on a JSON endpoint: %+v", f)
	}
}

func TestLiveVsSpec_UnknownSuffixIsNotJSON(t *testing.T) {
	// +xml is not mapped (the default gate carries no XML base); a +xml answer
	// on a JSON endpoint is an undeclared media type, not a decode attempt.
	f := onlyFinding(t, contentTypeCall("/v1/charges", 422, "application/soap+xml", "<e/>"))
	if f.Rule != RuleContentTypeMismatch {
		t.Fatalf("+xml treated as JSON: %+v", f)
	}
}

func TestLiveVsSpec_AnUndeclaredStatusIsNotValidated(t *testing.T) {
	// /v1/charges declares 200 and 422 and no default: a 500 has nothing to be
	// held against — say so instead of returning clean.
	findings, err := detectContentType(t, contentTypeCall("/v1/charges", 500, "application/json", `{"error":"boom"}`))

	var nv *NotValidatedError
	if !errors.As(err, &nv) || nv.Reason != NotValidatedStatusNotDeclared {
		t.Fatalf("want NotValidatedError(%s), got findings=%+v err=%v", NotValidatedStatusNotDeclared, findings, err)
	}
	if !strings.Contains(nv.Detail, "500") || !strings.Contains(nv.Detail, "POST /v1/charges") {
		t.Fatalf("detail: %q", nv.Detail)
	}
}

func TestLiveVsSpec_ABodyTheValidatorCannotReadIsNotValidated(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		truncated bool
		reason    string
	}{
		{"empty body under a declared schema", "", false, NotValidatedBodyEmpty},
		{"body cut at the capture cap", `{"type":"https://acme.test/e","ti`, true, NotValidatedBodyTruncated},
		{"body that is not JSON", "declined", false, NotValidatedBodyNotJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := contentTypeCall("/v1/charges", 422, "application/problem+json", tc.body)
			call.ResponseBodyTruncated = tc.truncated

			findings, err := detectContentType(t, call)

			var nv *NotValidatedError
			if !errors.As(err, &nv) || nv.Reason != tc.reason {
				t.Fatalf("want NotValidatedError(%s), got findings=%+v err=%v", tc.reason, findings, err)
			}
			if len(findings) != 0 {
				t.Fatalf("an unjudged call carries no findings: %+v", findings)
			}
		})
	}
}

func TestLiveVsSpec_RedirectsStayUnjudged(t *testing.T) {
	// kin-openapi never looks at 301/304/307/308 bodies; neither does the
	// media-type check, or a Location-only redirect would read as drift.
	findings, err := detectContentType(t, contentTypeCall("/v1/charges", 304, "text/html", ""))
	if err != nil || len(findings) != 0 {
		t.Fatalf("304 judged: findings=%+v err=%v", findings, err)
	}
}

func TestLiveVsSpec_MissingContentTypeStillDefaultsToJSON(t *testing.T) {
	f := onlyFinding(t, contentTypeCall("/v1/charges", 200, "", `{"amount":"1200"}`))
	if f.Rule != "type-mismatch" {
		t.Fatalf("no header, JSON assumed: %+v", f)
	}
}

func TestStructuredBase(t *testing.T) {
	cases := map[string]string{
		"application/problem+json":                    "application/json",
		"application/vnd.acme.v2+json; charset=utf-8": "application/json",
		"Application/HAL+JSON":                        "application/json",
		"application/json":                            "",
		"image/svg+xml":                               "",
		"application/x-ndjson":                        "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := structuredBase(in); got != want {
			t.Errorf("structuredBase(%q) = %q, want %q", in, got, want)
		}
	}
}
