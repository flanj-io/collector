// The two cases in which a NUMBER (a JSON number literal on the text path, or a number
// value on the structural path) is redacted — everything else numeric is left
// untouched:
//   - a 3–4 digit integer under a CVV key  -> CVV;
//   - a 13–19 digit integer that passes Luhn -> PAN (a PAN sent as a bare number).
//
// Mirrors numbers.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// classifyIntegerDigits returns the pattern id that fires for an integer literal
// whose digit string (no sign, no fraction, no exponent) is digits, under ctx; "" when
// nothing fires. Sign, fraction and exponent make a literal not an integer => the
// caller never reaches here.
func classifyIntegerDigits(digits string, ctx Context) string {
	if len(digits) == 0 {
		return ""
	}
	for i := 0; i < len(digits); i++ {
		if !isDigitAt(digits, i) {
			return ""
		}
	}
	if len(digits) >= 3 && len(digits) <= 4 && ctx.HasKey && IsCvvKey(ctx.Key) {
		return CVV
	}
	if len(digits) >= 13 && len(digits) <= 19 && luhn(digits) {
		return PAN
	}
	return ""
}

// integerDigitsOfFloat is the digit string of a float when it is a safe-to-print
// integer (mirrors numbers.ts integerDigitsOf for a JS number): finite, integral and
// |n| < 1e21 — above that String(n) switches to exponent form in JS, so the TS floor
// never sees digits and neither do we. FormatFloat(abs, 'f', -1) prints an integral
// float as plain digits exactly like String(abs).
func integerDigitsOfFloat(n float64) (string, bool) {
	if math.IsInf(n, 0) || math.IsNaN(n) || n != math.Trunc(n) {
		return "", false
	}
	abs := math.Abs(n)
	if abs >= 1e21 {
		return "", false
	}
	return strconv.FormatFloat(abs, 'f', -1, 64), true
}

// integerDigitsOfNumber is the digit string of a json.Number when it is an integer
// literal (no '.', 'e' or 'E'), without its leading '-'.
func integerDigitsOfNumber(n json.Number) (string, bool) {
	s := string(n)
	if strings.ContainsAny(s, ".eE") {
		return "", false
	}
	return strings.TrimPrefix(s, "-"), true
}
