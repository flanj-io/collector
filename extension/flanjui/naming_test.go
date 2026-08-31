package flanjui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNamingDenylist is the v0.1a naming pass: no user-facing "peek / peek link
// / magic link / minting / invite / invitee / previewer" anywhere the consumer
// reads — the UI's templates and string literals (ui/src/**/*.vue|ts) and the
// relay's user-facing messages. Wire identifiers (peek_url, /api/peek/*, vpeek_,
// peek-links) are allowed and only ever appear in JS identifiers / wire paths,
// which this scan skips. Cheap: source scan, no build step.
func TestNamingDenylist(t *testing.T) {
	deny := regexp.MustCompile(`(?i)\b(peek|peek[ -]link|magic[ -]link|mint|minting|minted|invite|invited|invitee|invitation|previewer)\b`)
	// Wire / internal tokens that may legitimately appear inside a literal
	// (asset paths, API routes) — never user-facing by themselves.
	allow := regexp.MustCompile(`(?i)(/api/peek|peek_url|vpeek_|peek-links|/peek-assets/)`)

	uiRoot := filepath.Join("..", "..", "ui", "src")
	var files []string
	if err := filepath.WalkDir(uiRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".vue") || strings.HasSuffix(path, ".ts")) && !strings.HasSuffix(path, ".test.ts") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", uiRoot, err)
	}
	if len(files) == 0 {
		t.Fatalf("no UI sources found under %s", uiRoot)
	}

	found := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range userFacingChunks(string(raw), strings.HasSuffix(f, ".vue")) {
			if allow.MatchString(chunk.text) {
				continue
			}
			if m := deny.FindString(chunk.text); m != "" {
				found++
				t.Errorf("%s:%d: user-facing text contains %q: %q", f, chunk.line, m, strings.TrimSpace(chunk.text))
			}
		}
	}

	// The relay's own user-facing messages.
	for _, m := range relayMessages() {
		if w := deny.FindString(m); w != "" {
			found++
			t.Errorf("relay message contains %q: %q", w, m)
		}
	}
	if found > 0 {
		t.Fatalf("%d naming denylist hit(s)", found)
	}
}

type textChunk struct {
	line int
	text string
}

var (
	reScriptBlock   = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)
	reTemplateBlock = regexp.MustCompile(`(?s)<template>(.*)</template>`)
	reStrLit        = regexp.MustCompile("`(?:[^`\\\\]|\\\\.)*`|'(?:[^'\\\\\\n]|\\\\.)*'|\"(?:[^\"\\\\\\n]|\\\\.)*\"")
	// bound attributes / directives carry JS expressions, not copy
	reBoundAttr  = regexp.MustCompile(`(?s)\s(?::[\w.-]+|@[\w.-]+|v-[\w.:-]+)="[^"]*"`)
	reInterp     = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	reTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	reHTMLCmt    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reLineCmt    = regexp.MustCompile(`(?m)^\s*//.*$`)
	reBlockCmt   = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reStaticAttr = regexp.MustCompile(`\s(?:placeholder|title|aria-label|alt|value|label)="([^"]*)"`)
)

// userFacingChunks extracts what a person could read: template text (tags,
// bound expressions and interpolations stripped; static placeholder/title/aria
// attributes kept) + every string literal in script/ts (comments removed).
func userFacingChunks(src string, isVue bool) []textChunk {
	var out []textChunk
	lineOf := func(offset int) int { return 1 + strings.Count(src[:offset], "\n") }

	scriptSrc := src
	if isVue {
		scriptSrc = ""
		for _, m := range reScriptBlock.FindAllStringSubmatchIndex(src, -1) {
			scriptSrc += src[m[2]:m[3]] + "\n"
		}
		if tm := reTemplateBlock.FindStringSubmatchIndex(src); tm != nil {
			tpl := src[tm[2]:tm[3]]
			base := tm[2]
			// static attributes first (they vanish with the tags)
			for _, am := range reStaticAttr.FindAllStringSubmatchIndex(tpl, -1) {
				out = append(out, textChunk{line: lineOf(base + am[2]), text: tpl[am[2]:am[3]]})
			}
			clean := reHTMLCmt.ReplaceAllString(tpl, "")
			// string literals inside {{ }} and bound expressions are copy too
			// (`{{ busy ? 'Creating…' : 'Create thread' }}`, `:title="x ? 'a' : 'b'"`)
			for _, re := range []*regexp.Regexp{reInterp, reBoundAttr} {
				for _, em := range re.FindAllStringIndex(clean, -1) {
					for _, lit := range reStrLit.FindAllString(clean[em[0]:em[1]], -1) {
						out = append(out, textChunk{line: lineOf(base + em[0]), text: lit})
					}
				}
			}
			clean = reBoundAttr.ReplaceAllString(clean, " ")
			clean = reInterp.ReplaceAllString(clean, " ")
			clean = reTag.ReplaceAllString(clean, "\n")
			for i, ln := range strings.Split(clean, "\n") {
				if s := strings.TrimSpace(ln); s != "" {
					out = append(out, textChunk{line: i + lineOf(base), text: s})
				}
			}
		}
	}
	clean := reBlockCmt.ReplaceAllString(scriptSrc, "")
	clean = reLineCmt.ReplaceAllString(clean, "")
	for i, ln := range strings.Split(clean, "\n") {
		for _, lit := range reStrLit.FindAllString(ln, -1) {
			if strings.HasPrefix(lit, "'/api/") || strings.HasPrefix(lit, "\"/api/") {
				continue // wire paths
			}
			out = append(out, textChunk{line: i + 1, text: lit})
		}
	}
	return out
}

// relayMessages lists every user-facing string the relay can return.
func relayMessages() []string {
	return []string{
		msgNotConnected, msgContactUnconfirmed, msgCPUnreachableFlag, msgCPUnreachableSend, msgCPUnreachable,
		msgCPNotConfigured, msgStoreUnavailable, msgPostOnly, msgUIHeaderRequired, msgJSONRequired, msgForeignOrigin,
		msgInvalidJSON, msgConnectFields, msgInvalidEmail, msgFindingRequired, msgFindingNotFound, msgFindingNoCall,
		msgCallEvicted, msgThreadNotFound, msgWrongOrigin, msgKeyMissing, msgNotAckable, contactUnconfirmedMessage("ops@example.test"),
	}
}

// TestThreadsNoticeMirrorInSync makes the ui/src/threads.ts mirror of
// msgThreadsNotConnected a MECHANICAL guarantee rather than a comment.
//
// The SPA no longer provokes the 412 that used to carry this sentence (a
// refused request is logged by the browser as a failed resource on every poll
// tick), so it renders the line from its own constant. Two copies of one
// user-facing string drift silently — and a "keep these in sync" comment is
// exactly what failed us before. This asserts the Go source of truth appears
// verbatim in the mirror, so changing one without the other is a red test.
func TestThreadsNoticeMirrorInSync(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "threads.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), msgThreadsNotConnected) {
		t.Fatalf("ui/src/threads.ts no longer carries msgThreadsNotConnected verbatim.\n"+
			"want the exact sentence: %q\n"+
			"Update THREADS_NOT_CONNECTED_NOTICE in threads.ts to match messages.go "+
			"(the Threads tab renders it locally now, so the two must agree byte-for-byte).",
			msgThreadsNotConnected)
	}
}
