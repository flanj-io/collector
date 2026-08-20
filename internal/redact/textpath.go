// The TEXT entry point's traversal: how a captured body STRING is redacted.
//
// Bodies arrive as strings (the SDK's capped buffer, the collector's OTLP attribute,
// the control plane's reply box). Rather than parse → clone → re-serialize — which
// would reorder keys / reformat numbers differently in JS and Go and destroy formatting
// — the text path SCANS the text and rewrites ONLY the scalars that fired, in place:
//
//   - JSON (first non-space byte `{` or `[`): a tolerant scanner walks the text
//     tracking object/array nesting and the current key; every string literal is
//     decoded, scanned (keys too, values with their key as context), and re-encoded
//     canonically only if it changed; number literals are checked for CVV-under-key /
//     PAN-as-number; anything the scanner does not understand (malformed or truncated
//     bodies) is scanned as plain text as "residue" — so EVERY byte of the body is
//     scanned by some path and truncation at the capture cap never hides a scalar.
//   - form-urlencoded (`k=v&k=v`): each key and value is percent-decoded, scanned
//     (values with their key as context, so `cvv=123` is contextual and
//     `email=jane%40x.com` is seen as an address), and re-encoded minimally only if it
//     changed.
//   - anything else: one scalar.
//
// This is the byte-for-byte mirror of text-path.ts: the TS scanner walks UTF-16 code
// units and this one walks bytes, which is equivalent because every structural
// character is ASCII and every non-structural byte is copied verbatim. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type frameKind uint8

const (
	frameObj frameKind = iota
	frameArr
)

type frameState uint8

const (
	stateKey frameState = iota
	stateColon
	stateValue
	stateComma
)

// frame is one level of JSON nesting in the scanner.
type frame struct {
	kind frameKind
	// For objects: what the scanner expects next.
	state frameState
	// The original (un-redacted) current key, once read.
	key    string
	hasKey bool
	// For arrays: the index of the value currently being read.
	index int
}

// isJSONWS is the scanner's whitespace set (' ', \t, \n, \r).
func isJSONWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isStructural is the set of bytes that end a residue run: `"{}[]:,`.
func isStructural(c byte) bool {
	switch c {
	case '"', '{', '}', '[', ']', ':', ',':
		return true
	}
	return false
}

// redactTextPath is the text entry point (see file comment). It returns the rewritten
// text plus the whole-value redaction field records (JSON scanner + root scalar only;
// unsorted — the caller sorts), and marks every pattern id that fired in fired.
func redactTextPath(text string, recognizers []Recognizer, fired map[string]bool) (string, []RedactedField) {
	var fields []RedactedField
	if len(text) == 0 {
		return text, fields
	}
	i := 0
	for i < len(text) && isJSONWS(text[i]) {
		i++
	}
	first := byteAt(text, i)
	if first == '{' || first == '[' {
		out := scanJSON(text, recognizers, fired, &fields)
		return out, fields
	}
	// Form pairs carry no fields (spec-addressable form bodies are a later enhancement).
	if isFormBody(text) {
		return scanForm(text, recognizers, fired), fields
	}
	out := redactScalar(text, Context{}, recognizers, fired)
	// A top-level scalar body wholly redacted reports the RFC 6901 root path "".
	if out != text {
		if id, ok := WholeTokenID(out); ok {
			fields = append(fields, RedactedField{Path: "", Pattern: id, Props: ComputeProps(text, "string", false)})
		}
	}
	return out, fields
}

// --- JSON ----------------------------------------------------------------------------

