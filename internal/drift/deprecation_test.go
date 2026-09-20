package drift

import (
	"strings"
	"testing"
	"time"

	"github.com/flanj-io/collector/internal/model"
)

// The live-vs-spec deprecation path: a call that USED a deprecated surface.
//
// The load-bearing claim of every test here is that this is about the org's own
// traffic. A contract may deprecate anything it likes; the collector speaks only
// when a real call touched it.

// depSpec declares one deprecated operation, one live operation with a
// deprecated query parameter and a deprecated response field, and one operation
// with nothing deprecated at all.
const depSpec = `
openapi: 3.0.3
info: {title: Acme, version: "1.0.0"}
servers:
  - url: http://api.acme.test
paths:
  /v1/legacy-charges:
    post:
      operationId: createLegacyCharge
      deprecated: true
      x-sunset: "2027-03-01"
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: {type: string}
  /v1/charges:
    post:
      operationId: createCharge
      parameters:
        - name: legacy_mode
          in: query
          deprecated: true
          schema: {type: string}
        - name: trace_id
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
                  legacy_ref: {type: string, deprecated: true}
                  amount: {type: integer}
  /v1/quiet:
    post:
      operationId: quiet
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: {type: string}
`

func judgeDep(t *testing.T, call model.RedactedCall) ([]model.Finding, model.Validation) {
	t.Helper()
	doc, err := LoadSpecData([]byte(depSpec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	fs, v, err := JudgeLiveVsSpec(doc, call)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	return fs, v
}

func depCall(url, route, body string) model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion:       1,
		ID:                  "call_dep_1",
		Integration:         "acme-payments",
		Method:              "POST",
		URL:                 url,
		Route:               route,
		StatusCode:          200,
		ResponseContentType: "application/json",
		ResponseBody:        body,
	}
}

