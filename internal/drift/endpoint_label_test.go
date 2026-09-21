package drift

import (
	"errors"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// A finding's endpoint is the CONTRACT'S PATH, not the URL the call happened to
// use. CONTRACTS §4: "a drift is per endpoint, not per call" — the signature is
// `integration|endpoint|kind|rule|field_path`, so anything varying per call that
// leaks into `endpoint` splits one drift into a finding per call.
//
// Two things vary in ordinary traffic and both used to land there: a path
// PARAMETER and a query string. `GET /v1/accounts/acct_1/balance?fields=all` and
// `GET /v1/accounts/acct_2/balance?fields=none` are the same endpoint, and the
// provider has one thing wrong with it, so they are one finding.
const paramSpec = `
openapi: 3.0.3
info: {title: T, version: "1.0.0"}
servers:
  - url: http://api.acme.test
paths:
  /v1/accounts/{account_id}/balance:
    get:
      operationId: getBalance
      parameters:
        - name: account_id
          in: path
          required: true
          schema: {type: string}
        - name: fields
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
                  amount: {type: integer}
`

func paramCall(id, acct, query string) model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion: 1, ID: id, Integration: "api-acme-test",
		Method: "GET",
		URL:    "http://api.acme.test/v1/accounts/" + acct + "/balance" + query,
		// What the capture records: the request path AND its query string.
		Route:               "/v1/accounts/" + acct + "/balance" + query,
		StatusCode:          200,
		ResponseContentType: "application/json",
		// `amount` is declared integer and answered as a string — a real
		// live-vs-spec finding, so this is the ORDINARY path, not an exotic one.
		ResponseBody: `{"amount":"120"}`,
	}
}

// TestOneDriftAcrossManyCalls is the claim in the form an operator would state
// it: reading two accounts with different query strings is ONE finding whose
// count is 2, not two findings whose counts are 1.
func TestOneDriftAcrossManyCalls(t *testing.T) {
	doc, err := LoadSpecData([]byte(paramSpec))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	first, _, err := JudgeLiveVsSpec(doc, paramCall("c1", "acct_1", "?fields=all"))
	if err != nil {
		t.Fatalf("judge c1: %v", err)
	}
	second, _, err := JudgeLiveVsSpec(doc, paramCall("c2", "acct_2", "?fields=none"))
	if err != nil {
		t.Fatalf("judge c2: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("want one finding per call, got %d and %d", len(first), len(second))
	}

	const wantEndpoint = "GET /v1/accounts/{account_id}/balance"
	for _, f := range []model.Finding{first[0], second[0]} {
		if f.Endpoint != wantEndpoint {
			t.Errorf("endpoint = %q, want %q — a per-call value here splits one drift into a "+
				"finding per call, and the endpoint list grows without bound", f.Endpoint, wantEndpoint)
		}
	}

	// The consequence the store acts on: same signature, so the second call
	// bumps occurrence_count instead of inserting a sibling row.
	if first[0].Signature != second[0].Signature {
		t.Errorf("two calls to one endpoint produced two signatures:\n  %q\n  %q",
			first[0].Signature, second[0].Signature)
	}
}

// TestUnroutedCallEmitsNoFinding pins the answer to "what is the label when no
// contract path matches?" — there is none, because there is no finding.
//
// The router runs BEFORE any finding is built, and a miss returns early with
// `errRouteNotFound`; the call is stamped not-routable and nothing is reported.
// So no live-vs-spec finding can ever carry an unrouted label, and a query
// string cannot reach `endpoint` by that route either. Pinned because it is the
// property that makes the labelling above total rather than usually-right.
func TestUnroutedCallEmitsNoFinding(t *testing.T) {
	doc, err := LoadSpecData([]byte(paramSpec))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	call := paramCall("c3", "acct_1", "?fields=all")
	call.URL = "http://api.acme.test/v1/nowhere?token=abc"
	call.Route = "/v1/nowhere?token=abc"

	fs, verdict, err := JudgeLiveVsSpec(doc, call)
	if len(fs) != 0 {
		t.Errorf("an unrouted call produced %d finding(s) — there is no contract path to label them with", len(fs))
	}
	if !errors.Is(err, errRouteNotFound) {
		t.Errorf("err = %v, want errRouteNotFound", err)
	}
	if verdict.Verdict != model.ValidatedNot || verdict.Reason != model.NotValidatedNotRoutable {
		t.Errorf("verdict = %q/%q, want not-validated/not-routable", verdict.Verdict, verdict.Reason)
	}
}
