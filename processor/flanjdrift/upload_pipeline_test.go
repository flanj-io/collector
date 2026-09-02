package flanjdrift

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// goldenCallBatch is the vendored golden OTLP call: one drifting POST
// /v1/charges to api.acme.test whose response carries `amount` as a string
// where spec-v1 declares integer. Ingesting it must deterministically produce
// the live-vs-spec Finding (CLAUDE.md, "Contract").
func goldenCallBatch(t *testing.T) plog.Logs {
	t.Helper()
	b, err := os.ReadFile("../../contracts/golden-otlp-call.json")
	if err != nil {
		t.Fatalf("read golden call: %v", err)
	}
	ld, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(b)
	if err != nil {
		t.Fatalf("unmarshal golden call: %v", err)
	}
	return ld
}

// processorWith returns a processor whose spec cache holds the given
// host->document bindings, as if they had been uploaded in the UI and picked up
// by a refresh.
func processorWith(t *testing.T, bindings map[string][]byte) *driftProcessor {
	t.Helper()
	src := newFakeSource()
	for host, doc := range bindings {
		src.put("int-"+host, host, "v1", doc)
	}
	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("seed cache: %v", errs)
	}
	return &driftProcessor{
		cfg:   &Config{},
		mcp:   drift.NewMCPDetector(),
		specs: c,
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// TestUploadedContractDetectsDrift is the oracle for the whole slice: the same
// golden call that used to drift against a config-mounted `spec_path` now
// drifts against a contract that arrived from the store. If this passes, upload
// actually validates traffic.
func TestUploadedContractDetectsDrift(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})

	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 1 {
		t.Fatalf("findings = %d, want exactly 1 live-vs-spec finding", got)
	}
}

// TestContractBindingScopesDetection: a contract bound to one host must never
// validate another host's traffic. The old model had an OPTIONAL `peer_host`,
// so an unscoped spec validated EVERY outbound call against one document;
// upload makes binding mandatory, and this is that promise under test.
func TestContractBindingScopesDetection(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.globex.test": specV1(t)})

	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if got := countRecords(out)[otlpattr.RecordTypeFinding]; got != 0 {
		t.Fatalf("findings = %d, want 0 — a contract bound to api.globex.test validated api.acme.test traffic", got)
	}
}

// TestUncoveredHostIsCapturedNotValidated: with no contract for the host, the
// call passes through untouched and produces no finding — captured, not
// validated. It still gets its call id stamped, which is what makes front->store
// retries idempotent and ties findings to calls across the tiered hop.
func TestUncoveredHostIsCapturedNotValidated(t *testing.T) {
	p := processorWith(t, nil)

	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	counts := countRecords(out)
	if counts[otlpattr.RecordTypeFinding] != 0 {
		t.Errorf("findings = %d, want 0 for an uncovered host", counts[otlpattr.RecordTypeFinding])
	}
	if counts[otlpattr.RecordTypeCall] != 1 {
		t.Errorf("calls = %d, want the call still captured", counts[otlpattr.RecordTypeCall])
	}

	lr := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	if id, ok := lr.Attributes().Get(otlpattr.AttrCallID); !ok || id.Str() == "" {
		t.Error("call id not stamped on an uncovered call — front->store retries stop being idempotent")
	}
}

// TestUncoveredHostAsksForARefresh: a call for a host with no cached contract
// kicks the refresh loop, so a contract uploaded moments ago starts validating
// without waiting out the full tick. The kick must never block the pipeline.
func TestUncoveredHostAsksForARefresh(t *testing.T) {
	p := processorWith(t, nil)

	if _, err := p.processLogs(context.Background(), goldenCallBatch(t)); err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	select {
	case <-p.kick:
	default:
		t.Fatal("an uncovered host did not ask for a refresh")
	}

	// A second batch with the kick channel already full must still not block.
	p.kickRefresh()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.processLogs(context.Background(), goldenCallBatch(t))
	}()
	<-done
}

