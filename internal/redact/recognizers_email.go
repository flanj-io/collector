// EMAIL recognizer. Email-shaped candidate: local part, `@`, dotted domain with an
// alphabetic TLD (≥ 2). Shape only — govalidator.IsEmail (a pure regex grammar; the TS
// mirror uses validator's isEmail on the identical shape) DECIDES. Mirrors
// recognizers/email.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

import (
	"regexp"

	"github.com/asaskevich/govalidator"
)

var emailCandidateRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// isEmailLeadingTrim: characters the candidate shape allows at the start but an
// address cannot begin with.
func isEmailLeadingTrim(c byte) bool {
	return c == '.' || c == '_' || c == '%' || c == '+' || c == '-'
}

func findEmail(text string, _ Context) []Span {
	var spans []Span
	for _, loc := range emailCandidateRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		for start < end && isEmailLeadingTrim(text[start]) {
			start++
		}
		candidate := text[start:end]
		if len(candidate) > 0 && govalidator.IsEmail(candidate) {
			spans = append(spans, Span{Start: start, End: end})
		}
	}
	return spans
}
