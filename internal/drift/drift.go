// Package drift is the technical-adherence detector: live-traffic-vs-spec
// (kin-openapi) and spec v1->v2 breaking-diff (oasdiff). It validates
// fields/types/shapes/enums ONLY — never business/economic correctness.
package drift

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	contractopenapi "github.com/flanj-io/collector/contract/openapi"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/redact"
)

// LoadSpecFile reads and validates an OpenAPI document from disk. The OpenAPI
// loading lines moved to the public contract/openapi loader subpackage
// (v0.5 Step A; kin-openapi deliberately stays out of package contract) —
// this delegation keeps the drift API and behavior identical.
func LoadSpecFile(path string) (*openapi3.T, error) {
	return contractopenapi.LoadFile(path)
}

// LoadSpecData parses an OpenAPI document from bytes (delegates to the public
// contract/openapi loader; behavior identical).
func LoadSpecData(b []byte) (*openapi3.T, error) {
	return contractopenapi.LoadData(b)
}

// RuleContentTypeMismatch is the live-vs-spec rule of a response whose media
// type the contract declares under no name for that status — not verbatim,
// not normalized, not by its RFC 6839 base. The provider answers in a shape the
// contract never promised: drift evidence, one finding per endpoint.
const RuleContentTypeMismatch = "content-type-mismatch"

// DetectLiveVsSpec reconstructs the request from the stored call and validates
// the recorded response against doc using ValidateResponse (MultiError: true).
// Each schema violation becomes one Finding; an undeclared response media type
// becomes one finding too. The remaining ways a response evades judgement — an
// undeclared status, an empty, truncated or non-JSON body — yield no finding
// here; the per-call verdict stamp (feat/per-call-validated-stamp) is what
// records those as NOT validated, so they must never read clean downstream.
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

	endpoint := endpointLabel(call.Method, call.Route)
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")

	wireContentType := call.ResponseContentType
	if wireContentType == "" {
		wireContentType = "application/json"
	}

	// The contract lookup, RFC 6839-aware. kin-openapi resolves a response's
	// media type verbatim, then parameter-stripped, then `type/*`, then `*/*` —
	// so application/problem+json (RFC 7807, the standard error payload) misses
	// a contract that declares application/json, and used to leave the call
	// unjudged and reading clean. The suffix says the payload IS JSON, so the
	// lookup falls back to the base type — the LOOKUP only: the body is still
	// decoded and validated as JSON against the schema the contract declares.
	// A wire type the contract declares under none of those names is drift
	// evidence, not noise: one finding, and the call reads drifted.
	lookupType := wireContentType
	if !responseUnjudged(call.Method, call.StatusCode) {
		if declared := declaredContent(route.Operation, call.StatusCode); len(declared) > 0 {
			resolved, ok := resolveContentType(declared, wireContentType)
			if !ok {
				return []model.Finding{contentTypeMismatchFinding(call, endpoint, now, wireContentType, declared)}, nil
			}
			lookupType = resolved
		}
	}

	respHeader := http.Header{}
	respHeader.Set("Content-Type", lookupType)

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

	validatorMu.RLock()
	verr := openapi3filter.ValidateResponse(context.Background(), respInput)
	validatorMu.RUnlock()
	if verr == nil {
		return nil, nil
	}

	schemaErrs := collectSchemaErrors(verr)
	if len(schemaErrs) == 0 {
		// kin-openapi refused the exchange without holding a single value
		// against the schema (undeclared status, undecodable body): nothing to
		// report as drift. The verdict stamp owns saying "not validated".
		return nil, nil
	}
	findings := make([]model.Finding, 0, len(schemaErrs))
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

// structuredSuffixBase maps an RFC 6839 structured-syntax suffix to the base
// media type it denotes: `<type>/<subtype>+json` carries JSON, so it is looked
// up as application/json when the contract does not spell the suffixed name
// out. One entry, on purpose — the SDK's capture gate maps the same one — and
// one line to extend (`"xml": "application/xml"`) if a contract ever declares
// XML bodies this detector should judge.
var structuredSuffixBase = map[string]string{
	"json": "application/json",
}

// mediaTypeOf is `type/subtype` of a content-type header value: lowercased,
// trimmed, parameters dropped. Malformed values fall back to a plain strip so
// the caller still has something to name in a finding.
func mediaTypeOf(contentType string) string {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		return mt
	}
	mt := contentType
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

// structuredBase is the base media type an RFC 6839 suffix denotes
// (application/problem+json → application/json), or "" when the subtype carries
// no suffix this detector maps. A subtype has at most one structured suffix,
// always the last `+` segment (RFC 6838 §4.2.8).
func structuredBase(contentType string) string {
	mt := mediaTypeOf(contentType)
	plus := strings.LastIndexByte(mt, '+')
	if plus < 0 {
		return ""
	}
	return structuredSuffixBase[mt[plus+1:]]
}

