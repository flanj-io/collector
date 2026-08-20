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
	"github.com/vinifera-io/collector/internal/redact"
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
		if redactedValue(se.Value) {
			// Drift runs AFTER the redaction floor, so this constraint may have
			// "failed" only because the floor replaced the value with a
			// ⟦REDACTED:…⟧ token (a pattern the token can't match, a type the
			// PAN-as-number rewrite changed, a length the token bytes corrupt).
			// Redacted means UNKNOWN by default — but for a WHOLE-VALUE redaction
			// the call carries the original's captured properties
			// (redaction.fields, CONTRACTS §2/§6), which make the DECIDABLE
			// constraints (type, min/maxLength) judgeable again. Undecidable
			// constraints (pattern/format/enum/…) and token-carrying values with
			// no matching record (span redactions; older SDKs that emit no
			// fields) keep skipping — the skip stays one-directional: it cannot
			// mask drift on a value the floor did not touch. Constraints the
			// token accidentally SATISFIES are not re-checked against the props
			// (kin-openapi produced no error to hang them on) — an accepted
			// under-detection.
			props, ok := responseFieldProps(call, se)
			if !ok || evaluateRedactedConstraint(se, props) != propsViolated {
				continue
			}
			findings = append(findings, liveVsSpecFinding(se, call, endpoint, now, actualFromProps(se.SchemaField, props)))
			continue
		}
		findings = append(findings, liveVsSpecFinding(se, call, endpoint, now, actualFromValue(se.Value)))
	}
	return findings, nil
}

// propsVerdict is the outcome of judging a redacted value's schema error against
// its captured properties.
type propsVerdict int

const (
	// propsUndecidable: the constraint cannot be judged from the props — skip.
	propsUndecidable propsVerdict = iota
	// propsSatisfied: the ORIGINAL satisfied the constraint (the token broke it) — skip.
	propsSatisfied
	// propsViolated: the ORIGINAL itself violated the constraint — a real finding.
	propsViolated
)

// responseFieldProps finds the captured properties for the schema error's field in
// the call's response-part redaction.fields records (the live-vs-spec detector
// validates the RESPONSE body). The lookup key is the error's RFC 6901 pointer.
func responseFieldProps(call model.RedactedCall, se *openapi3.SchemaError) (redact.ValueProps, bool) {
	var b strings.Builder
	for _, seg := range se.JSONPointer() {
		b.WriteByte('/')
		b.WriteString(redact.EscapePointerSegment(seg))
	}
	path := b.String() // "" = the root scalar
	for _, f := range call.Redaction.Fields {
		if f.Part == "response" && f.Path == path {
			return f.Props, true
		}
	}
	return redact.ValueProps{}, false
}

// evaluateRedactedConstraint judges the DECIDABLE constraint kinds against the
// original's captured properties; everything else is undecidable.
func evaluateRedactedConstraint(se *openapi3.SchemaError, props redact.ValueProps) propsVerdict {
	if se.Schema == nil {
		return propsUndecidable
	}
	switch se.SchemaField {
	case "type":
		if se.Schema.Type == nil {
			return propsUndecidable
		}
		// A union type is satisfied when ANY member accepts the original. A
		// captured scalar is a string or a number, so boolean/array/object/null
		// spec types can never be satisfied by it.
		for _, t := range se.Schema.Type.Slice() {
			switch t {
			case "string":
				if props.Type == "string" {
					return propsSatisfied
				}
			case "integer":
				if props.Type == "number" && props.Integer != nil && *props.Integer {
					return propsSatisfied
				}
			case "number":
				if props.Type == "number" {
					return propsSatisfied
				}
			}
		}
		return propsViolated
	case "minLength":
		// Length constraints are only decidable for an original STRING (a number's
		// captured length is its literal's, which no string constraint governs).
		if props.Type != "string" || props.Length < 0 {
			return propsUndecidable
		}
		if uint64(props.Length) < se.Schema.MinLength {
			return propsViolated
		}
		return propsSatisfied
	case "maxLength":
		if props.Type != "string" || props.Length < 0 || se.Schema.MaxLength == nil {
			return propsUndecidable
		}
		if uint64(props.Length) > *se.Schema.MaxLength {
			return propsViolated
		}
		return propsSatisfied
	}
	return propsUndecidable
}

// actualFromProps renders the finding's `actual` for a violation judged from the
// captured properties of a redacted value (the value itself is gone by design).
func actualFromProps(schemaField string, props redact.ValueProps) string {
	if schemaField == "minLength" || schemaField == "maxLength" {
		return fmt.Sprintf("length=%d (redacted)", props.Length)
	}
	if props.Type == "string" {
		return fmt.Sprintf("type=string (redacted; length=%d)", props.Length)
	}
	return "type=number (redacted)"
}

// redactedValue reports whether a schema error's offending value is a SCALAR that
// carries a redaction token. Deliberately scalar-only: every constraint the floor can
// directly break lands on the redacted scalar itself (pattern/format/enum/length on
// the token string; type after the PAN-as-number number→string rewrite). Container-
// level errors (required-missing, minItems, …) are NOT skipped even when a nested
// field carries a token — the text-path floor never adds or removes keys/elements,
// so those errors are the provider's, and skipping them would mask genuine drift.
func redactedValue(v interface{}) bool {
	s, ok := v.(string)
	return ok && redact.ContainsToken(s)
}

func liveVsSpecFinding(se *openapi3.SchemaError, call model.RedactedCall, endpoint, now, actual string) model.Finding {
	fieldPath := strings.Join(se.JSONPointer(), ".")
	location := "$.response.body"
	if fieldPath != "" {
		location += "." + fieldPath
	}
	expected := expectedFromSchema(se)
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
