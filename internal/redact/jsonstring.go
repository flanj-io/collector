// Canonical JSON string encoding for REWRITTEN scalars on the text path. Escapes only
// what JSON requires (`"`, `\`, control chars < 0x20 — short forms for \b \f \n \r \t,
// lowercase `\u00xx` otherwise); everything else, including the token glyphs ⟦ ⟧ and
// any non-ASCII, is written raw. This is byte-identical to JSON.stringify for
// well-formed text and to json-string.ts, so a rewritten literal is the same bytes in
// both languages. Untouched literals are never re-encoded at all. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

import "strings"

const hexDigits = "0123456789abcdef"

// encodeJSONString returns s as a quoted JSON string literal (see file comment).
func encodeJSONString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c >= 0x20:
			b.WriteByte(c) // raw, including every byte of a multi-byte UTF-8 sequence
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		default:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		}
	}
	b.WriteByte('"')
	return b.String()
}
