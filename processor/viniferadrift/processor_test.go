package viniferadrift

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
)

// countRecords returns how many records of each vinifera.record.type a batch holds.
func countRecords(ld plog.Logs) map[string]int {
	out := map[string]int{}
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				out[otlpattr.RecordType(recs.At(k))]++
			}
		}
	}
	return out
}

// TestSpecInfoEmission: the loaded contracts ride the pipeline as spec_info
// records — all of them on the first batch after Start, none again until the
// refresh interval elapses, then again. This is what lets a store pod behind an
// otlphttp hop render the Contracts tab for a front collector's specs.
func TestSpecInfoEmission(t *testing.T) {
	p := &driftProcessor{
		cfg: &Config{IntegrationID: "acme-payments"},
		specInfos: []specInfoRecord{
			{info: model.SpecInfo{Integration: "acme-payments", Role: model.SpecRoleProvider, Format: "openapi"}, raw: []byte("openapi: 3.0.3\n")},
			{info: model.SpecInfo{Integration: "self", Role: model.SpecRoleSelf, Format: "openapi"}, raw: []byte("openapi: 3.0.3\n")},
		},
	}
	emptyBatch := func() plog.Logs { return plog.NewLogs() }

	// First batch after Start: every spec_info goes out (even with no calls).
	out, err := p.processLogs(context.Background(), emptyBatch())
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 2 {
		t.Fatalf("first batch spec_info records = %d, want 2", got)
	}
	// The records decode back to what was loaded.
	sl := out.ResourceLogs().At(out.ResourceLogs().Len() - 1).ScopeLogs().At(0)
	if sl.Scope().Name() != "viniferadrift" {
		t.Errorf("trailing scope = %q, want viniferadrift", sl.Scope().Name())
	}
	info, raw, err := otlpattr.SpecInfoFromRecord(sl.LogRecords().At(0))
	if err != nil || info.Integration != "acme-payments" || string(raw) != "openapi: 3.0.3\n" {
		t.Errorf("decoded spec_info = %+v raw=%q err=%v", info, raw, err)
	}

	// Immediately after: nothing (rate-limited).
	out, _ = p.processLogs(context.Background(), emptyBatch())
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 0 {
		t.Fatalf("second batch spec_info records = %d, want 0 (within refresh interval)", got)
	}

	// Once the interval has elapsed: emitted again (a fresh store converges).
	p.specEmitMu.Lock()
	p.nextSpecEmit = time.Now().Add(-time.Second)
	p.specEmitMu.Unlock()
	out, _ = p.processLogs(context.Background(), emptyBatch())
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 2 {
		t.Fatalf("post-interval spec_info records = %d, want 2", got)
	}

	// A processor with no specs never emits any.
	none := &driftProcessor{cfg: &Config{}}
	out, _ = none.processLogs(context.Background(), emptyBatch())
	if got := countRecords(out)[otlpattr.RecordTypeSpecInfo]; got != 0 {
		t.Fatalf("spec-less processor emitted %d spec_info records", got)
	}
}
