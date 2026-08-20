// The swappable Recognizer interface: one pattern of the floor LOCATES candidates and
// lets a hardened validator DECIDE; it returns confirmed spans only. Mirrors
// sdk/packages/redaction-patterns/src/recognizer.ts. Everything structural above a
// recognizer (deep traversal, token protection, base64 decode-then-scan, token format,
// report order, fail-closed/no-I/O) is owned by this package, not by the recognizer,
// so swapping one recognizer never touches the rest of the floor. The contract for
// the whole is contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

// Span is a half-open [Start, End) range inside a single scalar string.
//
// Offsets are BYTE offsets into the Go string (the TS mirror uses UTF-16 code-unit
// offsets). This is internal to the engine: spans are only ever spliced back into
// the same scalar they were found in, so the resulting strings are identical in both
// languages.
type Span struct {
	Start, End int
}

// Context says where a scalar sits in its enclosing structure. Key is the
// (original, un-redacted) object key whose value is being scanned; HasKey is false
// for keys, array elements and free text. Only contextual recognizers (CVV) use it.
type Context struct {
	Key    string
	HasKey bool
}

// Recognizer is one pattern of the floor. Find returns the CONFIRMED sensitive spans
// inside a single scalar string — confirmed meaning a hardened validator (Luhn,
// mod-97, email grammar, phone metadata) said yes; our code only LOCATES candidates,
// it never decides by regex.
//
// Contract for implementations (mirrored byte-for-byte with the TS package):
//   - spans are sorted by Start and non-overlapping (the engine normalises
//     defensively anyway);
//   - Find is pure and must never perform I/O;
//   - the engine behind a recognizer is swappable: replacing one recognizer must not
//     touch traversal, token format, base64 or gating.
type Recognizer interface {
	ID() string
	Find(value string, ctx Context) []Span
}
