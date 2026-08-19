// Package drift is the technical-adherence detector: live-traffic-vs-spec
// (kin-openapi) and spec v1->v2 breaking-diff (oasdiff). It validates
// fields/types/shapes/enums ONLY — never business/economic correctness.
package drift

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
)

// LoadSpecFile reads and validates an OpenAPI document from disk.
func LoadSpecFile(path string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("spec invalid: %w", err)
	}
	return doc, nil
}

// LoadSpecData parses an OpenAPI document from bytes.
func LoadSpecData(b []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("spec invalid: %w", err)
	}
	return doc, nil
}

// DetectLiveVsSpec reconstructs the request from the stored call and validates
// the recorded response against doc using ValidateResponse (MultiError: true).
// Each schema violation becomes one Finding.
func DetectLiveVsSpec(doc *openapi3.T, call model.RedactedCall) ([]model.Finding, error) {
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}

	reqURL := call.URL
	if reqURL == "" {
		reqURL = call.Route
	}
	req, err := http.NewRequest(call.Method, reqURL, strings.NewReader(call.RequestBody))
	if err != nil {
		return nil, fmt.Errorf("reconstruct request: %w", err)
	}
	if call.RequestContentType != "" {
		req.Header.Set("Content-Type", call.RequestContentType)
	}

	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		// No matching route in the spec is itself a drift signal, but v0's
		// deterministic finding is the response-schema mismatch; surface the
		// route miss as an error the caller can log.
		return nil, fmt.Errorf("route not found in spec for %s %s: %w", call.Method, reqURL, err)
	}

	reqInput := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
	}

	respContentType := call.ResponseContentType
	if respContentType == "" {
		respContentType = "application/json"
	}
	respHeader := http.Header{}
	respHeader.Set("Content-Type", respContentType)

	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 call.StatusCode,
		Header:                 respHeader,
		Body:                   noopCloser(call.ResponseBody),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			MultiError:            true,
		},
	}

	endpoint := endpointLabel(call.Method, call.Route)
	verr := openapi3filter.ValidateResponse(context.Background(), respInput)
	if verr == nil {
		return nil, nil
	}

	schemaErrs := collectSchemaErrors(verr)
	if len(schemaErrs) == 0 {
		return nil, nil
	}
	findings := make([]model.Finding, 0, len(schemaErrs))
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	for _, se := range schemaErrs {
		findings = append(findings, liveVsSpecFinding(se, call, endpoint, now))
	}
	return findings, nil
}

func liveVsSpecFinding(se *openapi3.SchemaError, call model.RedactedCall, endpoint, now string) model.Finding {
	fieldPath := strings.Join(se.JSONPointer(), ".")
	location := "$.response.body"
	if fieldPath != "" {
		location += "." + fieldPath
	}
	expected := expectedFromSchema(se)
	actual := actualFromValue(se.Value)
	rule := ruleFromSchemaField(se.SchemaField)
	sourceID := call.ID
	detail := fmt.Sprintf("Response field `%s` %s.", lastSegment(fieldPath), se.Reason)
	if se.Reason == "" {
		detail = fmt.Sprintf("Response field `%s` violates the spec (%s).", lastSegment(fieldPath), rule)
	}
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              otlpattr.NewID(),
		Kind:            model.KindLiveVsSpec,
		Severity:        model.SeverityBreaking,
		Integration:     call.Integration,
		Endpoint:        endpoint,
		FieldPath:       model.Ptr(fieldPath),
		Location:        model.Ptr(location),
		Expected:        expected,
		Actual:          actual,
		Rule:            rule,
		SourceCallID:    &sourceID,
		DetectedAt:      now,
		Detail:          detail,
		OccurrenceCount: 1,
		FirstSeen:       now,
		LastSeen:        now,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func expectedFromSchema(se *openapi3.SchemaError) string {
	if se.Schema != nil && se.Schema.Type != nil {
		types := se.Schema.Type.Slice()
		if len(types) > 0 {
			return "type=" + strings.Join(types, "|")
		}
	}
	if se.SchemaField != "" {
		return se.SchemaField
	}
	return se.Reason
}

func actualFromValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "type=null"
	case string:
		return fmt.Sprintf("type=string (%q)", t)
	case bool:
		return fmt.Sprintf("type=boolean (%v)", t)
	case float64:
		return fmt.Sprintf("type=number (%v)", t)
	case int, int64:
		return fmt.Sprintf("type=integer (%v)", t)
	case map[string]interface{}:
		return "type=object"
	case []interface{}:
		return "type=array"
	default:
		return fmt.Sprintf("type=%T (%v)", v, v)
	}
}

func ruleFromSchemaField(field string) string {
	switch field {
	case "type":
		return "type-mismatch"
	case "enum":
		return "undocumented-enum"
	case "required":
		return "missing-required"
	case "":
		return "schema-mismatch"
	default:
		return field + "-mismatch"
	}
}

// collectSchemaErrors flattens kin-openapi's error tree into SchemaErrors.
func collectSchemaErrors(err error) []*openapi3.SchemaError {
	var out []*openapi3.SchemaError
	var walk func(error)
	walk = func(e error) {
		switch t := e.(type) {
		case nil:
			return
		case openapi3.MultiError:
			for _, sub := range t {
				walk(sub)
			}
		case *openapi3filter.ResponseError:
			if t.Err != nil {
				walk(t.Err)
			}
		case *openapi3filter.RequestError:
			if t.Err != nil {
				walk(t.Err)
			}
		case *openapi3.SchemaError:
			out = append(out, t)
		default:
			// Some wrappers expose Unwrap.
			if u, ok := e.(interface{ Unwrap() error }); ok {
				if inner := u.Unwrap(); inner != nil {
					walk(inner)
				}
			}
		}
	}
	walk(err)
	return out
}

func endpointLabel(method, route string) string {
	return strings.ToUpper(method) + " " + route
}

func lastSegment(path string) string {
	if path == "" {
		return path
	}
	parts := strings.Split(path, ".")
	return parts[len(parts)-1]
}

type stringReadCloser struct{ *strings.Reader }

func (stringReadCloser) Close() error { return nil }

func noopCloser(s string) stringReadCloser {
	return stringReadCloser{strings.NewReader(s)}
}
