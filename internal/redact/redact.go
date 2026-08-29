// Package redact is the collector's Go implementation of the Flanj redaction
// floor — the byte-for-byte mirror of the SDK's @flanj/redaction-patterns
// (sdk/packages/redaction-patterns). The floor owns: deep traversal (every string,
// keys included, undocumented fields included), Luhn-gated PAN detection (never
// brand/BIN-gated), base64 decode-then-scan, candidate anchoring against word
// characters, the ⟦REDACTED:<ID>⟧ token format, and the canonical report order. It is
// pure and fail-closed: no I/O of any kind (netban_test.go is the lint ban).
//
// The contract is the fixture files, not either implementation:
// contracts/redaction-vectors.json (scalar vectors) and
// contracts/redaction-fixtures.json (the cross-language parity battery — structured
// bodies, base64, forms, truncation, enhancer specs). Both language suites must
// produce these exact results.
//
// Three invariants (governed by the fixtures, tested in redact_test.go +
// fixtures_test.go):
//
//  1. Add-only: redaction only ever ADDS ⟦REDACTED:…⟧ tokens; it never removes an
//     existing one (poisoned-spec safety).
//  2. Idempotent: Redact(Redact(x)) == Redact(x); an existing ⟦REDACTED:…⟧ token is
//     inert to re-scanning and is never double-wrapped.
//  3. Redact before store/emit: callers must run this before a body is written to
//     disk or attached to any OTLP record.
package redact

import (
	"encoding/json"
	"reflect"
	"sort"
)

// Result is the outcome of a single Redact call.
type Result struct {
	// Text is the redacted string.
	Text string
	// Patterns is the de-duplicated list of pattern ids that fired on THIS call, in
	// canonical ReportOrder (empty when nothing new was redacted — e.g.
	// already-redacted input). It is the delta, not the running union.
	Patterns []string
	// Fields are the whole-value redactions with the original's captured properties,
	// sorted by path (see props.go). Always non-nil: an empty slice when none — the
	// nil/empty distinction matters to JSON consumers downstream.
	Fields []RedactedField
}

// Redactor applies the floor behind one swappable interface (mirroring the TS
// createRedactor):
//   - Redact(text) is the production path for captured BODIES: JSON is scanned in
//     place (only fired scalars rewritten), forms are decoded-then-scanned, anything
//     else is one scalar. Byte-identical to the TS package for the same input.
//   - RedactValue(value) recurses ARBITRARY nested structures (maps, slices, structs,
//     scalars) and returns a redacted clone plus the patterns that fired.
//
// Both entry points also report the whole-value redaction FIELD records (props.go):
// for every scalar that became exactly one token, the RFC 6901 path, the pattern and
// the original's non-reversible captured properties — so drift detection downstream
// can still judge type/length of redacted fields. Span-in-text redactions, redacted
// keys, form pairs and non-JSON text emit no fields.
//
// The zero value is not usable; use New.
type Redactor struct {
	// recognizers is the APPLICATION-ordered set (see DefaultRecognizers).
	recognizers []Recognizer
	// enableIP appends the optional IP recognizer (off by default: it over-redacts
	// peer hosts / identifiers).
	enableIP bool
	// replaced is set by WithRecognizers; the IP recognizer is still appended to a
	// replaced set when WithIP is also given (mirrors the TS option semantics).
	replaced bool
}

// Option configures a Redactor.
type Option func(*Redactor)

// WithIP enables the optional IP recognizer (appended last).
func WithIP() Option { return func(r *Redactor) { r.enableIP = true } }

// WithRecognizers replaces the default recognizer set (APPLICATION order). The engine
// behind the floor — traversal, token protection, base64 decode-then-scan, token
// format, report order — is unchanged; only the per-pattern recognizers swap.
func WithRecognizers(recs ...Recognizer) Option {
	return func(r *Redactor) {
		r.recognizers = append([]Recognizer(nil), recs...)
		r.replaced = true
	}
}

// New builds a Redactor with the mandatory floor enabled.
func New(opts ...Option) *Redactor {
	r := &Redactor{}
	for _, o := range opts {
		o(r)
	}
	if !r.replaced {
		r.recognizers = DefaultRecognizers()
	}
	if r.enableIP {
		r.recognizers = append(r.recognizers, IPRecognizer)
	}
	return r
}

// Redact applies the floor to s — the TEXT entry point (see textpath.go).
func (r *Redactor) Redact(s string) Result {
	fired := map[string]bool{}
	out, fields := redactTextPath(s, r.recognizers, fired)
	if fields == nil {
		fields = []RedactedField{}
	}
	return Result{Text: out, Patterns: inReportOrder(fired), Fields: SortFields(fields)}
}

