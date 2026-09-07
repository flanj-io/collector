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

	// The peer-review case (sdk #24 session), as RULED with collector#44: an
	// application/problem+json body under a contract that declares
	// application/json for a DECLARED status is JUDGED — the +json suffix
	// resolves to the declared entry for the lookup and the body is held
	// against that schema. Here a problem document against the Charge schema:
	// drifted, with the schema violations as findings. Never not-validated.
	t.Run("a +json media type under an application/json declaration is judged", func(t *testing.T) {
		c := golden
		c.ResponseContentType = "application/problem+json"
		c.ResponseBody = `{"type":"about:blank","title":"Bad Gateway","status":502}`
		fs, v := JudgeLiveVsSpec(doc, c)
		if len(fs) == 0 || v.Verdict != model.ValidatedDrifted {
			t.Fatalf("findings = %d verdict = %+v, want schema findings and drifted (the body was judged against Charge)", len(fs), v)
		}
		if dfs, err := DetectLiveVsSpec(doc, c); err != nil || len(dfs) != len(fs) {
			t.Errorf("DetectLiveVsSpec = (%d, %v), want the same findings and no error", len(dfs), err)
		}
	})

	// A media type the contract declares under NO name, on a declared status,
	// is the provider breaching a promise: collector#44's content-type-mismatch
	// finding, and drifted through VerdictOf — media-type-undeclared is
	// unreachable here (it remains for a default-only status).
	t.Run("an undeclared media type on a declared status is a finding, drifted", func(t *testing.T) {
		c := golden
		c.ResponseContentType = "text/html; charset=utf-8"
		c.ResponseBody = `<html>declined</html>`
		fs, v := JudgeLiveVsSpec(doc, c)
		if len(fs) != 1 || fs[0].Rule != RuleContentTypeMismatch || v.Verdict != model.ValidatedDrifted {
			t.Fatalf("findings = %+v verdict = %+v, want one content-type-mismatch and drifted", fs, v)
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
