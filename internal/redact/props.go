// Captured properties of a redacted value — computed from the ORIGINAL scalar, at the
// only moment it still exists, so downstream consumers (the drift detector) can validate
// the DECIDABLE spec constraints of a redacted field (type, min/maxLength) instead of
// skipping it. NON-REVERSIBLE by design: these are coarse schema-level facts; never add
// anything that narrows the value (no prefixes/suffixes, no entropy, no samples).
//
// Every definition here is part of the cross-language contract (mirrors props.ts; the
// fixture battery asserts byte-identical output):
//   - Length counts UNICODE CODE POINTS of the original scalar text (JS `[...s]`,
//     Go for-range runes) — NOT UTF-16 units, NOT bytes;
//   - lower/upper/digit classes are ASCII (a-z / A-Z / 0-9);
//   - control = code point <= 0x1F or == 0x7F; printable = 0x20-0x7E; extended = > 0x7F.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// ValueProps are the captured properties of an original scalar. Integer is a *bool so
// string props marshal WITHOUT the key (the TS mirror only sets `integer` for numbers).
type ValueProps struct {
	// Type is "string" or "number".
	Type string `json:"type"`
	// Length is the code-point count of the original scalar text (a number's literal).
	Length int `json:"length"`
	// Integer (numbers only): the literal had no fraction/exponent.
	Integer                     *bool `json:"integer,omitempty"`
	ContainsLowerCase           bool  `json:"containsLowerCase"`
	ContainsUpperCase           bool  `json:"containsUpperCase"`
	ContainsDigits              bool  `json:"containsDigits"`
	ContainsASCIIControlChars   bool  `json:"containsASCIIControlChars"`
	ContainsASCIIPrintableChars bool  `json:"containsASCIIPrintableChars"`
	ContainsASCIIExtendedChars  bool  `json:"containsASCIIExtendedChars"`
}

// RedactedField is one whole-value redaction: where, what fired, and the original's
// properties.
type RedactedField struct {
	// Path is an RFC 6901 JSON Pointer into the scanned value ("" = the root scalar).
	Path string `json:"path"`
	// Pattern is the id of the emitted token.
	Pattern string     `json:"pattern"`
	Props   ValueProps `json:"props"`
}

// ComputeProps computes the props of an original scalar. text is the scalar's text (a
// number's literal, sign included); kind is "string" or "number"; integer is only
// meaningful for numbers (mirrors computeProps in props.ts: two independent else-if
// chains, evaluated per code point).
func ComputeProps(text string, kind string, integer bool) ValueProps {
	props := ValueProps{Type: kind}
	if kind == "number" {
		i := integer
		props.Integer = &i
	}
	for _, c := range text {
		props.Length++
		if c >= 0x61 && c <= 0x7a {
			props.ContainsLowerCase = true
		} else if c >= 0x41 && c <= 0x5a {
			props.ContainsUpperCase = true
		} else if c >= 0x30 && c <= 0x39 {
			props.ContainsDigits = true
		}
		if c <= 0x1f || c == 0x7f {
			props.ContainsASCIIControlChars = true
		} else if c <= 0x7e {
			props.ContainsASCIIPrintableChars = true
		} else {
			props.ContainsASCIIExtendedChars = true
		}
	}
	return props
}

// EscapePointerSegment is RFC 6901 segment escaping: `~` -> `~0`, `/` -> `~1`.
func EscapePointerSegment(segment string) string {
	if !strings.ContainsAny(segment, "~/") {
		return segment
	}
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

// wholeTokenRe matches a scalar that is EXACTLY one emitted token; group 1 = the
// pattern id (mirrors WHOLE_TOKEN_RE in props.ts).
var wholeTokenRe = regexp.MustCompile(`^\x{27e6}REDACTED:([A-Z0-9_]+)\x{27e7}$`)

// WholeTokenID returns the pattern id when value is exactly one token.
func WholeTokenID(value string) (string, bool) {
	m := wholeTokenRe.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// SortFields sorts fields in place into the canonical order: by Path, byte-wise
// (fields never share a path). Returns the slice for convenience.
func SortFields(fields []RedactedField) []RedactedField {
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	return fields
}
