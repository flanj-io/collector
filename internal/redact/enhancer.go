// The schema-aware enhancer: our own spec-driven layer ABOVE the floor. It is applied
// to the floor's OUTPUT and may only ADD redaction, never subtract:
//   - it only ever replaces a string/number leaf that carries NO token with a token;
//   - a scalar the floor already touched is immutable (a poisoned spec cannot relabel
//     a PAN as EMAIL, nor "un-redact" anything — there is no operation for it);
//   - paths that do not resolve are ignored; containers are never replaced.
//
// The never-subtract law — every floor token survives, unchanged, at its path — is
// asserted by both language suites over every fixture × every spec. Mirrors
// enhancer.ts. Contract = contracts/redaction-fixtures.json.
package redact

import (
	"encoding/json"
	"strings"
)

// SensitiveField is one spec-declared sensitive field. Path is dot-separated; a
// segment may end with `[]` to address every element of an array. Type must be a
// floor pattern id — it names the token emitted (⟦REDACTED:<Type>⟧); unknown types
// are ignored.
type SensitiveField struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

// pathSegment is one parsed path segment: a key plus how many trailing `[]`.
type pathSegment struct {
	key    string
	arrays int
}

// parsePath parses a dot-separated path; ok is false for a malformed path (empty
// path, or an empty key once the `[]` suffixes are stripped).
func parsePath(path string) ([]pathSegment, bool) {
	if len(path) == 0 {
		return nil, false
	}
	raws := strings.Split(path, ".")
	segments := make([]pathSegment, 0, len(raws))
	for _, raw := range raws {
		key := raw
		arrays := 0
		for strings.HasSuffix(key, "[]") {
			key = key[:len(key)-2]
			arrays++
		}
		if len(key) == 0 {
			return nil, false
		}
		segments = append(segments, pathSegment{key: key, arrays: arrays})
	}
	return segments, true
}

// carriesToken is true when a scalar already carries any floor token — such scalars
// are immutable here.
func carriesToken(v any) bool {
	s, ok := v.(string)
	return ok && reportedTokenRe.MatchString(s)
}

// isEnhanceableLeaf mirrors the TS `typeof v === 'string' || typeof v === 'number'`:
// strings plus every numeric shape a decoded body can carry.
func isEnhanceableLeaf(v any) bool {
	switch v.(type) {
	case string, json.Number, float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

// Enhance applies a spec to the floor's output (see file comment). It returns the
// enhanced value — a clone-on-write: the input is never mutated, and untouched
// subtrees are shared — plus the pattern ids the enhancer itself fired, in canonical
// ReportOrder.
func Enhance(v any, spec []SensitiveField) (any, []string) {
	known := map[string]bool{}
	for _, id := range ReportOrder {
		known[id] = true
	}
	fired := map[string]bool{}
	current := v
	for _, field := range spec {
		if !known[field.Type] {
			continue
		}
		segments, ok := parsePath(field.Path)
		if !ok {
			continue
		}
		current, _ = enhanceApply(current, segments, 0, field.Type, fired)
	}
	return current, inReportOrder(fired)
}

// enhanceApplyArrays unwraps depth levels of `[]`: at depth 0 the next step runs on
// the value itself; a non-array where an array is addressed is left untouched.
func enhanceApplyArrays(value any, depth int, next func(any) (any, bool)) (any, bool) {
	if depth == 0 {
		return next(value)
	}
	arr, ok := value.([]any)
	if !ok {
		return value, false
	}
	out := make([]any, len(arr))
	changed := false
	for i, e := range arr {
		nv, ch := enhanceApplyArrays(e, depth-1, next)
		out[i] = nv
		changed = changed || ch
	}
	if !changed {
		return value, false
	}
	return out, true
}

// enhanceApply resolves one path segment. Only objects are descended by key
// (containers are never replaced: a leaf step on a map/array is a no-op), and a
// parent is cloned only when a descendant actually changed.
func enhanceApply(value any, segments []pathSegment, idx int, typ string, fired map[string]bool) (any, bool) {
	if idx >= len(segments) {
		return value, false // unreachable: callers stop at the leaf
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return value, false
	}
	seg := segments[idx]
	child, present := obj[seg.key]
	if !present {
		return value, false
	}
	isLeaf := idx == len(segments)-1
	var next func(any) (any, bool)
	if isLeaf {
		next = func(v any) (any, bool) {
			if isEnhanceableLeaf(v) && !carriesToken(v) {
				fired[typ] = true
				return Token(typ), true
			}
			return v, false
		}
	} else {
		next = func(v any) (any, bool) { return enhanceApply(v, segments, idx+1, typ, fired) }
	}
	replaced, changed := enhanceApplyArrays(child, seg.arrays, next)
	if !changed {
		return value, false
	}
	out := make(map[string]any, len(obj))
	for k, e := range obj {
		out[k] = e
	}
	out[seg.key] = replaced
	return out, true
}
