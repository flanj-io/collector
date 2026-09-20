package flanjui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestMessagesNeverSayControlPlane guards messages.go — every user-facing
// relay string — directly against its own source, rather than a
// hand-maintained list like relayMessages() in naming_test.go: a new msg*
// constant is covered the moment it is added, so the guard cannot silently
// miss one the way an enumerated list can. "control plane" is this repo's
// internal name for the hosted side; the product's own vocabulary for it,
// visible in the UI (ConnectPanel.vue: "Connect to Flanj" / "your Flanj
// workspace"), is simply "Flanj", and every relay message must use that too.
func TestMessagesNeverSayControlPlane(t *testing.T) {
	raw, err := os.ReadFile("messages.go")
	if err != nil {
		t.Fatalf("read messages.go: %v", err)
	}

	deny := regexp.MustCompile(`(?i)control[ -]plane`)
	found := 0
	for _, lit := range stringLiteralsExcludingComments(string(raw)) {
		if deny.MatchString(lit.text) {
			found++
			t.Errorf("messages.go:%d: user-facing string says \"control plane\": %s", lit.line, lit.text)
		}
	}
	if found > 0 {
		t.Fatalf("%d hit(s) of \"control plane\" in messages.go", found)
	}
}

type stringLiteral struct {
	line int
	text string
}

var (
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`//.*$`)
	reGoStringLit  = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
)

// stringLiteralsExcludingComments returns every double-quoted Go string
// literal in src, with block and line comments stripped first so a comment
// that merely mentions "control plane" (of which messages.go has several,
// explaining the codes) is never mistaken for a user-facing string. None of
// the literals in this file contain "//", so stripping line comments this way
// is safe here.
func stringLiteralsExcludingComments(src string) []stringLiteral {
	clean := reBlockComment.ReplaceAllString(src, "")
	var out []stringLiteral
	for i, ln := range strings.Split(clean, "\n") {
		ln = reLineComment.ReplaceAllString(ln, "")
		for _, lit := range reGoStringLit.FindAllString(ln, -1) {
			out = append(out, stringLiteral{line: i + 1, text: lit})
		}
	}
	return out
}
