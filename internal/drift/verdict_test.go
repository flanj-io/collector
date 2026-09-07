package drift

import (
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/flanj-io/collector/internal/model"
)

// The per-call VERDICT for REST (JudgeLiveVsSpec): clean or drifted when the
// response was compared to a schema, and the first gate that stopped it
// otherwise. The cases that matter are the ones kin-openapi refuses WITHOUT a
// SchemaError — an undeclared status or media type, a body that will not
// decode — because the finding path drops those and "no finding" used to read
// as clean.

func specV1Doc(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := LoadSpecFile(filepath.Join(contractsDir(), "spec-v1.yaml"))
	if err != nil {
		t.Fatalf("load spec-v1: %v", err)
	}
	return doc
}

const conformingCharge = `{"id":"ch_1Mox","object":"charge","amount":1200,"currency":"usd","status":"succeeded","created":1755504000,"card":{"last4":"1111","brand":"visa"}}`

func TestJudgeLiveVsSpec_Verdicts(t *testing.T) {
	doc := specV1Doc(t)
	golden := loadGoldenCall(t)

	t.Run("drifted: the golden call", func(t *testing.T) {
		fs, v := JudgeLiveVsSpec(doc, golden)
		if len(fs) != 1 || fs[0].Kind != model.KindLiveVsSpec {
			t.Fatalf("findings = %+v, want the golden live-vs-spec finding", fs)
		}
		if v.Verdict != model.ValidatedDrifted || v.Reason != "" {
			t.Errorf("verdict = %+v, want drifted", v)
		}
	})

	t.Run("clean: the same call with a conforming body", func(t *testing.T) {
		c := golden
		c.ResponseBody = conformingCharge
		fs, v := JudgeLiveVsSpec(doc, c)
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none", fs)
		}
		if v.Verdict != model.ValidatedClean || v.Reason != "" {
			t.Errorf("verdict = %+v, want clean", v)
		}
	})

	t.Run("not routable", func(t *testing.T) {
		c := golden
		c.Route = "/v1/not-in-the-document"
		c.URL = "https://api.acme.test/v1/not-in-the-document"
		fs, v := JudgeLiveVsSpec(doc, c)
		if len(fs) != 0 || v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedNotRoutable {
			t.Errorf("findings = %+v verdict = %+v, want not-validated / not-routable", fs, v)
		}
		if _, err := DetectLiveVsSpec(doc, c); err == nil {
			t.Errorf("DetectLiveVsSpec must still surface the route miss as an error")
		}
	})

	// The peer-review case (sdk #24 session): an application/problem+json body
	// under a contract that declares application/json. kin-openapi returns a
	// ResponseError with no SchemaError; collectSchemaErrors yields nothing;
	// DetectLiveVsSpec returns (nil, nil) — and that "no finding" was CLEAN.
	t.Run("undeclared media type is NOT clean", func(t *testing.T) {
		c := golden
		c.ResponseContentType = "application/problem+json"
		c.ResponseBody = `{"type":"about:blank","title":"Bad Gateway","status":502}`
		fs, v := JudgeLiveVsSpec(doc, c)
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none (the detector reports schema violations only)", fs)
		}
		if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedMediaTypeUndeclared {
			t.Errorf("verdict = %+v, want not-validated / media-type-undeclared (200 is declared, problem+json is not)", v)
		}
		// The old reading, pinned: no error, no finding — which is exactly why
		// the processor stamps off the verdict and not off this pair.
		if fs, err := DetectLiveVsSpec(doc, c); err != nil || len(fs) != 0 {
			t.Errorf("DetectLiveVsSpec = (%+v, %v), want (none, nil) for an undeclared media type", fs, err)
		}
	})

	t.Run("undeclared status is NOT clean, and is its own reason", func(t *testing.T) {
		// The operator's fix differs from the media-type case — declare the
		// status — so the two must not share a word.
		c := golden
		c.StatusCode = 502
		c.ResponseBody = conformingCharge
		_, v := JudgeLiveVsSpec(doc, c)
		if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedStatusUndeclared {
			t.Errorf("verdict = %+v, want not-validated / status-undeclared (spec-v1 declares 200 only)", v)
		}
		// Undeclared status AND undeclared media type: the status is named,
		// because that is the first thing the validator refuses.
		c.ResponseContentType = "application/problem+json"
		_, v = JudgeLiveVsSpec(doc, c)
		if v.Reason != model.NotValidatedStatusUndeclared {
			t.Errorf("verdict = %+v, want status-undeclared when both are undeclared", v)
		}
	})

	t.Run("a body that will not decode is NOT clean", func(t *testing.T) {
		c := golden
		c.ResponseBody = `<html>upstream error</html>`
		_, v := JudgeLiveVsSpec(doc, c)
		if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedBodyNotDecodable {
			t.Errorf("verdict = %+v, want not-validated / body-not-decodable", v)
		}
	})
}

