package redact

// Behaviour NOT pinned by the cross-language fixtures (which are the contract): the
// optional IP recognizer, the swappable-interface mechanics, and recognizer edge
// cases that document deliberate choices — the Go twin of the TS recognizers.spec.ts.
// Anything that must hold in TS too belongs in contracts/redaction-fixtures.json,
// not here.

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func spansEqual(t *testing.T, got, want []Span) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans mismatch\n got:  %+v\n want: %+v", got, want)
	}
}

func TestPANRecognizer(t *testing.T) {
	// Confirmed spans only (Luhn-gated) with original-span offsets.
	spansEqual(t, PANRecognizer.Find("a 4111 1111 1111 1111 b 1111111111111111 c", Context{}),
		[]Span{{Start: 2, End: 21}})

	// Anchored against word characters on both sides.
	spansEqual(t, PANRecognizer.Find("x4111111111111111", Context{}), nil)
	spansEqual(t, PANRecognizer.Find("4111111111111111x", Context{}), nil)
	spansEqual(t, PANRecognizer.Find("_4111111111111111", Context{}), nil)
	spansEqual(t, PANRecognizer.Find("(4111111111111111)", Context{}), []Span{{Start: 1, End: 17}})

	// Never more than five groups as one card (digit lists are not PANs):
	// 16 single-digit groups — a valid Luhn sequence but not a PAN format.
	spansEqual(t, PANRecognizer.Find("4 1 1 1 1 1 1 1 1 1 1 1 1 1 1 1", Context{}), nil)
}

func TestEmailRecognizer(t *testing.T) {
	// Trims leading punctuation and validates.
	spansEqual(t, EmailRecognizer.Find("see ...jane@example.com", Context{}), []Span{{Start: 7, End: 23}})
	spansEqual(t, EmailRecognizer.Find("not-an-email@", Context{}), nil)
	spansEqual(t, EmailRecognizer.Find("a@b", Context{}), nil)
}

func TestIBANRecognizer(t *testing.T) {
	// Accepts lowercase; rejects a wrong country or checksum.
	spansEqual(t, IBANRecognizer.Find("de89370400440532013000", Context{}), []Span{{Start: 0, End: 22}})
	spansEqual(t, IBANRecognizer.Find("ZZ89370400440532013000", Context{}), nil)
	spansEqual(t, IBANRecognizer.Find("DE00370400440532013000", Context{}), nil)
}

func TestPhoneRecognizer(t *testing.T) {
	// Requires a + country code and validates against metadata.
	spansEqual(t, PhoneRecognizer.Find("+14155552671", Context{}), []Span{{Start: 0, End: 12}})
	spansEqual(t, PhoneRecognizer.Find("14155552671", Context{}), nil)
	spansEqual(t, PhoneRecognizer.Find("+1234", Context{}), nil)
	spansEqual(t, PhoneRecognizer.Find("x+14155552671", Context{}), nil)
}

func TestCVVRecognizerContextual(t *testing.T) {
	// Key mode.
	spansEqual(t, CVVRecognizer.Find("123", Context{Key: "cvv", HasKey: true}), []Span{{Start: 0, End: 3}})
	spansEqual(t, CVVRecognizer.Find("123", Context{Key: "retries", HasKey: true}), nil)
	spansEqual(t, CVVRecognizer.Find("123", Context{}), nil)
	spansEqual(t, CVVRecognizer.Find("the cvv was 123 today", Context{Key: "note", HasKey: true}), nil)
	// Text mode.
	spansEqual(t, CVVRecognizer.Find("cvv: 123", Context{}), []Span{{Start: 5, End: 8}})
	spansEqual(t, CVVRecognizer.Find("cvv: 12345", Context{}), nil)
}

func TestTokenRecognizerJWT(t *testing.T) {
	// Validates the JWT header and keeps the longest overlapping span.
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ4In0.abcdefghij"
	spansEqual(t, TokenRecognizer.Find("Bearer "+jwt, Context{}), []Span{{Start: 7, End: 7 + len(jwt)}})
	spansEqual(t, TokenRecognizer.Find("eyJxxxxxx.yyyyyyyy.zzzzzzzz", Context{}), nil)
}

func TestIPRecognizer(t *testing.T) {
	// Validates octets and v6 shapes.
	spansEqual(t, IPRecognizer.Find("from 10.0.0.1 to 256.1.1.1", Context{}), []Span{{Start: 5, End: 13}})
	spansEqual(t, IPRecognizer.Find("v6 2001:db8::1 time 12:30:45", Context{}), []Span{{Start: 3, End: 14}})
	spansEqual(t, IPRecognizer.Find("version 1.2.3.4.5", Context{}), nil)
}

