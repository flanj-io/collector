package drift

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// The live-vs-spec DEPRECATION path (CONTRACTS §4).
//
// A provider rarely breaks you overnight. They mark a surface deprecated, give
// a window, then remove it — and the removal is the only part the rest of this
// detector could ever see. By then the window has closed. So the announcement
// is the finding, at severity WARNING: nothing has broken, and something will.
//
// It is about THIS ORG'S OWN TRAFFIC, which is what separates it from the
// version-diff arm. A deprecated operation nobody here calls raises nothing at
// all — there is no one to tell and nothing to change. Only a deprecated thing
// a real call actually USED is reported, and it is reported once per
// (endpoint, deprecated thing) like every other finding, with occurrence_count
// rising while the calling continues.
//
// Severity is always WARNING, and the kind is `deprecation` rather than a
// warning-severity `live-vs-spec`. That is what keeps `calls.drifted` clean
// with no special case anywhere: the call conformed — the operation is still
// declared, the response still matched — and only the per-call drift KINDS
// mark a call. Painting it red would accuse the provider of breaking a promise
// they are in fact keeping while giving notice of ending it.
const (
	// RuleDeprecatedOperation: the call used an operation the contract marks
	// deprecated.
	RuleDeprecatedOperation = "deprecated-operation"
	// RuleDeprecatedParameter: the call SENT a parameter the contract marks
	// deprecated. A deprecated parameter the call omits is not reported —
	// nothing here uses it, so there is nothing to change.
	RuleDeprecatedParameter = "deprecated-parameter"
	// RuleDeprecatedField: the call's request or response body carried a
	// property the contract marks deprecated. Presence in the BODY is the
	// test, not presence in the schema, for the same reason.
	RuleDeprecatedField = "deprecated-field"
)

// sunsetExtension is the convention oasdiff reads and the one this path reads,
// so a sunset date means the same thing on both arms. Two spellings are
// accepted because both are in the wild and oasdiff accepts both: a bare date
// (2026-12-31) and a full RFC 3339 timestamp.
const sunsetExtension = "x-sunset"

// maxSchemaDepth bounds the property walk. A contract is a stranger's document
// fetched over the network, and a self-referential schema ($ref cycles, which
// kin-openapi resolves into genuinely cyclic pointers) would otherwise walk
// forever. The visited-set below is the real cycle guard; this is the belt to
// its braces, and no honest payload nests this deep.
const maxSchemaDepth = 24

// deprecatedUsage returns one finding per deprecated thing THIS call used:
// the operation, any parameter it sent, and any request- or response-body
// property it carried. Nothing the call did not use is reported.
//
// It runs off the resolved route alone and needs no response validation, so it
// is emitted on every path out of judgeLiveVsSpec — including the ones where
// the response could not be judged at all. Whether the body conformed is a
// different question from whether the surface is going away.
func deprecatedUsage(
	route *routers.Route,
	req *http.Request,
	pathParams map[string]string,
	call model.RedactedCall,
	endpoint, now, responseContentType string,
) []model.Finding {
	if route == nil || route.Operation == nil {
		return nil
	}
	op := route.Operation

	var out []model.Finding

	if op.Deprecated {
		out = append(out, deprecationFinding(call, endpoint, now,
			nil, "$.operation",
			fmt.Sprintf("Operation `%s` is deprecated%s.", endpoint, sunsetSuffix(op.Extensions)),
			RuleDeprecatedOperation, op.Extensions))
	}

	for _, name := range deprecatedParametersUsed(route, req, pathParams, call) {
		p := name
		out = append(out, deprecationFinding(call, endpoint, now,
			&p, "$.request.parameters."+name,
			fmt.Sprintf("Parameter `%s` is deprecated%s, and this call sends it.", name, sunsetSuffix(op.Extensions)),
			RuleDeprecatedParameter, op.Extensions))
	}

	// Request body first, then response: a caller can act on their own request
	// immediately, which makes it the more useful of the two to see first.
	//
	// An absent request Content-Type is read as JSON, the same assumption the
	// response side makes and for the same reason: a header-less JSON body is
	// common enough to keep judging, and the only question asked of the body
	// here is which KEYS it carries.
	reqContentType := call.RequestContentType
	if reqContentType == "" {
		reqContentType = "application/json"
	}
	if schema := requestBodySchema(op, reqContentType); schema != nil {
		for _, p := range deprecatedBodyFields(schema, call.RequestBody) {
			path := p
			out = append(out, deprecationFinding(call, endpoint, now,
				&path, "$.request.body."+path,
				fmt.Sprintf("Request field `%s` is deprecated%s, and this call sends it.", lastSegment(path), sunsetSuffix(op.Extensions)),
				RuleDeprecatedField, op.Extensions))
		}
	}
	if schema := responseBodySchema(op, call.StatusCode, responseContentType); schema != nil {
		for _, p := range deprecatedBodyFields(schema, call.ResponseBody) {
			path := p
			out = append(out, deprecationFinding(call, endpoint, now,
				&path, "$.response.body."+path,
				fmt.Sprintf("Response field `%s` is deprecated%s, and this call reads it.", lastSegment(path), sunsetSuffix(op.Extensions)),
				RuleDeprecatedField, op.Extensions))
		}
	}
	return out
}

