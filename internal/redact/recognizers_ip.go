// IP recognizer (OPTIONAL — off by default; enable with WithIP). IPs are identifiers
// more often than PII and over-redact peer hosts, so the floor leaves them unless
// asked. Shapes are located here; govalidator.IsIPv4 / IsIPv6 (net.ParseIP — a pure
// parser, no resolution) DECIDE, rejecting 256.1.1.1 etc. Mirrors recognizers/ip.ts.
// Contract = contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

import (
	"regexp"
	"sort"

	"github.com/asaskevich/govalidator"
)

// ipv4CandidateRe: dotted quad shape.
var ipv4CandidateRe = regexp.MustCompile(`(?:[0-9]{1,3}\.){3}[0-9]{1,3}`)

// ipv6CandidateRe: hex groups with ≥ 2 colons (covers `::1`, `2001:db8::1`).
var ipv6CandidateRe = regexp.MustCompile(`(?:[0-9A-Fa-f]{0,4}:){2,7}[0-9A-Fa-f]{0,4}`)

// ipBounded: neither neighbour is a word character nor one of the extra bytes
// (so `1.2.3.4.5` and `a::b:c:d` prefixes/suffixes are not candidates).
func ipBounded(text string, start, end int, extra string) bool {
	if isWordCharAt(text, start-1) || isWordCharAt(text, end) {
		return false
	}
	for i := 0; i < len(extra); i++ {
		if start-1 >= 0 && text[start-1] == extra[i] {
			return false
		}
		if end < len(text) && text[end] == extra[i] {
			return false
		}
	}
	return true
}

func findIP(text string, _ Context) []Span {
	var spans []Span
	for _, loc := range ipv4CandidateRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		if ipBounded(text, start, end, ".") && govalidator.IsIPv4(text[start:end]) {
			spans = append(spans, Span{Start: start, End: end})
		}
	}
	for _, loc := range ipv6CandidateRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		if end-start < 2 {
			continue
		}
		if ipBounded(text, start, end, ":.") && govalidator.IsIPv6(text[start:end]) {
			spans = append(spans, Span{Start: start, End: end})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	var out []Span
	for _, s := range spans {
		if len(out) > 0 && s.Start < out[len(out)-1].End {
			continue
		}
		out = append(out, s)
	}
	return out
}
