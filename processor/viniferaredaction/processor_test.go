package viniferaredaction

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/vinifera-io/collector/internal/otlpattr"
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
