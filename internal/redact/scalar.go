// The per-scalar engine: protects existing tokens (idempotency / never double-wrap),
// runs the recognizers in application order over each unprotected segment, then the
// base64 decode-then-scan pass. Everything structural above this (objects, arrays,
// JSON text, forms) funnels every scalar through here, so one scalar contract serves
// all entry points — and the TS package (scalar.ts) implements the identical function.
// Contract = contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

import (
	"sort"
	"strings"
)

// splice replaces spans (sorted, non-overlapping) in text with token.
func splice(text string, spans []Span, token string) string {
	var b strings.Builder
	cursor := 0
	for _, s := range spans {
		b.WriteString(text[cursor:s.Start])
		b.WriteString(token)
		cursor = s.End
	}
	b.WriteString(text[cursor:])
	return b.String()
}

// normalise is the defensive normalisation of a recognizer's spans: sort by start
// (then end), drop empty spans, drop overlaps (first wins). Stable, like the TS sort.
func normalise(spans []Span) []Span {
	sorted := append([]Span(nil), spans...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})
	out := sorted[:0]
	for _, s := range sorted {
		if s.End <= s.Start {
			continue
		}
		if len(out) > 0 && s.Start < out[len(out)-1].End {
			continue
		}
		out = append(out, s)
	}
	return out
}

// applyRecognizers runs the recognizers sequentially over one token-free segment.
// Each recognizer sees the text as rewritten by the ones before it.
func applyRecognizers(segment string, ctx Context, recognizers []Recognizer, fired map[string]bool) string {
	text := segment
	for _, rec := range recognizers {
		spans := normalise(rec.Find(text, ctx))
		if len(spans) == 0 {
			continue
		}
		fired[rec.ID()] = true
		text = splice(text, spans, Token(rec.ID()))
	}
	return text
}

// applyBase64 decode-then-scans every base64 run; on a hit the WHOLE run becomes one
// token — the token of the first fired id in ReportOrder — and every id that fired
// inside the decoded text is reported.
func applyBase64(segment string, recognizers []Recognizer, fired map[string]bool) string {
	runs := findBase64Runs(segment)
	if len(runs) == 0 {
		return segment
	}
	var b strings.Builder
	cursor := 0
	for _, run := range runs {
		inner := map[string]bool{}
		applyRecognizers(run.Decoded, Context{}, recognizers, inner)
		if len(inner) == 0 {
			continue
		}
		first := ""
		for _, id := range ReportOrder {
			if inner[id] {
				first = id
				break
			}
		}
		if first == "" {
			// Only reachable with a custom recognizer whose id is not a floor id; the
			// TS mirror would index undefined here. Pick the recognizer that fired in
			// application order so the run is still redacted (fail-closed).
			for _, rec := range recognizers {
				if inner[rec.ID()] {
					first = rec.ID()
					break
				}
			}
		}
		for id := range inner {
			fired[id] = true
		}
		b.WriteString(segment[cursor:run.Start])
		b.WriteString(Token(first))
		cursor = run.End
	}
	b.WriteString(segment[cursor:])
	return b.String()
}

// redactScalar runs the full per-scalar engine over value (see file comment). It
// returns the rewritten value and marks every pattern id that fired in fired.
func redactScalar(value string, ctx Context, recognizers []Recognizer, fired map[string]bool) string {
	if len(value) == 0 {
		return value
	}
	scan := func(seg string) string {
		if len(seg) == 0 {
			return seg
		}
		return applyBase64(applyRecognizers(seg, ctx, recognizers, fired), recognizers, fired)
	}
	locs := tokenRe.FindAllStringIndex(value, -1)
	if len(locs) == 0 {
		return scan(value)
	}
	var b strings.Builder
	cursor := 0
	for _, loc := range locs {
		b.WriteString(scan(value[cursor:loc[0]])) // redact the gap before the token
		b.WriteString(value[loc[0]:loc[1]])       // copy the token verbatim
		cursor = loc[1]
	}
	b.WriteString(scan(value[cursor:]))
	return b.String()
}
