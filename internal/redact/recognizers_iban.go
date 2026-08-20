// IBAN recognizer. LOCATES a `CC##` head (2 letters, 2 digits) at a word boundary and
// consumes alphanumeric groups separated by single spaces (electronic `DE89370400…`
// and print `DE89 3704 0044 …` formats). Because prose can follow a print-format IBAN
// with a space, candidates are tried longest-first, dropping trailing groups, until
// the validator accepts one. isIBAN (registry length + mod-97, iban.go) DECIDES; the
// TS mirror uses the identical locator with validator's isIBAN. Mirrors
// recognizers/iban.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

// ISO 13616 length bounds of the stripped (separator-free) IBAN.
const (
	ibanMinLen = 15
	ibanMaxLen = 34
)

func findIBAN(text string, _ Context) []Span {
	var spans []Span
	i := 0
	for i < len(text) {
		head := isLetterAt(text, i) && isLetterAt(text, i+1) && isDigitAt(text, i+2) && isDigitAt(text, i+3)
		if !head || isWordCharAt(text, i-1) {
			i++
			continue
		}
		// Consume groups: a maximal alnum run, then optionally one space followed by alnum.
		var groups []Span
		pos := i
		stripped := 0
		for {
			start := pos
			for isAlnumAt(text, pos) {
				pos++
			}
			if pos == start {
				break
			}
			groups = append(groups, Span{Start: start, End: pos})
			stripped += pos - start
			if stripped >= ibanMaxLen {
				break
			}
			if byteAt(text, pos) == ' ' && isAlnumAt(text, pos+1) {
				pos++
				continue
			}
			break
		}
		matched := false
		length := stripped
		for e := len(groups) - 1; e >= 0; e-- {
			g := groups[e]
			if e < len(groups)-1 {
				length -= groups[e+1].End - groups[e+1].Start
			}
			if length < ibanMinLen {
				break
			}
			if length > ibanMaxLen || isWordCharAt(text, g.End) {
				continue
			}
			if isIBAN(text[i:g.End]) {
				spans = append(spans, Span{Start: i, End: g.End})
				i = g.End
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}
	return spans
}
