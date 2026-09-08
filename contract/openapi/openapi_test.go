package openapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// The neutral Contract model used to read only the `application/json` entry of
// a request body and of the lowest 2xx response, so a document that declares
// its JSON under an RFC 6839 `+json` name (application/hal+json,
// application/vnd.api+json, a vendor application/vnd.acme.v2+json) produced an
// operation with no input or output schema — "no output contract", when the
// contract was right there.
const suffixDoc = `
openapi: 3.0.3
info: { title: t, version: "1.0.0" }
paths:
  /v1/charges:
    post:
      requestBody:
        required: true
        content:
          application/vnd.api+json:
            schema:
              type: object
              properties:
                amount: { type: integer }
      responses:
        "200":
          description: ok
          content:
            application/hal+json:
              schema:
                type: object
                properties:
                  id: { type: string }
  /v1/exports:
    post:
      requestBody:
        content:
          application/xml:
            schema: { type: string }
      responses:
        "200":
          description: ok
          content:
            application/xml:
              schema: { type: string }
`

func TestFrom_StructuredSuffixJSONBodiesYieldSchemas(t *testing.T) {
	doc, err := LoadData([]byte(suffixDoc))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c, err := From(doc, "api.acme.test", "2026-09-07T00:00:00Z", "test")
	if err != nil {
		t.Fatalf("from: %v", err)
	}
	byID := map[string]int{}
	for i, op := range c.Operations {
		byID[op.ID] = i
	}

	charges := c.Operations[byID["POST /v1/charges"]]
	if charges.OutputSchema == nil {
		t.Fatal("application/hal+json 200 yielded no output schema")
	}
	in, _ := json.Marshal(charges.InputSchema)
	if !strings.Contains(string(in), "amount") {
		t.Fatalf("application/vnd.api+json request body missing from the input schema: %s", in)
	}

	exports := c.Operations[byID["POST /v1/exports"]]
	if exports.OutputSchema != nil {
		t.Fatalf("application/xml is not JSON; want no output contract, got %v", exports.OutputSchema)
	}
	in, _ = json.Marshal(exports.InputSchema)
	if strings.Contains(string(in), `"string"`) {
		t.Fatalf("application/xml request body leaked into the input schema: %s", in)
	}
}
