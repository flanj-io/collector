package redact

// THE cross-language PARITY suite — the Go twin of the TS fixtures.spec.ts.
// contracts/redaction-fixtures.json is run by this suite AND by the TS suite; both
// must produce these exact results. This file is the contract, not the code.
//
//   - kind=json: BOTH entry points are asserted — the structural RedactValue(input)
//     and the text path Redact(marshal(input)) parsed back — by deep-equality (the
//     parity oracle: JS/Go serializer differences — key order, number formatting,
//     HTML escaping — cannot mask or fake a redaction difference).
//   - kind=text: the text path must match byte-for-byte.
//   - every case must be idempotent (redacting `expected` is a no-op firing nothing)
//     and must never mutate its input.
//   - `enhancer` cases run the schema-aware enhancer on the floor's output; and the
//     never-subtract law is asserted over the cross product of every json case ×
//     every spec in the file plus a hostile spec.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

type fixtureEnhancer struct {
	Spec     []SensitiveField `json:"spec"`
	Expected any              `json:"expected"`
	Patterns []string         `json:"patterns"`
}

type fixtureCase struct {
	ID          string           `json:"id"`
	Description string           `json:"description"`
	Kind        string           `json:"kind"`
	Direction   string           `json:"direction"`
	Input       any              `json:"input"`
	Expected    any              `json:"expected"`
	Patterns    []string         `json:"patterns"`
	Enhancer    *fixtureEnhancer `json:"enhancer"`
}

type fixtureFile struct {
	Cases []fixtureCase `json:"cases"`
}

// loadFixtures decodes the vendored fixture file with UseNumber so every JSON number
// in input/expected is a json.Number — the structural path must return them untouched
// (bit-identical literals), which reflect.DeepEqual then verifies.
func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "redaction-fixtures.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixtures: %v", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.UseNumber()
	var ff fixtureFile
	if err := dec.Decode(&ff); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(ff.Cases) <= 50 {
		t.Fatalf("fixture battery suspiciously small: %d cases", len(ff.Cases))
	}
	return ff
}

// decodeNumber parses JSON text with UseNumber (the same decode the fixture file
// gets), so parse-backs compare like with like.
func decodeNumber(t *testing.T, text string) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("parse redacted text %q: %v", text, err)
	}
	return v
}

