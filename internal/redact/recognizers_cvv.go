// CVV recognizer. CVV is CONTEXTUAL: a bare 3–4 digit number is never a CVV. Two modes:
//   - key mode (structured traversal gave us the key): the whole value is redacted when
//     the key is a CVV key and the value is a 3–4 digit shape;
//   - text mode (no key): `cvv=123` / `cvc: 456` / `"cvv":"789"` forms, digits span only.
//
// Mirrors recognizers/cvv.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

import "regexp"

// cvvKeyRe: keys whose value is a card verification code. Case-insensitive (ASCII
// folding, see asciiFold); optional `card_`/`card-` prefix. (`cid` is deliberately
// excluded — it is overwhelmingly "client/customer id".)
var cvvKeyRe = regexp.MustCompile(`^(?:` + asciiFold("card") + `[_-]?)?(?:` +
	asciiFold("cvv") + `2?|` + asciiFold("cvc") + `2?|` + asciiFold("csc") + `|` +
	asciiFold("security") + `[_-]?` + asciiFold("code") + `)$`)

// cvvTextRe: `cvv=123`, `cvc: 456`, `"cvv": "789"` inside free text / form bodies /
// malformed JSON. Group 1 = key, group 2 = separator, group 3 = digits. `\b` is an
// ASCII word boundary in both RE2 and non-unicode JS; `\s` is spelled out as jsSpace.
var cvvTextRe = regexp.MustCompile(`\b((?:` + asciiFold("card") + `[_-]?)?(?:` +
	asciiFold("cvv") + `2?|` + asciiFold("cvc") + `2?|` + asciiFold("csc") + `|` +
	asciiFold("security") + `[_-]?` + asciiFold("code") + `))("?` + jsSpace + `*[:=]` + jsSpace + `*"?)([0-9]{3,4})`)

// IsCvvKey reports whether key names a card verification code field. Shared with the
// JSON number path (numbers.go).
func IsCvvKey(key string) bool { return cvvKeyRe.MatchString(key) }

// isCvvShape reports whether value is exactly a 3–4 digit CVV shape.
func isCvvShape(value string) bool {
	if len(value) < 3 || len(value) > 4 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isDigitAt(value, i) {
			return false
		}
	}
	return true
}

func findCVV(text string, ctx Context) []Span {
	if ctx.HasKey && IsCvvKey(ctx.Key) && isCvvShape(text) {
		return []Span{{Start: 0, End: len(text)}}
	}
	// Otherwise (no key, non-CVV key, or a non-shape value) scan for textual forms.
	var spans []Span
	for _, m := range cvvTextRe.FindAllStringSubmatchIndex(text, -1) {
		end := m[1]
		digitsLen := m[7] - m[6]
		if isDigitAt(text, end) {
			continue // 5+ digits is not a CVV shape
		}
		spans = append(spans, Span{Start: end - digitsLen, End: end})
	}
	return spans
}