// TestJudgeLiveVsSpec_DefaultOnlyStatus pins the split with flanj-io/collector#44
// (content-type-mismatch): a status declared ONLY via `default`, answered with
// an undeclared media type, is not-validated / media-type-undeclared here — a
// catch-all response is not evidence that the provider breached anything, so a
// gateway's `502 text/html` under a JSON `default` never raises a breaking
// finding — while the same response under a contract with no `default` is
// status-undeclared. A status declared by exact code or NXX range with an
// undeclared media type is #44's finding and is synthesized before this
// classifier runs; #44's gate excludes `default` on purpose, and this is the
// case that falls through it.
func TestJudgeLiveVsSpec_DefaultOnlyStatus(t *testing.T) {
	gateway := loadGoldenCall(t)
	gateway.StatusCode = 502
	gateway.ResponseContentType = "text/html"
	gateway.ResponseBody = "<html>Bad Gateway</html>"

	// spec-v1 declares 200 only: the status itself is undeclared.
	_, v := JudgeLiveVsSpec(specV1Doc(t), gateway)
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedStatusUndeclared {
		t.Errorf("no default: verdict = %+v, want not-validated / status-undeclared", v)
	}

	// The same document with a JSON `default` response: the status is now
	// declared (via the catch-all), the media type is not.
	withDefault := specV1Doc(t)
	desc := "any other response"
	withDefault.Paths.Find("/v1/charges").Post.Responses.Set("default", &openapi3.ResponseRef{Value: &openapi3.Response{
		Description: &desc,
		Content:     openapi3.NewContentWithJSONSchema(openapi3.NewObjectSchema()),
	}})
	fs, v := JudgeLiveVsSpec(withDefault, gateway)
	if len(fs) != 0 {
		t.Fatalf("default-only 502 text/html produced findings: %+v — a catch-all is not evidence of a breach", fs)
	}
	if v.Verdict != model.ValidatedNot || v.Reason != model.NotValidatedMediaTypeUndeclared {
		t.Errorf("default-only: verdict = %+v, want not-validated / media-type-undeclared", v)
	}

	// A 502 problem+json under that JSON default reaches the same branch today.
	// #44's RFC 6839 lookup validates it against the default's schema BEFORE
	// this point; pinning the current answer makes that rebase a deliberate
	// change to this line, not a silent one.
	problem := gateway
	problem.ResponseContentType = "application/problem+json"
	problem.ResponseBody = `{"type":"about:blank","title":"Bad Gateway","status":502}`
	_, v = JudgeLiveVsSpec(withDefault, problem)
	if v.Reason != model.NotValidatedMediaTypeUndeclared {
		t.Errorf("default-only problem+json (pre-#44): verdict = %+v, want media-type-undeclared", v)
	}
}

// TestUnjudgedReason_ClassifiesByShape pins the mapping from kin-openapi's
// refusal shapes to reasons, so a validator upgrade that changes a message
// cannot silently move a case between them.
func TestUnjudgedReason_ClassifiesByShape(t *testing.T) {
	if got := unjudgedReason(errNoFindingShape{}, nil, 200); got != model.NotValidatedValidatorError {
		t.Errorf("unknown error shape = %q, want validator-error", got)
	}
	if got := unjudgedReason(&openapi3filter.ResponseError{Reason: "x", Err: errNoFindingShape{}}, nil, 200); got != model.NotValidatedBodyNotDecodable {
		t.Errorf("ResponseError with an inner error = %q, want body-not-decodable", got)
	}
	if got := unjudgedReason(&openapi3filter.ResponseError{Reason: "status is not supported"}, nil, 200); got != model.NotValidatedStatusUndeclared {
		t.Errorf("ResponseError with no route declaring the status = %q, want status-undeclared", got)
	}
}

type errNoFindingShape struct{}

func (errNoFindingShape) Error() string { return "something the collector does not classify" }
