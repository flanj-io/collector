package redact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// vectorFile is the FROZEN contract. The vectors, not this code, are the oracle.
type vectorFile struct {
	Cases []struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Input       string   `json:"input"`
		Expected    string   `json:"expected"`
		Patterns    []string `json:"patterns"`
	} `json:"cases"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "redaction-vectors.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vf vectorFile
	if err := json.Unmarshal(b, &vf); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(vf.Cases) == 0 {
		t.Fatal("no vector cases loaded")
	}
	return vf
}

// TestRedactionVectors is the lead security suite: every golden vector must
// pass, exact text AND exact fired-pattern set.
func TestRedactionVectors(t *testing.T) {
	vf := loadVectors(t)
	r := New()
	for _, c := range vf.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			got := r.Redact(c.Input)
			if got.Text != c.Expected {
				t.Errorf("%s: text mismatch\n input:    %q\n expected: %q\n got:      %q",
					c.ID, c.Input, c.Expected, got.Text)
			}
			want := append([]string{}, c.Patterns...)
			sort.Strings(want)
			gotP := append([]string{}, got.Patterns...)
			sort.Strings(gotP)
			if len(want) == 0 {
				want = nil
			}
			if len(gotP) == 0 {
				gotP = nil
			}
			if !reflect.DeepEqual(want, gotP) {
				t.Errorf("%s: patterns mismatch\n expected: %v\n got:      %v", c.ID, want, gotP)
			}
		})
	}
}

// TestIdempotent enforces invariant 2 for every vector: Redact(Redact(x)) == Redact(x)
// and the second pass fires no new patterns (token is inert).
func TestIdempotent(t *testing.T) {
	vf := loadVectors(t)
	r := New()
	for _, c := range vf.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			once := r.Redact(c.Input)
			twice := r.Redact(once.Text)
			if twice.Text != once.Text {
				t.Errorf("%s: not idempotent\n once:  %q\n twice: %q", c.ID, once.Text, twice.Text)
			}
			if len(twice.Patterns) != 0 {
				t.Errorf("%s: re-scan fired patterns %v (must be inert)", c.ID, twice.Patterns)
			}
		})
	}
}

// TestAddOnly enforces invariant 1: redaction never removes an existing token.
func TestAddOnly(t *testing.T) {
	r := New()
	in := "pre ⟦REDACTED:PAN⟧ mid ⟦REDACTED:EMAIL⟧ post 4242424242424242"
	got := r.Redact(in)
	// Both pre-existing tokens must survive verbatim.
	if want := "⟦REDACTED:PAN⟧"; !contains(got.Text, want) {
		t.Errorf("existing PAN token was removed: %q", got.Text)
	}
	if want := "⟦REDACTED:EMAIL⟧"; !contains(got.Text, want) {
		t.Errorf("existing EMAIL token was removed: %q", got.Text)
	}
	// The new bare PAN must be redacted.
	if contains(got.Text, "4242424242424242") {
		t.Errorf("bare PAN not redacted: %q", got.Text)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