// equalPatterns compares fired ids EXACTLY (order included); nil and empty are equal.
func equalPatterns(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// tokensByPath collects every ⟦REDACTED:…⟧ token in a value, keyed by its JSON path —
// the never-subtract oracle (the Go twin of the TS helper).
func tokensByPath(v any, path string, out map[string][]string) {
	switch val := v.(type) {
	case string:
		if found := reportedTokenRe.FindAllString(val, -1); found != nil {
			out[path] = found
		}
	case []any:
		for i, e := range val {
			tokensByPath(e, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if keyTokens := reportedTokenRe.FindAllString(k, -1); keyTokens != nil {
				out[path+".<key:"+k+">"] = keyTokens
			}
			tokensByPath(val[k], path+"."+k, out)
		}
	}
}

func allTokensByPath(v any) map[string][]string {
	out := map[string][]string{}
	tokensByPath(v, "$", out)
	return out
}

// TestFixtures is the fixture battery: every case, both entry points, idempotency,
// input immutability and the enhancer expectations.
func TestFixtures(t *testing.T) {
	ff := loadFixtures(t)
	r := New()
	for _, c := range ff.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			if c.Kind == "json" {
				// (a) structural RedactValue(input) deep-equals expected.
				before := marshal(t, c.Input)
				redacted, hits := r.RedactValue(c.Input)
				if !reflect.DeepEqual(redacted, c.Expected) {
					t.Errorf("RedactValue mismatch\n got:  %s\n want: %s", marshal(t, redacted), marshal(t, c.Expected))
				}
				if !equalPatterns(hits, c.Patterns) {
					t.Errorf("RedactValue hits mismatch\n got:  %v\n want: %v", hits, c.Patterns)
				}

				// (d) never mutates its input.
				if after := marshal(t, c.Input); after != before {
					t.Errorf("input was mutated\n before: %s\n after:  %s", before, after)
				}

				// (b) text path over the serialized input parses back to expected.
				res := r.Redact(before)
				if got := decodeNumber(t, res.Text); !reflect.DeepEqual(got, c.Expected) {
					t.Errorf("text path mismatch\n got:  %s\n want: %s", res.Text, marshal(t, c.Expected))
				}
				if !equalPatterns(res.Patterns, c.Patterns) {
					t.Errorf("text path patterns mismatch\n got:  %v\n want: %v", res.Patterns, c.Patterns)
				}

				// (c) idempotent on the structural path.
				again, againHits := r.RedactValue(c.Expected)
				if !reflect.DeepEqual(again, c.Expected) {
					t.Errorf("structural re-redaction changed the value\n got:  %s", marshal(t, again))
				}
				if len(againHits) != 0 {
					t.Errorf("structural re-redaction fired %v (must be inert)", againHits)
				}
			} else {
				input, ok := c.Input.(string)
				if !ok {
					t.Fatalf("text case input is not a string")
				}
				expected, ok := c.Expected.(string)
				if !ok {
					t.Fatalf("text case expected is not a string")
				}
				res := r.Redact(input)
				if res.Text != expected {
					t.Errorf("text mismatch\n input: %q\n got:   %q\n want:  %q", input, res.Text, expected)
				}
				if !equalPatterns(res.Patterns, c.Patterns) {
					t.Errorf("patterns mismatch\n got:  %v\n want: %v", res.Patterns, c.Patterns)
				}
			}

			// Idempotency on the text path. For json cases expectedText is the
			// MARSHALLED expected (json.Marshal sorts keys and HTML-escapes <>&; that
			// is fine because we compare Redact(marshalled).Text to the marshalled
			// bytes themselves, not to the TS serialization).
			var expectedText string
			if c.Kind == "json" {
				expectedText = marshal(t, c.Expected)
			} else {
				expectedText = c.Expected.(string)
			}
			again := r.Redact(expectedText)
			if again.Text != expectedText {
				t.Errorf("text re-redaction not a no-op\n got:  %q\n want: %q", again.Text, expectedText)
			}
			if len(again.Patterns) != 0 {
				t.Errorf("text re-redaction fired %v (must be inert)", again.Patterns)
			}

			// Enhancer expectations.
			if c.Enhancer != nil {
				floor, _ := r.RedactValue(c.Input)
				enhanced, hits := Enhance(floor, c.Enhancer.Spec)
				if !reflect.DeepEqual(enhanced, c.Enhancer.Expected) {
					t.Errorf("enhancer mismatch\n got:  %s\n want: %s", marshal(t, enhanced), marshal(t, c.Enhancer.Expected))
				}
				if !equalPatterns(hits, c.Enhancer.Patterns) {
					t.Errorf("enhancer hits mismatch\n got:  %v\n want: %v", hits, c.Enhancer.Patterns)
				}
			}
		})
	}
}

// TestNeverSubtractLaw asserts enhancer(floor(x), spec) ⊇ floor(x) for every json
// case × every spec in the file, plus the hostile spec the TS suite uses: a spec that
// points at every floor-redacted field with a DIFFERENT type, plus wildcards. Every
// floor token must survive, unchanged, at its path.
func TestNeverSubtractLaw(t *testing.T) {
	ff := loadFixtures(t)
	r := New()

	specs := [][]SensitiveField{{}}
	for _, c := range ff.Cases {
		if c.Enhancer != nil {
			specs = append(specs, c.Enhancer.Spec)
		}
	}
	specs = append(specs, []SensitiveField{
		{Path: "card_number", Type: "EMAIL"},
		{Path: "card", Type: "IP"},
		{Path: "cards[]", Type: "TOKEN"},
		{Path: "items[].pan", Type: "SSN"},
		{Path: "charge.source.card_number", Type: "PHONE"},
		{Path: "payment.card.number", Type: "EMAIL"},
	})

	for _, c := range ff.Cases {
		if c.Kind != "json" {
			continue
		}
		c := c
		t.Run(c.ID, func(t *testing.T) {
			floor, _ := r.RedactValue(c.Input)
			before := allTokensByPath(floor)
			for _, spec := range specs {
				enhanced, _ := Enhance(floor, spec)
				after := allTokensByPath(enhanced)
				for path, toks := range before {
					if !reflect.DeepEqual(after[path], toks) {
						t.Errorf("spec %+v removed/changed tokens at %s\n before: %v\n after:  %v", spec, path, toks, after[path])
					}
				}
			}
		})
	}
}
