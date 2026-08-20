// Redaction token format, pattern ids and the canonical report order.
//
// Delimiters are U+27E6 / U+27E7 (⟦ ⟧) — regex-stable, won't collide with JSON or
// text, and make emitted tokens inert to re-scanning (idempotency / never
// double-wrap). Mirrors sdk/packages/redaction-patterns/src/tokens.ts and
// report-order.ts byte-for-byte; the contract is contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

import "regexp"

// Token delimiters are U+27E6 / U+27E7 (⟦ ⟧) — regex-stable, JSON-safe.
const (
	tokenOpen  = "⟦" // ⟦
	tokenClose = "⟧" // ⟧
)

// Pattern ids (stable — surfaced in vinifera.redaction.patterns).
const (
	PAN   = "PAN"
	EMAIL = "EMAIL"
	IBAN  = "IBAN"
	SSN   = "SSN"
	PHONE = "PHONE"
	CVV   = "CVV"
	TOKEN = "TOKEN"
	IP    = "IP"
)

// ReportOrder is the canonical order in which fired pattern ids are reported in
// Result.Patterns / RedactValue hits (and thence vinifera.redaction.patterns).
// Matches the ordering asserted by the golden vectors (e.g. combined bodies report
// ["PAN","EMAIL","CVV"]). Independent of the APPLICATION order in DefaultRecognizers.
var ReportOrder = []string{PAN, EMAIL, IBAN, SSN, PHONE, CVV, TOKEN, IP}

// Token builds the redaction token for a pattern id, e.g. Token("PAN") == "⟦REDACTED:PAN⟧".
func Token(id string) string { return tokenOpen + "REDACTED:" + id + tokenClose }

// tokenRe matches any already-emitted token on the SCALAR engine; such spans are
// protected and never re-scanned (mirrors scalar.ts TOKEN_RE: [A-Z0-9_]+).
var tokenRe = regexp.MustCompile(`\x{27e6}REDACTED:[A-Z0-9_]+\x{27e7}`)

// reportedTokenRe matches any already-emitted floor token, e.g. ⟦REDACTED:PAN⟧
// (mirrors tokens.ts REDACTED_TOKEN_RE: [A-Z]+). Used by the enhancer to prove a
// scalar already carries a token, and by the tests to assert the never-subtract law;
// never used to un-redact.
var reportedTokenRe = regexp.MustCompile(`\x{27e6}REDACTED:[A-Z]+\x{27e7}`)

// inReportOrder returns the fired ids, deduplicated, in canonical ReportOrder.
// Like the TS `REPORT_ORDER.filter((id) => fired.has(id))`, an id that is not a
// floor pattern id (only reachable through a custom Recognizer) is not reported.
func inReportOrder(fired map[string]bool) []string {
	out := make([]string, 0, len(fired))
	for _, id := range ReportOrder {
		if fired[id] {
			out = append(out, id)
		}
	}
	return out
}