func TestIPOffByDefaultOnWithOption(t *testing.T) {
	body := map[string]any{"peer": "203.0.113.7"}
	red, hits, fields := New().RedactValue(body)
	if !reflect.DeepEqual(red, body) || len(hits) != 0 || len(fields) != 0 {
		t.Errorf("IP redacted without WithIP: %v %v %v", red, hits, fields)
	}
	red, hits, fields = New(WithIP()).RedactValue(body)
	want := map[string]any{"peer": "⟦REDACTED:IP⟧"}
	if !reflect.DeepEqual(red, want) || !reflect.DeepEqual(hits, []string{IP}) {
		t.Errorf("WithIP did not redact: %v %v", red, hits)
	}
	// A whole-value IP redaction carries the original's captured props (the Go twin
	// of the TS recognizers.spec expectation).
	wantFields := []RedactedField{{
		Path:    "/peer",
		Pattern: IP,
		Props: ValueProps{
			Type:                        "string",
			Length:                      11,
			ContainsDigits:              true,
			ContainsASCIIPrintableChars: true,
		},
	}}
	if !reflect.DeepEqual(fields, wantFields) {
		t.Errorf("WithIP fields mismatch\n got:  %+v\n want: %+v", fields, wantFields)
	}
}

// stubRecognizer is a swapped-in engine for one pattern.
type stubRecognizer struct {
	id   string
	find func(v string, ctx Context) []Span
}

func (s stubRecognizer) ID() string                        { return s.id }
func (s stubRecognizer) Find(v string, ctx Context) []Span { return s.find(v, ctx) }

func TestWithRecognizersSwapsEnginesNotTheFloor(t *testing.T) {
	// Recognizers are swappable behind the interface without touching
	// traversal/base64/tokens.
	shout := stubRecognizer{id: TOKEN, find: func(v string, _ Context) []Span {
		if at := strings.Index(v, "secret"); at >= 0 {
			return []Span{{Start: at, End: at + 6}}
		}
		return nil
	}}
	r := New(WithRecognizers(shout))
	red, hits, _ := r.RedactValue(map[string]any{
		"a": []any{"secret", "plain"},
		"b": map[string]any{"c": "secret"},
	})
	want := map[string]any{
		"a": []any{"⟦REDACTED:TOKEN⟧", "plain"},
		"b": map[string]any{"c": "⟦REDACTED:TOKEN⟧"},
	}
	if !reflect.DeepEqual(red, want) || !reflect.DeepEqual(hits, []string{TOKEN}) {
		t.Errorf("swapped recognizer traversal broken: %v %v", red, hits)
	}
	// base64 decode-then-scan is owned by the wrapper, not the recognizer.
	encoded := base64.StdEncoding.EncodeToString([]byte("the secret is out"))
	if got := r.Redact(encoded).Patterns; !reflect.DeepEqual(got, []string{TOKEN}) {
		t.Errorf("base64 pass not owned by wrapper: %v", got)
	}
}

func TestKeyCollisionDeterminism(t *testing.T) {
	// Keys are redacted too; when two keys collapse into the SAME token the map
	// iterates in sorted order, so the LAST key in sorted order wins deterministically.
	red, hits, fields := New().RedactValue(map[string]any{
		"4111111111111111": "a",
		"4242424242424242": "b",
	})
	want := map[string]any{"⟦REDACTED:PAN⟧": "b"}
	if !reflect.DeepEqual(red, want) || !reflect.DeepEqual(hits, []string{PAN}) {
		t.Errorf("key collision not deterministic: %v %v", red, hits)
	}
	// Redacted KEYS never carry field records.
	if len(fields) != 0 {
		t.Errorf("redacted keys emitted fields: %+v", fields)
	}
}

type cardHolder struct {
	Card string `json:"card"`
	note string
}

func TestStructTraversalViaReflection(t *testing.T) {
	in := cardHolder{Card: "4111111111111111", note: "keep"}
	out, hits, fields := New().RedactValue(in)
	got, ok := out.(cardHolder)
	if !ok {
		t.Fatalf("RedactValue changed the struct type: %T", out)
	}
	if got.Card != "⟦REDACTED:PAN⟧" || got.note != "keep" {
		t.Errorf("struct copy wrong: %+v", got)
	}
	if !reflect.DeepEqual(hits, []string{PAN}) {
		t.Errorf("hits: %v", hits)
	}
	// A struct field's record addresses it by its JSON-TAG path segment (the same
	// key its serialized form would carry).
	wantFields := []RedactedField{{
		Path:    "/card",
		Pattern: PAN,
		Props: ValueProps{
			Type:                        "string",
			Length:                      16,
			ContainsDigits:              true,
			ContainsASCIIPrintableChars: true,
		},
	}}
	if !reflect.DeepEqual(fields, wantFields) {
		t.Errorf("struct fields mismatch\n got:  %+v\n want: %+v", fields, wantFields)
	}
	// The input is untouched (RedactValue works on a copy).
	if in.Card != "4111111111111111" {
		t.Errorf("input struct was mutated: %+v", in)
	}
}