// RedactValue applies the floor to an arbitrary decoded value — the STRUCTURAL entry
// point (mirrors the TS redact(value) walk). It returns a redacted deep clone, the
// fired pattern ids in canonical ReportOrder, and the whole-value redaction field
// records sorted by path (always a non-nil slice); the input is never mutated.
// Untouched values are returned as-is (a json.Number stays a json.Number), so a
// round-trip through RedactValue of an already-clean value is identity.
func (r *Redactor) RedactValue(v any) (any, []string, []RedactedField) {
	fired := map[string]bool{}
	fields := []RedactedField{}
	redacted := r.walk(v, Context{}, "", fired, &fields)
	return redacted, inReportOrder(fired), SortFields(fields)
}

// walk recurses one value. Strings go through the per-scalar engine; numbers through
// the PAN-as-number / CVV-under-key gate; containers are cloned with every element
// walked; everything else (bool, nil, chan, func, …) is untouched. path is the RFC
// 6901 pointer to v; every WHOLE-VALUE redaction (the scalar became exactly one token)
// appends a field record — keys never do (a key is not a spec-addressable value).
func (r *Redactor) walk(v any, ctx Context, path string, fired map[string]bool, fields *[]RedactedField) any {
	switch val := v.(type) {
	case string:
		out := redactScalar(val, ctx, r.recognizers, fired)
		if out != val {
			if id, ok := WholeTokenID(out); ok {
				*fields = append(*fields, RedactedField{Path: path, Pattern: id, Props: ComputeProps(val, "string", false)})
			}
		}
		return out
	case json.Number:
		// A json.Number carries the literal bytes: an integer is a literal with no
		// '.', 'e' or 'E'; its digits are the literal without a leading '-'. The props
		// text is the literal itself (the Go analogue of the TS String(value)).
		if digits, ok := integerDigitsOfNumber(val); ok {
			if id := classifyIntegerDigits(digits, ctx); id != "" {
				fired[id] = true
				*fields = append(*fields, RedactedField{Path: path, Pattern: id, Props: ComputeProps(string(val), "number", true)})
				return Token(id)
			}
		}
		return val
	case float64:
		return r.walkFloat(float64(val), val, ctx, path, fired, fields)
	case float32:
		return r.walkFloat(float64(val), val, ctx, path, fired, fields)
	case int:
		return r.walkInt(int64(val), val, ctx, path, fired, fields)
	case int8:
		return r.walkInt(int64(val), val, ctx, path, fired, fields)
	case int16:
		return r.walkInt(int64(val), val, ctx, path, fired, fields)
	case int32:
		return r.walkInt(int64(val), val, ctx, path, fired, fields)
	case int64:
		return r.walkInt(val, val, ctx, path, fired, fields)
	case uint:
		return r.walkUint(uint64(val), val, ctx, path, fired, fields)
	case uint8:
		return r.walkUint(uint64(val), val, ctx, path, fired, fields)
	case uint16:
		return r.walkUint(uint64(val), val, ctx, path, fired, fields)
	case uint32:
		return r.walkUint(uint64(val), val, ctx, path, fired, fields)
	case uint64:
		return r.walkUint(val, val, ctx, path, fired, fields)
	case uintptr:
		return val // an address, never data
	case bool, nil:
		return val
	case map[string]any:
		return r.walkMap(val, path, fired, fields)
	case []any:
		out := make([]any, len(val))
		for i, e := range val {
			out[i] = r.walk(e, Context{}, path+"/"+formatUint(uint64(i)), fired, fields)
		}
		return out
	}
	return r.walkReflect(v, ctx, path, fired, fields)
}

// walkMap clones a map with keys AND values redacted. Keys are iterated in SORTED
// order for determinism: Go map iteration is randomized, and when two different keys
// redact to the SAME token (two PANs as keys) the last one written wins — sorting
// makes that winner the last key in sorted order, deterministically, which is also
// what the TS mirror produces for its (insertion-ordered) objects in the fixtures.
func (r *Redactor) walkMap(m map[string]any, path string, fired map[string]bool, fields *[]RedactedField) map[string]any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(m))
	for _, k := range keys {
		// Redacted KEYS carry no field record — a key is not a spec-addressable value.
		kr := redactScalar(k, Context{}, r.recognizers, fired)
		out[kr] = r.walk(m[k], Context{Key: k, HasKey: true}, path+"/"+EscapePointerSegment(k), fired, fields)
	}
	return out
}