// deprecationFinding builds one warning finding. The signature convention is
// the shared one (integration|endpoint|kind|rule|field_path), so a second call
// to the same deprecated thing bumps occurrence_count rather than adding a row,
// and the three rules never collide on one endpoint.
func deprecationFinding(
	call model.RedactedCall, endpoint, now string,
	fieldPath *string, location, detail, rule string,
	ext map[string]any,
) model.Finding {
	sourceID := call.ID
	expected := "not deprecated"
	actual := "deprecated"
	if s := sunsetDate(ext); s != "" {
		actual = "deprecated, sunset " + s
	}
	f := model.Finding{
		SchemaVersion: model.SchemaVersion,
		ID:            otlpattr.NewID(),
		// Its OWN kind, not a warning-severity live-vs-spec. A reader asking
		// "is anything I depend on going away?" answers it from the kind, and
		// the kind is also what keeps this out of the per-call drifted mark and
		// the live-drift headline — the call conformed.
		Kind:            model.KindDeprecation,
		Severity:        model.SeverityWarning,
		Integration:     call.Integration,
		Endpoint:        endpoint,
		FieldPath:       fieldPath,
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

// sunsetDate reads the contract's own sunset announcement, or "" when it made
// none. A deprecation with a date is the actionable kind — it says how long you
// have — so the date rides the finding's `actual` and its detail rather than
// being left for a reader to find in the document.
func sunsetDate(ext map[string]any) string {
	if ext == nil {
		return ""
	}
	v, ok := ext[sunsetExtension]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		// Some documents carry the value as raw JSON rather than a decoded
		// string. Unwrap that one case; anything else is not a date we can
		// state, and a date we cannot state is better omitted than guessed.
		if raw, isRaw := v.(json.RawMessage); isRaw {
			if err := json.Unmarshal(raw, &s); err != nil {
				return ""
			}
		} else {
			return ""
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// An RFC 3339 timestamp is reported by its date half: the time of day is
	// noise in a sentence about a removal window.
	if i := strings.IndexByte(s, 'T'); i == 10 {
		s = s[:10]
	}
	if len(s) > 32 {
		return ""
	}
	return s
}

func sunsetSuffix(ext map[string]any) string {
	if s := sunsetDate(ext); s != "" {
		return " (sunset " + s + ")"
	}
	return ""
}

// deprecatedParametersUsed names the deprecated parameters this call actually
// SENT. Path-item parameters are included, and an operation-level parameter of
// the same (name, in) overrides the path-level one exactly as OpenAPI says.
//
// Cookie parameters are deliberately not inspected: the redaction floor treats
// Cookie as a credential header, so the collector has no reliable list of the
// cookies a call sent and would have to guess. A missed deprecated cookie is a
// gap; a guessed one is a false accusation about a stranger's API.
func deprecatedParametersUsed(route *routers.Route, req *http.Request, pathParams map[string]string, call model.RedactedCall) []string {
	type key struct{ name, in string }
	effective := map[key]*openapi3.Parameter{}
	if route.PathItem != nil {
		for _, ref := range route.PathItem.Parameters {
			if ref != nil && ref.Value != nil {
				effective[key{ref.Value.Name, ref.Value.In}] = ref.Value
			}
		}
	}
	for _, ref := range route.Operation.Parameters {
		if ref != nil && ref.Value != nil {
			effective[key{ref.Value.Name, ref.Value.In}] = ref.Value
		}
	}

	var query map[string][]string
	if req != nil && req.URL != nil {
		query = req.URL.Query()
	}

	var names []string
	for k, p := range effective {
		if !p.Deprecated {
			continue
		}
		used := false
		switch k.in {
		case openapi3.ParameterInQuery:
			_, used = query[k.name]
		case openapi3.ParameterInPath:
			_, used = pathParams[k.name]
		case openapi3.ParameterInHeader:
			for h := range call.RequestHeaders {
				if strings.EqualFold(h, k.name) {
					used = true
					break
				}
			}
		}
		if used {
			names = append(names, k.name)
		}
	}
	// Map iteration is random and a finding's identity must not be: sort, so
	// one call's findings are emitted in a stable order.
	sort.Strings(names)
	return names
}

func requestBodySchema(op *openapi3.Operation, contentType string) *openapi3.SchemaRef {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil
	}
	content := op.RequestBody.Value.Content
	if len(content) == 0 {
		return nil
	}
	name, ok := resolveContentType(content, contentType)
	if !ok {
		return nil
	}
	if mt := content[name]; mt != nil {
		return mt.Schema
	}
	return nil
}

func responseBodySchema(op *openapi3.Operation, status int, contentType string) *openapi3.SchemaRef {
	content := declaredContent(op, status)
	if len(content) == 0 {
		return nil
	}
	name, ok := resolveContentType(content, contentType)
	if !ok {
		return nil
	}
	if mt := content[name]; mt != nil {
		return mt.Schema
	}
	return nil
}

// deprecatedBodyFields returns the dotted paths of every declared-deprecated
// property the body actually CARRIES.
//
// Presence in the body is the whole test. A contract may deprecate a dozen
// fields; the only ones worth a finding are the ones this org's traffic is
// still reading or sending, because those are the ones that will break when the
// window closes.
//
// The body is parsed as JSON. Anything that does not decode yields nothing:
// this path reports what it can prove, and a body it cannot read proves
// nothing. Redaction tokens are irrelevant here — the question is whether the
// KEY is present, never what its value is — so a redacted field is judged
// exactly like any other, which is the one place in this detector where the
// floor costs no fidelity at all.
func deprecatedBodyFields(schema *openapi3.SchemaRef, body string) []string {
	if schema == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return nil
	}
	var found []string
	walkDeprecated(schema, decoded, "", map[*openapi3.Schema]bool{}, 0, &found)
	// One path, one finding: composition keywords and array elements can each
	// reach the same property, and the drift is the field, not the route the
	// walk took to it.
	sort.Strings(found)
	out := found[:0]
	var last string
	for i, p := range found {
		if i == 0 || p != last {
			out = append(out, p)
		}
		last = p
	}
	return out
}

// maxArrayElements bounds the per-array element scan (see walkDeprecated). A
// list endpoint's response is a normal place to find thousands of rows, and
// they all share one declared item schema.
const maxArrayElements = 64

// walkDeprecated descends the DECLARED schema and the decoded body together,
// collecting the paths of deprecated properties the body carries.
//
// It is driven by the schema, not by the body: a body can be arbitrarily large
// and mostly undeclared, while the schema bounds the walk to what the contract
// actually says. `visited` guards the cycles kin-openapi's $ref resolution
// creates — a self-referential schema is a real and legal shape (a tree node,
// a linked comment) and following one without a guard never returns.
func walkDeprecated(ref *openapi3.SchemaRef, value any, prefix string, visited map[*openapi3.Schema]bool, depth int, out *[]string) {
	if ref == nil || ref.Value == nil || depth > maxSchemaDepth {
		return
	}
	s := ref.Value
	if visited[s] {
		return
	}
	visited[s] = true
	defer delete(visited, s)

	// Composition keywords describe the SAME value, so they are walked against
	// the same node and the same prefix. A property deprecated in one branch of
	// a oneOf is deprecated for a body that carries it.
	for _, group := range [][]*openapi3.SchemaRef{s.AllOf, s.AnyOf, s.OneOf} {
		for _, sub := range group {
			walkDeprecated(sub, value, prefix, visited, depth+1, out)
		}
	}

	switch v := value.(type) {
	case map[string]any:
		for name, propRef := range s.Properties {
			child, present := v[name]
			if !present {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if propRef != nil && propRef.Value != nil && propRef.Value.Deprecated {
				*out = append(*out, path)
			}
			walkDeprecated(propRef, child, path, visited, depth+1, out)
		}
	case []any:
		if s.Items == nil {
			return
		}
		// Every element shares one declared item schema, so a deprecated
		// property yields ONE path for the array however many elements carry
		// it — the finding is about the FIELD, and a hundred-element response
		// must not become a hundred findings. Paths are deduped by the caller,
		// and the path names the array rather than an index for the same
		// reason.
		//
		// Elements are scanned to a cap rather than stopping at the first one
		// that matches: a deprecated field is optional as often as not, so the
		// first element is no guarantee of what the rest carry. The cap bounds
		// the walk on a response that is one enormous array, which is a normal
		// shape for a list endpoint.
		for i, el := range v {
			if i >= maxArrayElements {
				return
			}
			walkDeprecated(s.Items, el, prefix+"[]", visited, depth+1, out)
		}
	}
}
