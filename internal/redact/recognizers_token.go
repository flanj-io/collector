// TOKEN (credential) recognizer. Secrets have no checksum library; these are
// format-anchored shapes (the JWT additionally validates that its header decodes to a
// JSON object). Overlapping hits (e.g. `Bearer sk_live_…`) keep the longest span.
// Mirrors recognizers/token.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// secretKeyRe: secret-key shaped credentials: `sk_live_…`, `sk_test_…`, `pk_live_…`,
// `rk_test_…` (two lowercase letters, a live/test environment, ≥ 6 key chars).
var secretKeyRe = regexp.MustCompile(`\b[a-z]{2}_(?:live|test)_[A-Za-z0-9]{6,}`)

// jwtRe: three base64url segments starting with `eyJ` (`{"`). The header is VALIDATED
// by isJoseHeader.
var jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}`)

// bearerRe: `Bearer <token>` (scheme is case-insensitive per RFC 7235; ASCII folding,
// see asciiFold); the token is group 1.
var bearerRe = regexp.MustCompile(`\b` + asciiFold("bearer") + jsSpace + `+([A-Za-z0-9._~+/=\-]{8,})`)

// isJoseHeader decodes a base64url segment and requires a JSON object — a real JOSE
// header. Node's Buffer.from(segment, 'base64url') is lenient: a segment whose length
// is ≡ 1 (mod 4) simply loses its dangling final character, so we drop it too before
// handing the segment to Go's (stricter) RawURLEncoding.
func isJoseHeader(segment string) bool {
	if len(segment)%4 == 1 {
		segment = segment[:len(segment)-1]
	}
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil || !utf8.Valid(raw) {
		return false
	}
	// JSON.parse(decoded) must yield a non-null, non-array object: after JSON
	// whitespace the text must open with `{` (json.Unmarshal would happily put `null`
	// into a map) and the whole text must be valid JSON.
	decoded := strings.TrimLeft(string(raw), " \t\n\r")
	if !strings.HasPrefix(decoded, "{") {
		return false
	}
	var obj map[string]any
	return json.Unmarshal(raw, &obj) == nil
}

func findToken(text string, _ Context) []Span {
	var found []Span
	for _, loc := range secretKeyRe.FindAllStringIndex(text, -1) {
		found = append(found, Span{Start: loc[0], End: loc[1]})
	}
	for _, loc := range jwtRe.FindAllStringIndex(text, -1) {
		m := text[loc[0]:loc[1]]
		header := m[:strings.IndexByte(m, '.')]
		if isJoseHeader(header) {
			found = append(found, Span{Start: loc[0], End: loc[1]})
		}
	}
	for _, m := range bearerRe.FindAllStringSubmatchIndex(text, -1) {
		found = append(found, Span{Start: m[2], End: m[3]})
	}

	// Sort by start, longest first; drop anything overlapping an accepted span.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Start != found[j].Start {
			return found[i].Start < found[j].Start
		}
		return found[i].End > found[j].End
	})
	var spans []Span
	for _, s := range found {
		if len(spans) > 0 && s.Start < spans[len(spans)-1].End {
			continue
		}
		spans = append(spans, s)
	}
	return spans
}
