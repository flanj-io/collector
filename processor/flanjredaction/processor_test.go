package flanjredaction

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/otlpattr"
)

// TestRedactSnapshotRecord: the defense-in-depth pass also covers the stored
// MCP contract snapshot (v0.5) — a PAN that slipped past the SDK's floor
// (e.g. in a tool description) is tokenised before the snapshot reaches the
// drift loader / the store, and an already-redacted snapshot is untouched
// (idempotent, add-only).
func TestRedactSnapshotRecord(t *testing.T) {
	p := newRedactionProcessor(&Config{})

	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
	leaked := `{"tools":[{"name":"pay","description":"test card 4111 1111 1111 1111","inputSchema":{"type":"object"}}]}`
	lr.Attributes().PutStr(otlpattr.AttrMCPContractSnapshot, leaked)
	lr.Attributes().PutStr(otlpattr.AttrRedactPatterns, `[]`)

	p.redactSnapshot(lr)

	v, _ := lr.Attributes().Get(otlpattr.AttrMCPContractSnapshot)
	if strings.Contains(v.Str(), "4111") || !strings.Contains(v.Str(), "⟦REDACTED:PAN⟧") {
		t.Fatalf("snapshot not redacted: %s", v.Str())
	}
	if a, _ := lr.Attributes().Get(otlpattr.AttrRedactApplied); !a.Bool() {
		t.Errorf("redaction.applied not set")
	}
	if pats, _ := lr.Attributes().Get(otlpattr.AttrRedactPatterns); !strings.Contains(pats.Str(), "PAN") {
		t.Errorf("patterns = %s, want PAN merged in", pats.Str())
	}

	// Idempotent: a second pass changes nothing.
	before := v.Str()
	p.redactSnapshot(lr)
	after, _ := lr.Attributes().Get(otlpattr.AttrMCPContractSnapshot)
	if after.Str() != before {
		t.Errorf("second pass mutated the snapshot (double-wrap?)")
	}

	// A clean snapshot passes through byte-identical, nothing fires.
	lr2 := plog.NewLogRecord()
	lr2.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
	clean := `{"tools":[{"name":"get_balance","inputSchema":{"type":"object"}}]}`
	lr2.Attributes().PutStr(otlpattr.AttrMCPContractSnapshot, clean)
	p.redactSnapshot(lr2)
	if v2, _ := lr2.Attributes().Get(otlpattr.AttrMCPContractSnapshot); v2.Str() != clean {
		t.Errorf("clean snapshot mutated: %s", v2.Str())
	}
	if _, ok := lr2.Attributes().Get(otlpattr.AttrRedactApplied); ok {
		t.Errorf("clean snapshot must not set redaction.applied")
	}
}

// TestRedactSnapshotServerCommand: the stdio launch line riding a
// contract_snapshot gets the same defense-in-depth as the snapshot beside it —
// it is STORED with the observed contract and rendered on its card. The floor
// runs per ELEMENT (the SDK's own contract), so the value stays a JSON array;
// what the floor leaves alone is byte-identical (raw non-ASCII, no HTML
// escaping), and a second pass changes nothing.
func TestRedactSnapshotServerCommand(t *testing.T) {
	p := newRedactionProcessor(&Config{})
	record := func(command string) plog.LogRecord {
		lr := plog.NewLogRecord()
		lr.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
		lr.Attributes().PutStr(otlpattr.AttrMCPContractSnapshot, `{"tools":[{"name":"list_charges","inputSchema":{"type":"object"}}]}`)
		lr.Attributes().PutStr(otlpattr.AttrMCPServerCommand, command)
		lr.Attributes().PutStr(otlpattr.AttrRedactPatterns, `[]`)
		return lr
	}
	command := func(lr plog.LogRecord) string {
		v, _ := lr.Attributes().Get(otlpattr.AttrMCPServerCommand)
		return v.Str()
	}

	// A card number slipped past the SDK into an argument.
	lr := record(`["npx","-y","@acme/mcp@1.0.0","--note=<café & co>","--card=4111 1111 1111 1111"]`)
	p.redactSnapshot(lr)
	got := command(lr)
	if strings.Contains(got, "4111") || !strings.Contains(got, "⟦REDACTED:PAN⟧") {
		t.Fatalf("server command not redacted: %s", got)
	}
	parts, ok := otlpattr.DecodeServerCommand(got)
	if !ok || len(parts) != 5 {
		t.Fatalf("the redacted command is no longer a 5-element JSON array of strings: %s", got)
	}
	if parts[0] != "npx" || parts[2] != "@acme/mcp@1.0.0" {
		t.Errorf("clean elements moved: %q", parts)
	}
	if !strings.Contains(got, `"--note=<café & co>"`) {
		t.Errorf("an untouched element was re-escaped (want raw non-ASCII, no \\u003c): %s", got)
	}
	if a, _ := lr.Attributes().Get(otlpattr.AttrRedactApplied); !a.Bool() {
		t.Errorf("redaction.applied not set")
	}
	if pats, _ := lr.Attributes().Get(otlpattr.AttrRedactPatterns); !strings.Contains(pats.Str(), "PAN") {
		t.Errorf("patterns = %s, want PAN merged in", pats.Str())
	}
	p.redactSnapshot(lr)
	if again := command(lr); again != got {
		t.Errorf("second pass mutated the command (double-wrap?):\n  %s\n  %s", got, again)
	}

	// A clean command passes through byte-identical, and nothing fires.
	const clean = `["uvx","acme-mcp","--profile","café","…"]`
	lr2 := record(clean)
	p.redactSnapshot(lr2)
	if got := command(lr2); got != clean {
		t.Errorf("clean command mutated: %s", got)
	}
	if _, ok := lr2.Attributes().Get(otlpattr.AttrRedactApplied); ok {
		t.Errorf("a clean command must not set redaction.applied")
	}

	// Not a command at all: the snapshot decoder drops it, but it is floored
	// as text first — nothing un-floored rides the record whatever reads it.
	lr3 := record(`npx acme-mcp --card=4111 1111 1111 1111`)
	p.redactSnapshot(lr3)
	if got := command(lr3); strings.Contains(got, "4111") {
		t.Errorf("a malformed command carried a card number past the floor: %s", got)
	}
}