func onlyRule(t *testing.T, fs []model.Finding, rule string) model.Finding {
	t.Helper()
	var hits []model.Finding
	for _, f := range fs {
		if f.Rule == rule {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		var got []string
		for _, f := range fs {
			got = append(got, f.Rule+"/"+f.Severity)
		}
		t.Fatalf("want exactly one %q finding, got %d; all findings: %v", rule, len(hits), got)
	}
	return hits[0]
}

// TestCallingADeprecatedOperationRaisesOneWarning — and leaves the call CLEAN.
// The response conformed; the operation is still declared. Marking the call
// drifted would paint conforming traffic red.
func TestCallingADeprecatedOperationRaisesOneWarning(t *testing.T) {
	fs, verdict := judgeDep(t, depCall(
		"http://api.acme.test/v1/legacy-charges", "/v1/legacy-charges", `{"id":"ch_1"}`))

	if len(fs) != 1 {
		var got []string
		for _, f := range fs {
			got = append(got, f.Rule+"/"+f.Severity)
		}
		t.Fatalf("want exactly one finding, got %d: %v", len(fs), got)
	}
	f := fs[0]
	if f.Rule != RuleDeprecatedOperation {
		t.Errorf("rule = %q, want %q", f.Rule, RuleDeprecatedOperation)
	}
	if f.Kind != model.KindDeprecation {
		t.Errorf("kind = %q, want %q — its own kind, not a warning-severity live-vs-spec", f.Kind, model.KindDeprecation)
	}
	if f.Severity != model.SeverityWarning {
		t.Errorf("severity = %q, want warning — nothing has broken yet", f.Severity)
	}
	if f.SourceCallID == nil || *f.SourceCallID != "call_dep_1" {
		t.Error("the call must be pinned as evidence, like every other live finding")
	}
	if !strings.Contains(f.Actual, "2027-03-01") || !strings.Contains(f.Detail, "2027-03-01") {
		t.Errorf("the sunset date is the actionable half and must be stated: actual=%q detail=%q", f.Actual, f.Detail)
	}
	if !f.Flaggable() {
		t.Error("a deprecation must be flaggable — asking when it sunsets is what a thread is for")
	}

	// The whole point of the severity: the CALL is clean.
	if verdict.Verdict != model.ValidatedClean {
		t.Errorf("verdict = %q, want clean — a deprecated-but-conforming call did not drift", verdict.Verdict)
	}
	if model.MarksCallDrifted(f.Kind) {
		t.Error("a deprecation must not mark its call drifted — its kind is not a per-call drift kind")
	}
}

// TestNotCallingTheDeprecatedOperationRaisesNothing is the other half of the
// same claim: the contract deprecates an operation, and traffic that never
// touches it produces silence.
func TestNotCallingTheDeprecatedOperationRaisesNothing(t *testing.T) {
	fs, verdict := judgeDep(t, depCall(
		"http://api.acme.test/v1/quiet", "/v1/quiet", `{"id":"q_1"}`))

	if len(fs) != 0 {
		var got []string
		for _, f := range fs {
			got = append(got, f.Rule+"/"+f.Severity)
		}
		t.Errorf("want no findings for traffic that touches nothing deprecated; got %v", got)
	}
	if verdict.Verdict != model.ValidatedClean {
		t.Errorf("verdict = %q, want clean", verdict.Verdict)
	}
}

// TestDeprecatedParameterOnlyWhenSent: the parameter is deprecated either way;
// only a call that SENDS it has anything to change.
func TestDeprecatedParameterOnlyWhenSent(t *testing.T) {
	sent, _ := judgeDep(t, depCall(
		"http://api.acme.test/v1/charges?legacy_mode=on&trace_id=t1", "/v1/charges", `{"id":"ch_1","amount":1}`))
	f := onlyRule(t, sent, RuleDeprecatedParameter)
	if f.FieldPath == nil || *f.FieldPath != "legacy_mode" {
		t.Errorf("field_path = %v, want legacy_mode", f.FieldPath)
	}
	if f.Severity != model.SeverityWarning {
		t.Errorf("severity = %q, want warning", f.Severity)
	}

	omitted, _ := judgeDep(t, depCall(
		"http://api.acme.test/v1/charges?trace_id=t1", "/v1/charges", `{"id":"ch_1","amount":1}`))
	for _, f := range omitted {
		if f.Rule == RuleDeprecatedParameter {
			t.Error("a deprecated parameter the call does not send must raise nothing")
		}
	}
}

// TestDeprecatedFieldOnlyWhenCarried: same rule, on the body. A deprecated
// response field the provider did not send is not this org's problem.
func TestDeprecatedFieldOnlyWhenCarried(t *testing.T) {
	carried, _ := judgeDep(t, depCall(
		"http://api.acme.test/v1/charges", "/v1/charges", `{"id":"ch_1","legacy_ref":"old","amount":1}`))
	f := onlyRule(t, carried, RuleDeprecatedField)
	if f.FieldPath == nil || *f.FieldPath != "legacy_ref" {
		t.Errorf("field_path = %v, want legacy_ref", f.FieldPath)
	}
	if f.Location == nil || *f.Location != "$.response.body.legacy_ref" {
		t.Errorf("location = %v, want $.response.body.legacy_ref", f.Location)
	}

	absent, _ := judgeDep(t, depCall(
		"http://api.acme.test/v1/charges", "/v1/charges", `{"id":"ch_1","amount":1}`))
	for _, f := range absent {
		if f.Rule == RuleDeprecatedField {
			t.Error("a deprecated field the response does not carry must raise nothing")
		}
	}
}

// TestDeprecationDedupsBySignature: two calls to the same deprecated operation
// are ONE drift. The store collapses on signature, so the signature must be
// identical across calls and distinct across rules.
func TestDeprecationDedupsBySignature(t *testing.T) {
	first, _ := judgeDep(t, depCall(
		"http://api.acme.test/v1/legacy-charges", "/v1/legacy-charges", `{"id":"ch_1"}`))
	second := depCall("http://api.acme.test/v1/legacy-charges", "/v1/legacy-charges", `{"id":"ch_2"}`)
	second.ID = "call_dep_2"
	again, _ := judgeDep(t, second)

	if first[0].Signature != again[0].Signature {
		t.Errorf("two calls to one deprecated operation produced different signatures (%q vs %q) — they would list twice instead of counting",
			first[0].Signature, again[0].Signature)
	}
	if first[0].ID == again[0].ID {
		t.Error("each detection is its own record; the ids must differ")
	}

	// Distinct rules on one endpoint must not collide.
	both, _ := judgeDep(t, depCall(
		"http://api.acme.test/v1/charges?legacy_mode=on", "/v1/charges", `{"id":"ch_1","legacy_ref":"old","amount":1}`))
	seen := map[string]bool{}
	for _, f := range both {
		if seen[f.Signature] {
			t.Errorf("signature collision on %q", f.Signature)
		}
		seen[f.Signature] = true
	}
}

// TestBreakingDriftStillDriftsAlongsideADeprecation: the warning must not
// soften the red. A response that violates the schema on a deprecated operation
// is both — a breaking finding AND a deprecation warning — and the call drifts.
func TestBreakingDriftStillDriftsAlongsideADeprecation(t *testing.T) {
	// amount is declared integer; the provider answers a string.
	fs, verdict := judgeDep(t, depCall(
		"http://api.acme.test/v1/charges?legacy_mode=on", "/v1/charges", `{"id":"ch_1","amount":"1200"}`))

	var warn, breaking int
	for _, f := range fs {
		switch f.Severity {
		case model.SeverityWarning:
			warn++
		case model.SeverityBreaking:
			breaking++
		}
	}
	if warn == 0 {
		t.Error("the deprecated parameter must still be reported next to a real violation")
	}
	if breaking == 0 {
		t.Fatal("the type mismatch must still be breaking")
	}
	if verdict.Verdict != model.ValidatedDrifted {
		t.Errorf("verdict = %q, want drifted — a breaking finding is present", verdict.Verdict)
	}
}

// TestDeprecationSurvivesAnUnjudgeableResponse: whether the body could be
// validated and whether the surface is going away are different questions. A
// response the validator refuses must not swallow the deprecation.
func TestDeprecationSurvivesAnUnjudgeableResponse(t *testing.T) {
	call := depCall("http://api.acme.test/v1/legacy-charges", "/v1/legacy-charges", `{"id":"ch_1"}`)
	call.StatusCode = 503 // a status the contract never declares
	fs, verdict := judgeDep(t, call)

	onlyRule(t, fs, RuleDeprecatedOperation)
	if verdict.Verdict != model.ValidatedNot {
		t.Errorf("verdict = %q, want not-validated — nothing was compared", verdict.Verdict)
	}
}

// TestSelfContractDeprecationIsTheSameFinding: an INBOUND call judged against
// the contract the org publishes reports a consumer still using a surface the
// org has deprecated. Same detector, same shape — the direction is the caller's
// to record, which is what keeps one code path serving both.
func TestSelfContractDeprecationIsTheSameFinding(t *testing.T) {
	// The host is the spec's own: a published self-contract is reached under
	// whatever name the org's consumers use, and the harness's real self-spec
	// declares no `servers` at all for that reason. What this test pins is the
	// direction and the key, not the routing.
	call := depCall("http://api.acme.test/v1/legacy-charges", "/v1/legacy-charges", `{"id":"ch_1"}`)
	call.Direction = "server"
	call.Integration = "org-app"
	fs, _ := judgeDep(t, call)

	f := onlyRule(t, fs, RuleDeprecatedOperation)
	if f.Severity != model.SeverityWarning {
		t.Errorf("severity = %q, want warning", f.Severity)
	}
	if f.Integration != "org-app" {
		t.Errorf("integration = %q — the finding carries the call's own key", f.Integration)
	}
}

// TestSchemaCycleTerminates: a contract is a stranger's document, and a
// self-referential schema is a legal shape. The walk must not follow it forever.
func TestSchemaCycleTerminates(t *testing.T) {
	const cyclic = `
openapi: 3.0.3
info: {title: T, version: "1.0.0"}
servers:
  - url: http://api.acme.test
paths:
  /v1/tree:
    post:
      operationId: tree
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Node"}
components:
  schemas:
    Node:
      type: object
      properties:
        old_id: {type: string, deprecated: true}
        child: {$ref: "#/components/schemas/Node"}
`
	doc, err := LoadSpecData([]byte(cyclic))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	call := depCall("http://api.acme.test/v1/tree", "/v1/tree",
		`{"old_id":"a","child":{"old_id":"b","child":{"old_id":"c"}}}`)

	done := make(chan []model.Finding, 1)
	go func() {
		fs, _, _ := JudgeLiveVsSpec(doc, call)
		done <- fs
	}()
	select {
	case fs := <-done:
		// One finding for the field, however deep the body nests it: the
		// finding is about the declared property, not each occurrence.
		onlyRule(t, fs, RuleDeprecatedField)
	case <-time.After(10 * time.Second):
		t.Fatal("the schema walk did not terminate on a self-referential document")
	}
}

// TestDeprecatedFieldsAcrossArrayElements: a deprecated field is usually
// OPTIONAL, so the first element of a list is no guarantee of what the rest
// carry. Every element is scanned (to a cap), and one field is still ONE
// finding however many rows carry it.
func TestDeprecatedFieldsAcrossArrayElements(t *testing.T) {
	const listSpec = `
openapi: 3.0.3
info: {title: T, version: "1.0.0"}
servers:
  - url: http://api.acme.test
paths:
  /v1/list:
    post:
      operationId: list
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  items:
                    type: array
                    items:
                      type: object
                      properties:
                        id: {type: string}
                        old_a: {type: string, deprecated: true}
                        old_b: {type: string, deprecated: true}
`
	doc, err := LoadSpecData([]byte(listSpec))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// old_a appears only in the first row, old_b only in the second, and old_a
	// again in the third — so a walk that stopped at the first matching element
	// would miss old_b, and one that did not dedup would report old_a twice.
	call := depCall("http://api.acme.test/v1/list", "/v1/list",
		`{"items":[{"id":"1","old_a":"x"},{"id":"2","old_b":"y"},{"id":"3","old_a":"z"}]}`)
	fs, _, err := JudgeLiveVsSpec(doc, call)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}

	got := map[string]int{}
	for _, f := range fs {
		if f.Rule == RuleDeprecatedField && f.FieldPath != nil {
			got[*f.FieldPath]++
		}
	}
	if len(got) != 2 || got["items[].old_a"] != 1 || got["items[].old_b"] != 1 {
		t.Errorf("want exactly one finding for each of items[].old_a and items[].old_b; got %v", got)
	}
}

