// ASCII character classes used for recognizer ANCHORING. Candidates are anchored
// against "word" characters so a sensitive run glued to letters/digits/underscore
// inside an identifier is not a candidate, and a digit run is never split. Kept
// ASCII-only and evaluated on BYTES so this file is byte-identical to the TS
// chars.ts (which evaluates UTF-16 code units: a non-ASCII unit is never a word char
// there, and a non-ASCII byte is never a word char here). `i` may be out of range;
// that counts as a boundary. Part of the floor mirrored by
// sdk/packages/redaction-patterns; contract = contracts/redaction-*.json.
package redact

func isDigitAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9'
}

func isLetterAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isAlnumAt(s string, i int) bool {
	return isDigitAt(s, i) || isLetterAt(s, i)
}

// isWordCharAt is `[A-Za-z0-9_]` — the anchoring class. Out of range => false (a boundary).
func isWordCharAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	return isAlnumAt(s, i) || s[i] == '_'
}

// byteAt returns the byte at i, or 0 when out of range (mirrors chars.ts charAt
// returning ”; callers only compare against ASCII punctuation, never against 0).
func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

// asciiFold expands a lowercase ASCII literal into a regex that matches it
// case-insensitively using explicit `[xX]` classes. We deliberately do NOT use Go's
// `(?i)`: RE2 applies Unicode simple case folding (so `s` would also match U+017F
// LATIN SMALL LETTER LONG S and `k` the KELVIN SIGN), whereas the TS patterns use the
// JS `i` flag without `u`, which never folds a non-ASCII character onto an ASCII one.
// Explicit classes make the two engines accept the same bytes.
func asciiFold(lit string) string {
	out := make([]byte, 0, len(lit)*4)
	for i := 0; i < len(lit); i++ {
		c := lit[i]
		if c >= 'a' && c <= 'z' {
			out = append(out, '[', c, c-'a'+'A', ']')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}

// jsSpace is the JS `\s` class (WhiteSpace + LineTerminator, ES2024) spelled out for
// RE2, whose `\s` is ASCII-only: \t \n \v \f \r space U+00A0 U+1680 U+2000–U+200A
// U+2028 U+2029 U+202F U+205F U+3000 U+FEFF. Used wherever the TS source uses `\s`
// so both languages accept exactly the same separators.
const jsSpace = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

// isJSSpace reports whether r is in the JS `\s` class (see jsSpace).
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