var numberLiteralRe = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?`)

func scanJSON(text string, recognizers []Recognizer, fired map[string]bool, fields *[]RedactedField) string {
	n := len(text)
	var stack []*frame
	top := func() *frame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	expectsValue := func() bool {
		t := top()
		return t == nil || t.kind == frameArr || t.state == stateValue
	}
	valueDone := func() {
		if t := top(); t != nil && t.kind == frameObj {
			t.state = stateComma
		}
	}
	// currentPath is the RFC 6901 pointer to the value currently being read (object
	// keys from frames, array indices from each array frame's counter).
	currentPath := func() string {
		var b strings.Builder
		for _, f := range stack {
			b.WriteByte('/')
			if f.kind == frameArr {
				b.WriteString(strconv.Itoa(f.index))
			} else {
				b.WriteString(EscapePointerSegment(f.key))
			}
		}
		return b.String()
	}

	var out strings.Builder
	out.Grow(n)
	i := 0
	for i < n {
		c := text[i]
		if isJSONWS(c) {
			out.WriteByte(c)
			i++
			continue
		}
		if c == '{' || c == '[' {
			if c == '{' {
				stack = append(stack, &frame{kind: frameObj, state: stateKey})
			} else {
				stack = append(stack, &frame{kind: frameArr, state: stateValue})
			}
			out.WriteByte(c)
			i++
			continue
		}
		if c == '}' || c == ']' {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			valueDone()
			out.WriteByte(c)
			i++
			continue
		}
		if c == ':' || c == ',' {
			if t := top(); t != nil {
				if t.kind == frameObj {
					if c == ':' {
						t.state = stateValue
					} else {
						t.state = stateKey
					}
				} else if c == ',' {
					t.index++
				}
			}
			out.WriteByte(c)
			i++
			continue
		}
		if c == '"' {
			// Find the closing quote, honouring escapes.
			j := i + 1
			for j < n {
				cj := text[j]
				if cj == '\\' {
					j += 2
				} else if cj == '"' {
					break
				} else {
					j++
				}
			}
			if j >= n {
				// Unterminated (truncated body): scan the remainder as plain text.
				out.WriteString(redactScalar(text[i:], Context{}, recognizers, fired))
				i = n
				break
			}
			literal := text[i : j+1]
			var decoded string
			if err := json.Unmarshal([]byte(literal), &decoded); err != nil {
				out.WriteString(redactScalar(literal, Context{}, recognizers, fired))
				i = j + 1
				continue
			}
			t := top()
			isKey := t != nil && t.kind == frameObj && t.state == stateKey
			ctx := Context{}
			valuePath := ""
			isValue := false
			if isKey {
				t.key = decoded
				t.hasKey = true
				t.state = stateColon
			} else {
				if t != nil && t.kind == frameObj && t.hasKey {
					ctx = Context{Key: t.key, HasKey: true}
				}
				// The pointer must be read BEFORE valueDone() (an array frame's index
				// only moves on ',', but the obj state flip is part of the same step).
				valuePath = currentPath()
				isValue = true
				valueDone()
			}
			redacted := redactScalar(decoded, ctx, recognizers, fired)
			// Redacted KEYS carry no field record — a key is not a spec-addressable value.
			if isValue && redacted != decoded {
				if id, ok := WholeTokenID(redacted); ok {
					*fields = append(*fields, RedactedField{Path: valuePath, Pattern: id, Props: ComputeProps(decoded, "string", false)})
				}
			}
			if redacted == decoded {
				out.WriteString(literal)
			} else {
				out.WriteString(encodeJSONString(redacted))
			}
			i = j + 1
			continue
		}
		if expectsValue() && (c == '-' || isDigitAt(text, i)) {
			if lit := numberLiteralRe.FindString(text[i:]); lit != "" {
				ctx := Context{}
				if t := top(); t != nil && t.kind == frameObj && t.hasKey {
					ctx = Context{Key: t.key, HasKey: true}
				}
				id := ""
				if isIntegerLiteral(lit) {
					id = classifyIntegerDigits(strings.Replace(lit, "-", "", 1), ctx)
				}
				if id != "" {
					fired[id] = true
					*fields = append(*fields, RedactedField{Path: currentPath(), Pattern: id, Props: ComputeProps(lit, "number", true)})
					out.WriteString(encodeJSONString(Token(id)))
				} else {
					out.WriteString(lit)
				}
				valueDone()
				i += len(lit)
				continue
			}
		}
		if expectsValue() {
			lit := ""
			for _, w := range [...]string{"true", "false", "null"} {
				if strings.HasPrefix(text[i:], w) {
					lit = w
					break
				}
			}
			if lit != "" {
				out.WriteString(lit)
				valueDone()
				i += len(lit)
				continue
			}
		}
		// Residue: anything else, up to the next structural character — scanned as text.
		j := i + 1
		for j < n && !isStructural(text[j]) {
			j++
		}
		out.WriteString(redactScalar(text[i:j], Context{}, recognizers, fired))
		i = j
	}
	return out.String()
}

// isIntegerLiteral: no fraction, no exponent.
func isIntegerLiteral(lit string) bool { return !strings.ContainsAny(lit, ".eE") }

// --- form-urlencoded -----------------------------------------------------------------

// isFormBody: `k=v&k=v…` — no whitespace at all (a real form body encodes spaces as
// `+`/`%20`; "whitespace" is the JS `\s` class, see isJSSpace), at most one `=` per
// pair, no empty key, no quote inside a key (a quoted JSON string body is not a form),
// and not a lone `blob=` (a base64 value's padding).
func isFormBody(text string) bool {
	if !strings.Contains(text, "=") {
		return false
	}
	for _, r := range text {
		if isJSSpace(r) {
			return false
		}
	}
	pairs := strings.Split(text, "&")
	for _, pair := range pairs {
		eq := strings.IndexByte(pair, '=')
		if eq == 0 {
			return false
		}
		if eq >= 0 && strings.IndexByte(pair[eq+1:], '=') >= 0 {
			return false
		}
		// A form key never contains a quote (a quoted JSON string body is not a form).
		key := pair
		if eq >= 0 {
			key = pair[:eq]
		}
		if strings.IndexByte(key, '"') >= 0 {
			return false
		}
	}
	if len(pairs) == 1 && strings.HasSuffix(pairs[0], "=") {
		return false
	}
	return true
}

func scanForm(text string, recognizers []Recognizer, fired map[string]bool) string {
	pairs := strings.Split(text, "&")
	out := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			decoded := formDecode(pair)
			red := redactScalar(decoded, Context{}, recognizers, fired)
			if red == decoded {
				out = append(out, pair)
			} else {
				out = append(out, formEncode(red))
			}
			continue
		}
		rawKey := pair[:eq]
		rawVal := pair[eq+1:]
		key := formDecode(rawKey)
		val := formDecode(rawVal)
		redKey := redactScalar(key, Context{}, recognizers, fired)
		redVal := redactScalar(val, Context{Key: key, HasKey: true}, recognizers, fired)
		k, v := rawKey, rawVal
		if redKey != key {
			k = formEncode(redKey)
		}
		if redVal != val {
			v = formEncode(redVal)
		}
		out = append(out, k+"="+v)
	}
	return strings.Join(out, "&")
}

func isHexAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// formDecode: `+` → space, `%XX` → byte; malformed escapes pass through. The
// resulting bytes are decoded leniently as UTF-8: an invalid sequence becomes U+FFFD
// (strings.ToValidUTF8 replaces each maximal run of invalid bytes with ONE U+FFFD,
// whereas the TS mirror's non-fatal TextDecoder emits one per maximal subpart; the
// two only differ for malformed percent-encoded bytes that are then rewritten).
func formDecode(s string) string {
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '+':
			buf = append(buf, ' ')
		case c == '%' && isHexAt(s, i+1) && isHexAt(s, i+2):
			buf = append(buf, hexVal(s[i+1])<<4|hexVal(s[i+2]))
			i += 2
		default:
			buf = append(buf, c)
		}
	}
	return strings.ToValidUTF8(string(buf), "�")
}

// formEncode is the minimal re-encoding of a REWRITTEN form key/value: only the
// characters that would break the `k=v&k=v` structure are escaped (`%`, `&`, `=`, `+`,
// CR, LF) and spaces become `+`; everything else — including the token glyphs — is
// written raw.
func formEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ':
			b.WriteByte('+')
		case '%':
			b.WriteString("%25")
		case '&':
			b.WriteString("%26")
		case '=':
			b.WriteString("%3D")
		case '+':
			b.WriteString("%2B")
		case '\r':
			b.WriteString("%0D")
		case '\n':
			b.WriteString("%0A")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
