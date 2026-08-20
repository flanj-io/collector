// Base64 decode-then-scan support (one of the floor's owned responsibilities).
//
// Locates runs that LOOK like base64 (≥ minBase64Run chars of one base64 alphabet,
// optional `=` padding), decodes them, and hands back the decoded TEXT when — and only
// when — it is valid, printable UTF-8. Binary blobs, hashes and ordinary long words
// decode to non-text and are never scanned. The caller (scalar.go) runs the
// recognizers over the decoded text and, on a hit, redacts the WHOLE encoded run.
// Depth is 1 (no base64-in-base64).
//
// The shape rules and decode semantics mirror base64.ts exactly: lenient about missing
// padding and trailing bits (Go's non-Strict RawStdEncoding/RawURLEncoding, like Node's
// Buffer.from), strict about the alphabet and `len % 4 == 1`. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

import (
	"encoding/base64"
	"regexp"
	"strings"
	"unicode/utf8"
)

const minBase64Run = 20

var base64RunRe = regexp.MustCompile(`[A-Za-z0-9+/_-]{20,}={0,2}`)

// base64Run is one decodable run: [Start, End) in the scanned segment, plus its text.
type base64Run struct {
	Start, End int
	Decoded    string
}

// decodeBase64Text decodes one candidate run; ok is false when it is not base64 text.
func decodeBase64Text(run string) (string, bool) {
	body := strings.TrimRight(run, "=")
	if len(body) < minBase64Run || len(body)%4 == 1 {
		return "", false
	}
	std := strings.ContainsAny(body, "+/")
	url := strings.ContainsAny(body, "_-")
	if std && url {
		return "", false
	}
	enc := base64.RawStdEncoding
	if url {
		enc = base64.RawURLEncoding
	}
	bytes, err := enc.DecodeString(body)
	if err != nil {
		return "", false
	}
	// Node silently drops invalid input; guard that the whole run was consumed.
	if len(bytes) != len(body)*3/4 {
		return "", false
	}
	if !utf8.Valid(bytes) {
		return "", false
	}
	// Printable text only: no control characters except \t \n \r, no DEL. These are all
	// single ASCII bytes, so a byte walk equals the TS code-unit walk.
	for _, c := range bytes {
		if c < 0x20 && c != 0x09 && c != 0x0a && c != 0x0d {
			return "", false
		}
		if c == 0x7f {
			return "", false
		}
	}
	return string(bytes), true
}

// findBase64Runs returns every base64-text run in text, left to right, non-overlapping.
func findBase64Runs(text string) []base64Run {
	var runs []base64Run
	for _, loc := range base64RunRe.FindAllStringIndex(text, -1) {
		if decoded, ok := decodeBase64Text(text[loc[0]:loc[1]]); ok {
			runs = append(runs, base64Run{Start: loc[0], End: loc[1], Decoded: decoded})
		}
	}
	return runs
}
