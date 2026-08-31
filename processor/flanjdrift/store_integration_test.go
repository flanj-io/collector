package flanjdrift

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// TestUploadToDetectionAgainstARealStore is the seam this whole slice turns on,
// against the real backend rather than a fake: a contract written to the store
// the way the upload endpoint writes it is picked up by the refresh and
// validates the next call. Every other test here stubs one side or the other.
func TestUploadToDetectionAgainstARealStore(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	p := &driftProcessor{
		cfg:   &Config{},
		mcp:   drift.NewMCPDetector(),
		specs: newSpecCache(),
		src:   storeSpecSource{st: st},
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}

	// Nothing uploaded: the golden call is captured, not validated.
	p.refreshSpecs()
	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 0 {
		t.Fatalf("findings before any upload = %d, want 0", got)
	}

	// The operator uploads api.acme.test's contract.
	if _, err := st.PutUploadedSpec(model.SpecInfo{
		Integration: "api-acme-test",
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatOpenAPI,
		PeerHost:    "api.acme.test",
		Source:      model.SpecSourceUpload,
		LoadedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}, specV1(t)); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// The refresh picks it up, and the very next call is validated.
	p.refreshSpecs()
	out, err = p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs after upload: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 1 {
		t.Fatalf("findings after upload = %d, want 1 — the uploaded contract is not validating traffic", got)
	}

	// Removed: detection stops. This is what makes a mis-bound upload undoable.
	if _, err := st.DeleteSpecInfo("api-acme-test"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	p.refreshSpecs()
	out, err = p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs after remove: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 0 {
		t.Fatalf("findings after remove = %d, want 0 — a removed contract kept validating", got)
	}
}

// TestRefreshLoopPicksUpAnUploadWithoutARestart: the loop is what turns "the
// store holds contracts" into "an upload takes effect". A kick must get there
// well inside the 60s tick, because the alternative is an operator uploading a
// contract and watching nothing happen.
func TestRefreshLoopPicksUpAnUploadWithoutARestart(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	p := &driftProcessor{
		cfg:   &Config{},
		mcp:   drift.NewMCPDetector(),
		specs: newSpecCache(),
		src:   storeSpecSource{st: st},
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	p.wg.Add(1)
	go p.refreshLoop()
	t.Cleanup(func() { _ = p.shutdown(context.Background()) })

	if _, err := st.PutUploadedSpec(model.SpecInfo{
		Integration: "api-acme-test",
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatOpenAPI,
		PeerHost:    "api.acme.test",
		Source:      model.SpecSourceUpload,
		LoadedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}, specV1(t)); err != nil {
		t.Fatalf("upload: %v", err)
	}
	p.kickRefresh()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := p.specs.lookup("api.acme.test"); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh loop never picked up an uploaded contract")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestShutdownStopsTheRefreshLoop: the loop holds the store handle, so it must
// be stopped before the store extension closes it under a restart. Shutdown is
// also called more than once in some collector paths and must tolerate it.
func TestShutdownStopsTheRefreshLoop(t *testing.T) {
	p := &driftProcessor{
		cfg:   &Config{},
		specs: newSpecCache(),
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	p.wg.Add(1)
	go p.refreshLoop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.shutdown(context.Background())
		_ = p.shutdown(context.Background()) // idempotent
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not stop the refresh loop")
	}
}
