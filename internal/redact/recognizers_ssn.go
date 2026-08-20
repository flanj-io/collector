// SSN recognizer. US SSN `###-##-####`. An SSN has no checksum, so this recognizer is
// FORMAT-anchored by definition: the candidate is confirmed with govalidator.IsSSN
// (the same format rule; the TS mirror relies on the shape alone). A bare 9-digit run
// is deliberately NOT a candidate — that is a common id shape and would over-redact.
// Anchored against word characters on both sides (a dash neighbour is allowed so
// `ssn-123-45-6789` is still caught). Mirrors recognizers/ssn.ts. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

import (
	"regexp"

	"github.com/asaskevich/govalidator"
)

var ssnCandidateRe = regexp.MustCompile(`[0-9]{3}-[0-9]{2}-[0-9]{4}`)

func findSSN(text string, _ Context) []Span {
	var spans []Span
	for _, loc := range ssnCandidateRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		if isWordCharAt(text, start-1) || isWordCharAt(text, end) {
			continue
		}
		if !govalidator.IsSSN(text[start:end]) {
			continue
		}
		spans = append(spans, Span{Start: start, End: end})
	}
	return spans
}