// walkFloat gates a float: only a finite, integral value below 1e21 has integer
// digits (mirrors the TS integerDigitsOf on a JS number); anything else — fractions,
// exponent-form magnitudes, NaN/Inf — is untouched. orig preserves the value's
// original Go type when nothing fires.
func (r *Redactor) walkFloat(f float64, orig any, ctx Context, path string, fired map[string]bool, fields *[]RedactedField) any {
	if digits, ok := integerDigitsOfFloat(f); ok {
		if id := classifyIntegerDigits(digits, ctx); id != "" {
			fired[id] = true
			*fields = append(*fields, RedactedField{Path: path, Pattern: id, Props: ComputeProps(signedLiteral(f < 0, digits), "number", true)})
			return Token(id)
		}
	}
	return orig
}

func (r *Redactor) walkInt(i int64, orig any, ctx Context, path string, fired map[string]bool, fields *[]RedactedField) any {
	digits := formatUint(absInt(i))
	if id := classifyIntegerDigits(digits, ctx); id != "" {
		fired[id] = true
		*fields = append(*fields, RedactedField{Path: path, Pattern: id, Props: ComputeProps(signedLiteral(i < 0, digits), "number", true)})
		return Token(id)
	}
	return orig
}

func (r *Redactor) walkUint(u uint64, orig any, ctx Context, path string, fired map[string]bool, fields *[]RedactedField) any {
	if id := classifyIntegerDigits(formatUint(u), ctx); id != "" {
		fired[id] = true
		*fields = append(*fields, RedactedField{Path: path, Pattern: id, Props: ComputeProps(formatUint(u), "number", true)})
		return Token(id)
	}
	return orig
}

// signedLiteral rebuilds a fired number's decimal literal from its digit string —
// the Go analogue of the TS String(value) the props are computed from (the sign is
// part of the literal; the digits never are more than the abs value's).
func signedLiteral(negative bool, digits string) string {
	if negative {
		return "-" + digits
	}
	return digits
}

// walkReflect handles the shapes the type switch cannot: pointers, named slice/array
// types, and STRUCTS. Structs are traversed over their exported fields only (Go
// reflection cannot read unexported ones) and returned as a modified COPY —
// reflect.New(t).Elem().Set(v) copies every field (unexported included), then the
// exported fields are overwritten with their walked values. The context key for a
// field is its json tag name when present, else the field name — the same key its
// serialized form would carry, so CVV-under-key behaves identically on both paths.
// The input is never mutated.
func (r *Redactor) walkReflect(v any, ctx Context, path string, fired map[string]bool, fields *[]RedactedField) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return v
		}
		return r.walk(rv.Elem().Interface(), ctx, path, fired, fields)
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = r.walk(rv.Index(i).Interface(), Context{}, path+"/"+formatUint(uint64(i)), fired, fields)
		}
		return out
	case reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = r.walk(rv.Index(i).Interface(), Context{}, path+"/"+formatUint(uint64(i)), fired, fields)
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		if rv.Type().Key().Kind() != reflect.String {
			return v // non-string-keyed maps have no JSON analogue; untouched
		}
		m := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			m[iter.Key().String()] = iter.Value().Interface()
		}
		return r.walkMap(m, path, fired, fields)
	case reflect.Struct:
		t := rv.Type()
		out := reflect.New(t).Elem()
		out.Set(rv) // copy everything, unexported fields included
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			key := f.Name
			if tag, ok := f.Tag.Lookup("json"); ok {
				if name, _, _ := cutTag(tag); name != "" {
					key = name
				}
			}
			// The path segment is the same key the serialized form would carry (json
			// tag name else field name) — identical to the ctx key, so a field record
			// addresses the field the way a spec would.
			walked := r.walk(rv.Field(i).Interface(), Context{Key: key, HasKey: true}, path+"/"+EscapePointerSegment(key), fired, fields)
			fv := out.Field(i)
			wv := reflect.ValueOf(walked)
			if walked == nil || !wv.Type().AssignableTo(fv.Type()) {
				// A fired scalar became a string token that no longer fits the field's
				// type (e.g. an int field). Structs are static shapes; leave the copied
				// original in place — the string/interface{} fields that carry PII are
				// the ones a token can land in.
				continue
			}
			fv.Set(wv)
		}
		return out.Interface()
	}
	return v // chan, func, unsafe pointer, …: untouched
}

// cutTag splits a json struct tag into (name, options, hasOptions).
func cutTag(tag string) (string, string, bool) {
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i], tag[i+1:], true
		}
	}
	return tag, "", false
}

// absInt is |i| as a uint64 (safe at MinInt64).
func absInt(i int64) uint64 {
	if i < 0 {
		return uint64(-(i + 1)) + 1
	}
	return uint64(i)
}

// formatUint renders u in decimal without pulling in strconv here.
func formatUint(u uint64) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}