func TestNumbers(t *testing.T) {
	r := New()
	if red, _, _ := r.RedactValue(float64(4111111111111111)); red != "⟦REDACTED:PAN⟧" {
		t.Errorf("PAN-as-float64 not redacted: %v", red)
	}
	if red, _, _ := r.RedactValue(1200); red != 1200 {
		t.Errorf("1200 changed: %v", red)
	}
	if red, _, _ := r.RedactValue(1.5); red != 1.5 {
		t.Errorf("1.5 changed: %v", red)
	}
	if red, _, _ := r.RedactValue(json.Number("4111111111111111")); red != "⟦REDACTED:PAN⟧" {
		t.Errorf("PAN-as-json.Number not redacted: %v", red)
	}
	if red, _, _ := r.RedactValue(json.Number("1200")); red != json.Number("1200") {
		t.Errorf("json.Number 1200 changed type or value: %#v", red)
	}
	// Non-string, non-number scalars are left alone.
	if red, _, _ := r.RedactValue(true); red != true {
		t.Errorf("bool changed: %v", red)
	}
	if red, _, _ := r.RedactValue(nil); red != nil {
		t.Errorf("nil changed: %v", red)
	}
}

func TestTextPathRobustness(t *testing.T) {
	r := New()
	// Empty body.
	if res := r.Redact(""); res.Text != "" || len(res.Patterns) != 0 {
		t.Errorf("empty body: %+v", res)
	}
	// A JSON string body (top-level scalar) is scanned.
	if res := r.Redact(`"4111111111111111"`); res.Text != `"⟦REDACTED:PAN⟧"` {
		t.Errorf("top-level scalar: %q", res.Text)
	}
	// Escaped JSON strings are decoded, scanned and re-encoded canonically.
	in := `{"note":"line1\nline2 4111111111111111 \"q\" é"}`
	want := "{\"note\":\"line1\\nline2 ⟦REDACTED:PAN⟧ \\\"q\\\" é\"}"
	if res := r.Redact(in); res.Text != want {
		t.Errorf("escaped literal:\n got:  %q\n want: %q", res.Text, want)
	}
	// A URL with a query string goes through the form-aware path.
	if res := r.Redact("https://api.example.com/v1/x?card=4111111111111111&cb=a%20b"); res.Text != "https://api.example.com/v1/x?card=⟦REDACTED:PAN⟧&cb=a%20b" {
		t.Errorf("url query: %q", res.Text)
	}
	// No shared state across calls (fresh regex state).
	a := r.Redact("a@b.com and c@d.com")
	b := r.Redact("a@b.com and c@d.com")
	if !reflect.DeepEqual(a, b) || a.Text != "⟦REDACTED:EMAIL⟧ and ⟦REDACTED:EMAIL⟧" {
		t.Errorf("state leaked across calls: %+v vs %+v", a, b)
	}
}

func TestEnhancerEdgeCases(t *testing.T) {
	v := map[string]any{"a": "x"}
	// Unknown types and malformed paths are ignored.
	if red, _ := Enhance(v, []SensitiveField{{Path: "a", Type: "NOPE"}}); !reflect.DeepEqual(red, v) {
		t.Errorf("unknown type not ignored: %v", red)
	}
	if red, _ := Enhance(v, []SensitiveField{{Path: "", Type: "PAN"}}); !reflect.DeepEqual(red, v) {
		t.Errorf("empty path not ignored: %v", red)
	}
	if red, _ := Enhance(v, []SensitiveField{{Path: ".a", Type: "PAN"}}); !reflect.DeepEqual(red, v) {
		t.Errorf("malformed path not ignored: %v", red)
	}
	// Containers are never replaced.
	nested := map[string]any{"a": map[string]any{"b": "x"}}
	if red, _ := Enhance(nested, []SensitiveField{{Path: "a", Type: "PAN"}}); !reflect.DeepEqual(red, nested) {
		t.Errorf("container replaced: %v", red)
	}
	// The input is never mutated.
	Enhance(v, []SensitiveField{{Path: "a", Type: "PAN"}})
	if !reflect.DeepEqual(v, map[string]any{"a": "x"}) {
		t.Errorf("input mutated: %v", v)
	}
}