/* ── The contract-change announcement ───────────────────────────────────── */

// storeExtStub stands in for the store extension (extension/flanjstore): the
// store handle plus the contract-change announcement the processor subscribes
// to. Kept local rather than imported — these are sibling component modules and
// neither may depend on the other; flanjstore pins its own half of the seam.
type storeExtStub struct {
	st   store.Store
	mu   sync.Mutex
	subs []func()
}

func (s *storeExtStub) Store() store.Store { return s.st }

func (s *storeExtStub) OnSpecsChanged(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, fn)
}

func (s *storeExtStub) NotifySpecsChanged() {
	s.mu.Lock()
	subs := append([]func(){}, s.subs...)
	s.mu.Unlock()
	for _, fn := range subs {
		fn()
	}
}

func (s *storeExtStub) subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func (s *storeExtStub) Start(context.Context, component.Host) error { return nil }
func (s *storeExtStub) Shutdown(context.Context) error              { return nil }

// extHost is a component.Host carrying a fixed extension set.
type extHost struct {
	exts map[component.ID]component.Component
}

func (h extHost) GetExtensions() map[component.ID]component.Component { return h.exts }

// uploadSpec writes a contract the way the UI's upload handler writes one.
func uploadSpec(t *testing.T, st store.Store, host string, doc []byte) {
	t.Helper()
	if _, err := st.PutUploadedSpec(model.SpecInfo{
		Integration: "api-acme-test",
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatOpenAPI,
		PeerHost:    host,
		Source:      model.SpecSourceUpload,
		LoadedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}, doc); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

// liveProcessor returns a started processor wired to a real store through a
// store extension, with its refresh loop running.
func liveProcessor(t *testing.T) (*driftProcessor, *storeExtStub, store.Store) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ext := &storeExtStub{st: st}
	p := &driftProcessor{
		cfg:   &Config{},
		mcp:   drift.NewMCPDetector(),
		specs: newSpecCache(),
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	host := extHost{exts: map[component.ID]component.Component{
		component.MustNewID("flanjstore"): ext,
	}}
	if err := p.start(context.Background(), host); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = p.shutdown(context.Background()) })
	return p, ext, st
}

// findingsForGoldenCall runs the golden call through the processor and reports
// how many findings it produced.
func findingsForGoldenCall(t *testing.T, p *driftProcessor) int {
	t.Helper()
	out, err := p.processLogs(context.Background(), goldenCallBatch(t))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	return countRecords(out)[otlpattr.RecordTypeFinding]
}