// responseUnjudged mirrors the exchanges kin-openapi's ValidateResponse never
// looks at (a HEAD response, and the 301/304/307/308 bodies-less redirects):
// their media type is not held against the contract either.
func responseUnjudged(method string, status int) bool {
	if strings.EqualFold(method, http.MethodHead) {
		return true
	}
	switch status {
	case http.StatusMovedPermanently, http.StatusNotModified, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// resolveResponse is the contract's response object for a status — the exact
// code, else its `4XX`-style range, else the `default` response — or nil when
// the operation declares none of them (kin-openapi's own resolution order).
func resolveResponse(op *openapi3.Operation, status int) *openapi3.Response {
	if op == nil || op.Responses == nil || op.Responses.Len() == 0 {
		return nil
	}
	ref := op.Responses.Status(status)
	if ref == nil {
		ref = op.Responses.Default()
	}
	if ref == nil {
		return nil
	}
	return ref.Value
}

// declaredContent is the content map the contract declares for a status, or nil
// when the status resolves to no response or that response declares no body.
func declaredContent(op *openapi3.Operation, status int) openapi3.Content {
	resp := resolveResponse(op, status)
	if resp == nil || len(resp.Content) == 0 {
		return nil
	}
	return resp.Content
}

// resolveContentType is the name the declared content map answers to for a wire
// media type — the header verbatim (kin-openapi's own match, parameters and
// wildcards included), else its parsed `type/subtype`, else the base type its
// RFC 6839 suffix denotes. The name returned is what the validator is handed, so
// a `+json` body decodes and validates as JSON against the base type's schema.
func resolveContentType(declared openapi3.Content, wire string) (string, bool) {
	if declared.Get(wire) != nil {
		ensureJSONDecoder(wire)
		return wire, true
	}
	if mt := mediaTypeOf(wire); mt != "" && declared.Get(mt) != nil {
		ensureJSONDecoder(mt)
		return mt, true
	}
	if base := structuredBase(wire); base != "" && declared.Get(base) != nil {
		return base, true
	}
	return "", false
}

// validatorMu guards kin-openapi's body-decoder registry, which the library
// documents as NOT thread-safe: registration must never race a concurrent
// ValidateResponse (its map read has no lock of its own). This package is the
// binary's only openapi3filter user, so a read lock around every validation and
// a write lock around the rare registration is the whole story.
var validatorMu sync.RWMutex

// ensureJSONDecoder teaches kin-openapi to decode a `+json` media type the
// contract declares by name. It ships decoders for the common ones
// (problem+json, hal+json, vnd.api+json, …) but not for a vendor name such as
// application/vnd.acme.v2+json, which would otherwise fail as "unsupported
// content type" AFTER its lookup succeeded. Idempotent.
func ensureJSONDecoder(contentType string) {
	mt := mediaTypeOf(contentType)
	if structuredBase(mt) == "" {
		return
	}
	validatorMu.RLock()
	registered := openapi3filter.RegisteredBodyDecoder(mt) != nil
	validatorMu.RUnlock()
	if registered {
		return
	}
	validatorMu.Lock()
	defer validatorMu.Unlock()
	if openapi3filter.RegisteredBodyDecoder(mt) == nil {
		openapi3filter.RegisterBodyDecoder(mt, openapi3filter.JSONBodyDecoder)
	}
}

// contentTypeMismatchFinding: the provider answered in a media type the contract
// declares under no name for this status. Location is the header; Expected lists
// the declared names so the flag shows the promise beside the answer.
func contentTypeMismatchFinding(call model.RedactedCall, endpoint, now, wire string, declared openapi3.Content) model.Finding {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	sourceID := call.ID
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              otlpattr.NewID(),
		Kind:            model.KindLiveVsSpec,
		Severity:        model.SeverityBreaking,
		Integration:     call.Integration,
		Endpoint:        endpoint,
		FieldPath:       model.Ptr(""),
		Location:        model.Ptr("$.response.headers.content-type"),
		Expected:        "content-type=" + strings.Join(names, "|"),
		Actual:          "content-type=" + wire,
		Rule:            RuleContentTypeMismatch,
		SourceCallID:    &sourceID,
		DetectedAt:      now,
		Detail:          fmt.Sprintf("Response content-type `%s` is not declared for %d; the contract declares %s.", mediaTypeOf(wire), call.StatusCode, joinNames(names)),
		OccurrenceCount: 1,
		FirstSeen:       now,
		LastSeen:        now,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func joinNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	return strings.Join(quoted, ", ")
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
	return capturedFieldProps(call, "response", se)
}

// capturedFieldProps is the part-aware lookup behind responseFieldProps: the MCP
// detector also judges REQUEST-part (tools/call arguments) redactions with it.
func capturedFieldProps(call model.RedactedCall, part string, se *openapi3.SchemaError) (redact.ValueProps, bool) {
	var b strings.Builder
	for _, seg := range se.JSONPointer() {
		b.WriteByte('/')
		b.WriteString(redact.EscapePointerSegment(seg))
	}
	path := b.String() // "" = the root scalar
	for _, f := range call.Redaction.Fields {
		if f.Part == part && f.Path == path {
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