// TestDeprecatedRequestFieldIsReported: the request half of the field rule —
// what this caller SENDS, which is the half they can change on their own.
func TestDeprecatedRequestFieldIsReported(t *testing.T) {
	const reqSpec = `
openapi: 3.0.3
info: {title: T, version: "1.0.0"}
servers:
  - url: http://api.acme.test
paths:
  /v1/charges:
    post:
      operationId: createCharge
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                amount: {type: integer}
                legacy_token: {type: string, deprecated: true}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object, properties: {id: {type: string}}}
`
	doc, err := LoadSpecData([]byte(reqSpec))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	call := depCall("http://api.acme.test/v1/charges", "/v1/charges", `{"id":"ch_1"}`)
	call.RequestBody = `{"amount":1200,"legacy_token":"tok_old"}`
	// No request Content-Type on purpose: a header-less JSON body is common,
	// and the only question asked of it is which keys it carries.
	fs, _, err := JudgeLiveVsSpec(doc, call)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	f := onlyRule(t, fs, RuleDeprecatedField)
	if f.FieldPath == nil || *f.FieldPath != "legacy_token" {
		t.Errorf("field_path = %v, want legacy_token", f.FieldPath)
	}
	if f.Location == nil || *f.Location != "$.request.body.legacy_token" {
		t.Errorf("location = %v, want $.request.body.legacy_token", f.Location)
	}
	if !strings.Contains(f.Detail, "sends it") {
		t.Errorf("detail should say the caller sends it: %q", f.Detail)
	}

	// Not sending it raises nothing.
	call.RequestBody = `{"amount":1200}`
	clean, _, err := JudgeLiveVsSpec(doc, call)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	for _, f := range clean {
		if f.Rule == RuleDeprecatedField {
			t.Error("a deprecated request field the call does not send must raise nothing")
		}
	}
}
