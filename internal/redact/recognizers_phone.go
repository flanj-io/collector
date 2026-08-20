// PHONE recognizer — INTERNATIONAL numbers only (`+` country code), in common
// separated formats: `+14155552671`, `+1 415 555 2671`, `+1 (415) 555-2671`,
// `+49.30.901820`. Our code LOCATES `+` followed by digit groups joined by single
// ` `/`.`/`-` and optional parentheses; github.com/nyaruka/phonenumbers (the Go port of
// libphonenumber, full metadata, matching the TS mirror's libphonenumber-js/max)
// DECIDES validity. Candidates are tried longest-first, dropping trailing groups (so
// `+1 415 555 2671 1225` finds the number and spares the 1225).
//
// National formats without `+` are NOT redacted: without a region they cannot be
// validated, and a loose phone regex is precisely what re-caught Luhn-spared ids.
// Mirrors recognizers/phone.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

import "github.com/nyaruka/phonenumbers"

// E.164 bounds: at most 15 digits; below 5 nothing validates anywhere.
const (
	phoneMaxDigits = 15
	phoneMinDigits = 5
	phoneMaxGroups = 8
)

// isValidPhone is the validator: metadata-backed parse + IsValidNumber, no region
// (candidates always carry their country code). Pure — phonenumbers embeds its
// metadata and performs no I/O.
func isValidPhone(candidate string) bool {
	num, err := phonenumbers.Parse(candidate, "")
	return err == nil && phonenumbers.IsValidNumber(num)
}

func findPhone(text string, _ Context) []Span {
	var spans []Span
	i := 0
	for i < len(text) {
		if byteAt(text, i) != '+' || !isDigitAt(text, i+1) || isWordCharAt(text, i-1) {
			i++
			continue
		}
		// Collect digit groups. ends[g] is the index just past group g (and its `)` if any).
		var ends, counts []int
		pos := i + 1
		digits := 0
		for {
			save := pos
			if len(ends) > 0 {
				sep := byteAt(text, pos)
				if sep == ' ' || sep == '.' || sep == '-' {
					pos++
				}
			}
			paren := false
			if byteAt(text, pos) == '(' {
				pos++
				paren = true
			}
			if !isDigitAt(text, pos) {
				pos = save
				break
			}
			start := pos
			for isDigitAt(text, pos) {
				pos++
			}
			n := pos - start
			digits += n
			if paren && byteAt(text, pos) == ')' {
				pos++
			}
			ends = append(ends, pos)
			counts = append(counts, n)
			if digits > phoneMaxDigits || len(ends) >= phoneMaxGroups {
				break
			}
		}
		matched := false
		total := digits
		for e := len(ends) - 1; e >= 0; e-- {
			if e < len(ends)-1 {
				total -= counts[e+1]
			}
			if total < phoneMinDigits {
				break
			}
			end := ends[e]
			if total > phoneMaxDigits || isWordCharAt(text, end) {
				continue
			}
			if isValidPhone(text[i:end]) {
				spans = append(spans, Span{Start: i, End: end})
				i = end
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