// awaitFindings waits for the golden call's verdict to become want, and reports
// how long it took. The elapsed time is the assertion that matters here: a
// verdict that only converges on the ticker is the defect, not the fix.
func awaitFindings(t *testing.T, p *driftProcessor, want int, within time.Duration, what string) time.Duration {
	t.Helper()
	started := time.Now()
	deadline := started.Add(within)
	var got int
	for time.Now().Before(deadline) {
		if got = findingsForGoldenCall(t, p); got == want {
			return time.Since(started)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: findings = %d after %s, want %d", what, got, within, want)
	return 0
}

// announceBudget is how long a test waits for an ANNOUNCED change to land. It
// must sit above specRefreshFloor — a change announced just after a refresh is
// deferred to the end of the floor — and below specRefresh, so a test that
// passes proves the announcement delivered the change and not the ticker
// quietly arriving at the same answer.
var announceBudget = specRefreshFloor + 3*time.Second

// assertBeatTheTicker fails when a change took as long as a plain ticker fire
// would have, which is the defect wearing the fix's test.
func assertBeatTheTicker(t *testing.T, took time.Duration, what string) {
	t.Helper()
	if announceBudget >= specRefresh {
		t.Fatalf("announceBudget %s must stay under specRefresh %s or this proves nothing",
			announceBudget, specRefresh)
	}
	if took >= specRefresh {
		t.Fatalf("%s took %s — that is the refresh ticker (%s), not the announcement",
			what, took, specRefresh)
	}
}

// TestProcessorSubscribesToContractChanges: the subscription is the whole fix,
// and it is one type assertion away from silently not happening.
func TestProcessorSubscribesToContractChanges(t *testing.T) {
	_, ext, _ := liveProcessor(t)
	if ext.subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1 — the processor is not listening for contract changes", ext.subscribers())
	}
}

// TestReplacedContractValidatesTheNewDocumentAtOnce is the defect this slice
// fixes. A REPLACE is a cache HIT: `specs.lookup` finds the superseded document
// and the processor's own first-sight kick never fires, so before the
// announcement every call for up to a full refresh interval was scored against
// the document the operator had just replaced — while the UI said "Validating
// from now on" and the card showed the new version as live. A call scored then
// is stamped permanently: captured calls are never re-checked.
//
// The golden call carries `amount` as a STRING. spec-v1 declares it an integer
// (drift), spec-v2 declares it a string (conforming), so the verdict itself
// says which document is live.
func TestReplacedContractValidatesTheNewDocumentAtOnce(t *testing.T) {
	p, ext, st := liveProcessor(t)

	uploadSpec(t, st, "api.acme.test", specV1(t))
	ext.NotifySpecsChanged()
	awaitFindings(t, p, 1, 5*time.Second, "after uploading spec-v1")

	// The replace. Nothing about the per-call path changes — the host is still
	// covered — so only the announcement can move the verdict.
	uploadSpec(t, st, "api.acme.test", specV2(t))
	ext.NotifySpecsChanged()
	took := awaitFindings(t, p, 0, announceBudget, "after replacing spec-v1 with spec-v2")
	assertBeatTheTicker(t, took, "the replaced document")
}

// TestRemovedContractStopsValidatingAtOnce: a removal is a cache hit on the
// DELETED document, so nothing on the per-call path notices it either. Remove
// exists to undo a contract bound to the wrong host; an undo that leaves the
// wrong contract scoring traffic for another interval is not one.
func TestRemovedContractStopsValidatingAtOnce(t *testing.T) {
	p, ext, st := liveProcessor(t)

	uploadSpec(t, st, "api.acme.test", specV1(t))
	ext.NotifySpecsChanged()
	awaitFindings(t, p, 1, 5*time.Second, "after uploading spec-v1")

	if _, err := st.DeleteSpecInfo("api-acme-test"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	ext.NotifySpecsChanged()
	took := awaitFindings(t, p, 0, announceBudget, "after removing the contract")
	assertBeatTheTicker(t, took, "the removal")

	if _, ok := p.specs.lookup("api.acme.test"); ok {
		t.Error("the removed contract is still cached — the refresh did not evict it")
	}
}

// TestAnnouncementInsideTheFloorIsDeferredNotDropped: traffic kicks repeat every
// batch, so the floor could discard one for free. An announcement is a one-shot
// event — swallowing it puts the upload back on the ticker, which is the wait
// the announcement exists to remove. Two uploads in quick succession is the
// ordinary case: an operator binding several providers, or re-uploading one they
// just bound to the wrong host.
//
// Runs for about specRefreshFloor by construction.
func TestAnnouncementInsideTheFloorIsDeferredNotDropped(t *testing.T) {
	p, ext, st := liveProcessor(t)

	uploadSpec(t, st, "api.acme.test", specV1(t))
	ext.NotifySpecsChanged()
	awaitFindings(t, p, 1, 5*time.Second, "after uploading spec-v1")

	// Immediately: this lands well inside the floor after the refresh above.
	uploadSpec(t, st, "api.acme.test", specV2(t))
	ext.NotifySpecsChanged()

	// It must arrive on the deferred kick, NOT on the next ticker fire.
	took := awaitFindings(t, p, 0, announceBudget,
		"a second announcement inside the refresh floor")
	assertBeatTheTicker(t, took, "the deferred announcement")
}
