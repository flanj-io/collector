package flanjstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// These are the regression tests for launch-week item 5 (2026-09-07): the
// store exporter used to hand consumeLogs' error straight back to the
// pipeline, so a store write that failed after the receiver had ACKed the
// batch — postgres away mid-write, a locked sqlite file — lost the batch: the
// calls AND the findings that pinned them. Now the exporter queues and retries,
// and the store is idempotent on call id and finding id, so the retry lands
// exactly once. The store here is the REAL sqlite backend behind a double that
// fails on command — the idempotency under test is the real one.

// flakyStore wraps a real store and fails writes on command.
type flakyStore struct {
	store.Store
	mu       sync.Mutex
	failing  bool // every write fails while set (the outage)
	failNth  int  // >0: fail exactly the nth write attempt, once (a mid-batch failure)
	attempts int
	failures int
	// rejectCallID, when set, makes InsertCall of that id come back as the
	// store's own deterministic refusal (store.ErrRejected — what both
	// backends wrap a constraint/encoding violation in; proven real on both
	// in internal/store rejected_test.go). Every attempt counts and fails.
	rejectCallID string
	// blockOn, when set, parks InsertCall until released — for the queue-full
	// test. blocked is closed (once) when the first write parks.
	blockOn   chan struct{}
	blocked   chan struct{}
	blockOnce sync.Once
}

var errOutage = errors.New("store: connection refused (simulated outage)")

func (f *flakyStore) gate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.failing || (f.failNth > 0 && f.attempts == f.failNth) {
		f.failures++
		return errOutage
	}
	return nil
}

func (f *flakyStore) set(fn func(*flakyStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *flakyStore) stats() (attempts, failures int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, f.failures
}

func (f *flakyStore) InsertCall(c model.RedactedCall) error {
	f.mu.Lock()
	park := f.blockOn
	reject := f.rejectCallID != "" && c.ID == f.rejectCallID
	if reject {
		f.attempts++
		f.failures++
	}
	f.mu.Unlock()
	if reject {
		return fmt.Errorf("insert call: %w: simulated UNIQUE violation", store.ErrRejected)
	}
	if park != nil {
		f.blockOnce.Do(func() { close(f.blocked) })
		<-park
	}
	if err := f.gate(); err != nil {
		return err
	}
	return f.Store.InsertCall(c)
}

func (f *flakyStore) InsertFinding(fd model.Finding) error {
	if err := f.gate(); err != nil {
		return err
	}
	return f.Store.InsertFinding(fd)
}

// storeProvider is the flanjstore EXTENSION as the exporter sees it.
type storeProvider struct{ st store.Store }

func (p *storeProvider) Start(context.Context, component.Host) error { return nil }
func (p *storeProvider) Shutdown(context.Context) error              { return nil }
func (p *storeProvider) Store() store.Store                          { return p.st }

type extHost struct {
	exts map[component.ID]component.Component
}

func (h extHost) GetExtensions() map[component.ID]component.Component { return h.exts }

func testSettings(logger *zap.Logger) exporter.Settings {
	set := exporter.Settings{
		ID:        component.NewID(typeStr),
		BuildInfo: component.NewDefaultBuildInfo(),
	}
	set.Logger = logger
	set.TracerProvider = nooptrace.NewTracerProvider()
	set.MeterProvider = noopmetric.NewMeterProvider()
	set.Resource = pcommon.NewResource()
	return set
}

// fastRetry keeps the production shape (queue on, retry on, reject when full)
// and only shrinks the clock.
func fastRetry(t *testing.T) *Config {
	t.Helper()
	cfg := newDefaultConfig()
	cfg.RetryConfig.InitialInterval = 10 * time.Millisecond
	cfg.RetryConfig.MaxInterval = 50 * time.Millisecond
	cfg.RetryConfig.MaxElapsedTime = 10 * time.Second
	cfg.RetryConfig.RandomizationFactor = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}
	return cfg
}

// newExporter builds the exporter over a real sqlite store wrapped in flaky.
func newExporter(t *testing.T, cfg *Config) (exporter.Logs, *flakyStore) {
	t.Helper()
	return newExporterLogging(t, cfg, zap.NewNop())
}

// newExporterLogging is newExporter with the exporter's (and exporterhelper's)
// logger supplied, for the tests that assert what gets logged.
func newExporterLogging(t *testing.T, cfg *Config, logger *zap.Logger) (exporter.Logs, *flakyStore) {
	t.Helper()
	real, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	flaky := &flakyStore{Store: real}

	exp, err := createLogsExporter(context.Background(), testSettings(logger), cfg)
	if err != nil {
		t.Fatalf("create exporter: %v", err)
	}
	host := extHost{exts: map[component.ID]component.Component{
		component.MustNewID("flanjstore"): &storeProvider{st: flaky},
	}}
	if err := exp.Start(context.Background(), host); err != nil {
		t.Fatalf("start exporter: %v", err)
	}
	return exp, flaky
}

// goldenCallUnstamped loads the SDK→collector golden call exactly as the SDK
// sends it: with NO flanj.call.id (the SDK never emits one; only the drift
// processor stamps it). Stripped defensively in case the fixture ever grows one.
func goldenCallUnstamped(t *testing.T) plog.LogRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "golden-otlp-call.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	ld, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(b)
	if err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	src := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	lr := plog.NewLogRecord()
	src.CopyTo(lr)
	lr.Attributes().Remove(otlpattr.AttrCallID)
	return lr
}

// goldenCall is the golden call as a FRONT forwards it: with the id — the
// drift-processor-stamped flanj.call.id that makes call inserts idempotent.
func goldenCall(t *testing.T, id string) plog.LogRecord {
	t.Helper()
	lr := goldenCallUnstamped(t)
	lr.Attributes().PutStr(otlpattr.AttrCallID, id)
	return lr
}

func driftFinding(id, sourceCallID string) model.Finding {
	f := model.Finding{
		SchemaVersion: model.SchemaVersion,
		ID:            id,
		Kind:          model.KindLiveVsSpec,
		Severity:      model.SeverityBreaking,
		Integration:   "acme-payments",
		Endpoint:      "POST /v1/charges",
		FieldPath:     model.Ptr("amount"),
		Location:      model.Ptr("$.response.body.amount"),
		Expected:      "type=integer",
		Actual:        `type=string ("1200")`,
		Rule:          "type-mismatch",
		SourceCallID:  &sourceCallID,
		DetectedAt:    "2026-09-07T08:00:01.000Z",
	}
	f.Signature = f.ComputeSignature()
	return f
}

// batch is one received request as the drift processor emits it: calls first,
// then the findings they produced.
func batch(t *testing.T, callIDs []string, findings []model.Finding) plog.Logs {
	t.Helper()
	ld := plog.NewLogs()
	recs := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	for _, id := range callIDs {
		goldenCall(t, id).CopyTo(recs.AppendEmpty())
	}
	for _, f := range findings {
		if err := otlpattr.FindingToRecord(recs.AppendEmpty(), f); err != nil {
			t.Fatal(err)
		}
	}
	return ld
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// assertExactlyOnce: the two calls and the one finding landed once each — the
// window holds exactly them, the edge counted each call once and the drift
// once, the finding has ONE occurrence and its source call is pinned + drifted.
func assertExactlyOnce(t *testing.T, st store.Store, callIDs []string, findingID string) {
	t.Helper()
	calls, findings, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if calls != len(callIDs) || findings != 1 {
		t.Fatalf("store holds %d calls / %d findings, want %d / 1", calls, findings, len(callIDs))
	}
	for _, id := range callIDs {
		if _, ok, err := st.GetCall(id); err != nil || !ok {
			t.Errorf("call %s missing after recovery (ok=%v err=%v)", id, ok, err)
		}
	}
	fs, err := st.ListFindings(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].ID != findingID {
		t.Fatalf("findings = %+v, want exactly %s", fs, findingID)
	}
	if fs[0].OccurrenceCount != 1 {
		t.Errorf("occurrence_count = %d, want 1 — the retried batch was counted twice", fs[0].OccurrenceCount)
	}
	edges, err := st.ListEdges(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].CallCount != len(callIDs) || edges[0].DriftCount != 1 {
		t.Errorf("edge call_count/drift_count = %d/%d, want %d/1", edges[0].CallCount, edges[0].DriftCount, len(callIDs))
	}
	src, _, _ := st.GetCall(*fs[0].SourceCallID)
	if !src.Drifted {
		t.Errorf("source call %s is not marked drifted", src.ID)
	}
}

// TestStoreOutage_RetriedWithoutLossOrDuplicate: the store is down when the
// batch arrives. The receiver's call returns nil (queued), the write fails and
// is retried, the store comes back, and everything lands exactly once.
func TestStoreOutage_RetriedWithoutLossOrDuplicate(t *testing.T) {
	exp, flaky := newExporter(t, fastRetry(t))
	defer func() { _ = exp.Shutdown(context.Background()) }()

	callIDs := []string{"0191e8c4-0000-7000-8000-000000000001", "0191e8c4-0000-7000-8000-000000000002"}
	finding := driftFinding("f_outage", callIDs[1])
	flaky.set(func(f *flakyStore) { f.failing = true })

	if err := exp.ConsumeLogs(context.Background(), batch(t, callIDs, []model.Finding{finding})); err != nil {
		t.Fatalf("ConsumeLogs during the outage must ACK (queue), got: %v", err)
	}
	// Prove it is retrying, not dropped: more than one failed attempt.
	waitFor(t, "the exporter to retry the failed write", func() bool {
		_, failures := flaky.stats()
		return failures >= 2
	})
	if calls, _, _ := flaky.Store.Counts(); calls != 0 {
		t.Fatalf("nothing can have landed while the store is down, got %d calls", calls)
	}

	flaky.set(func(f *flakyStore) { f.failing = false })
	waitFor(t, "the batch to land after recovery", func() bool {
		calls, findings, _ := flaky.Store.Counts()
		return calls == 2 && findings == 1
	})
	assertExactlyOnce(t, flaky.Store, callIDs, finding.ID)
}

// TestPartialBatch_RetryDoesNotDuplicate: the store dies half-way through a
// batch (first call written, second fails). The retry re-delivers the WHOLE
// batch — the already-written call is a no-op, the rest lands, and the finding
// counts once. Without the store's idempotency this is where rows doubled.
func TestPartialBatch_RetryDoesNotDuplicate(t *testing.T) {
	exp, flaky := newExporter(t, fastRetry(t))
	defer func() { _ = exp.Shutdown(context.Background()) }()

	callIDs := []string{"0191e8c4-0000-7000-8000-000000000011", "0191e8c4-0000-7000-8000-000000000012"}
	finding := driftFinding("f_partial", callIDs[0])
	flaky.set(func(f *flakyStore) { f.failNth = 2 }) // the second write of the first attempt

	if err := exp.ConsumeLogs(context.Background(), batch(t, callIDs, []model.Finding{finding})); err != nil {
		t.Fatalf("ConsumeLogs: %v", err)
	}
	waitFor(t, "the retried batch to land", func() bool {
		calls, findings, _ := flaky.Store.Counts()
		return calls == 2 && findings == 1
	})
	attempts, failures := flaky.stats()
	if failures != 1 || attempts != 5 { // 2 writes (1 ok, 1 failed) + 3 on the retry
		t.Errorf("attempts/failures = %d/%d, want 5/1 (one failure, one full re-delivery)", attempts, failures)
	}
	assertExactlyOnce(t, flaky.Store, callIDs, finding.ID)

	// And a second full re-delivery of the same batch (the front's retry after
	// a lost ACK) changes nothing either.
	if err := exp.ConsumeLogs(context.Background(), batch(t, callIDs, []model.Finding{finding})); err != nil {
		t.Fatalf("ConsumeLogs (re-delivery): %v", err)
	}
	waitFor(t, "the re-delivery to be consumed", func() bool {
		a, _ := flaky.stats()
		return a >= 8
	})
	assertExactlyOnce(t, flaky.Store, callIDs, finding.ID)
}

// TestQueueFull_IsRetryableBackpressure: the queue is bounded and REJECTS when
// full — the batch goes back to the receiver as a retryable (not permanent)
// error, i.e. a 503 the SDK / a front retries. That is the backstop that keeps
// an outage longer than the queue from losing anything silently.
func TestQueueFull_IsRetryableBackpressure(t *testing.T) {
	cfg := fastRetry(t)
	q := cfg.QueueConfig.Get()
	q.Sizer = exporterhelper.RequestSizerTypeRequests
	q.QueueSize = 2 // capacity counts the request a consumer is busy with
	q.NumConsumers = 1
	exp, flaky := newExporter(t, cfg)

	release := make(chan struct{})
	flaky.set(func(f *flakyStore) {
		f.blockOn = release
		f.blocked = make(chan struct{})
	})

	one := []string{"0191e8c4-0000-7000-8000-000000000021"}
	// First batch: taken by the single consumer, which parks in the store.
	if err := exp.ConsumeLogs(context.Background(), batch(t, one, nil)); err != nil {
		t.Fatalf("first ConsumeLogs: %v", err)
	}
	<-flaky.blocked
	// Second batch: fills the queue (one in flight + one waiting = 2).
	if err := exp.ConsumeLogs(context.Background(), batch(t, one, nil)); err != nil {
		t.Fatalf("second ConsumeLogs must queue: %v", err)
	}
	// Third: refused, retryably.
	err := exp.ConsumeLogs(context.Background(), batch(t, one, nil))
	if err == nil {
		t.Fatal("third ConsumeLogs must be refused by the full queue")
	}
	if consumererror.IsPermanent(err) {
		t.Fatalf("queue-full must be RETRYABLE so the receiver answers 503, got permanent: %v", err)
	}
	close(release) // the parked write and the queued one both complete
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestDefaultConfig_QueueAndRetryOn guards the shipped shape: the queue and
// the retry are ON by default, the queue rejects when full, and the operator
// switch (`sending_queue: {enabled: false}`) works. A future "cleanup" that
// drops either option turns a store blip back into silent loss.
func TestDefaultConfig_QueueAndRetryOn(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if !cfg.QueueConfig.HasValue() {
		t.Fatal("sending_queue must be enabled by default")
	}
	q := cfg.QueueConfig.Get()
	if q.BlockOnOverflow {
		t.Error("the queue must REJECT when full (block_on_overflow: false) so upstream retry stays the backstop")
	}
	if q.Sizer != exporterhelper.RequestSizerTypeBytes || q.QueueSize != defaultQueueBytes {
		t.Errorf("queue bound = %s/%d, want bytes/%d", q.Sizer, q.QueueSize, defaultQueueBytes)
	}
	if q.Batch.HasValue() {
		t.Error("no batching inside the queue: each received request is written whole")
	}
	if !cfg.RetryConfig.Enabled {
		t.Fatal("retry_on_failure must be enabled by default")
	}
	if cfg.RetryConfig.MaxElapsedTime != defaultRetryGiveUpAt {
		t.Errorf("max_elapsed_time = %s, want %s", cfg.RetryConfig.MaxElapsedTime, defaultRetryGiveUpAt)
	}

	// The operator can turn the queue off (synchronous writes, as before) and
	// tune the retry — the upstream keys, exactly as on a front's otlphttp.
	conf := confmap.NewFromStringMap(map[string]any{
		"sending_queue":    map[string]any{"enabled": false},
		"retry_on_failure": map[string]any{"max_elapsed_time": "0s"},
	})
	off := createDefaultConfig().(*Config)
	if err := conf.Unmarshal(off); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if off.QueueConfig.HasValue() {
		t.Error("sending_queue.enabled=false must disable the queue")
	}
	if off.RetryConfig.MaxElapsedTime != 0 || !off.RetryConfig.Enabled {
		t.Errorf("retry_on_failure after unmarshal = %+v, want enabled with max_elapsed_time 0", off.RetryConfig)
	}
	if err := off.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// TestUnstampedCall_RetryDoesNotDuplicate (review of #46, 2026-09-08): a call
// that reaches this exporter with NO flanj.call.id — the SDK never emits one,
// and a store pod's :4318 with no front in front of it runs [flanjredaction]
// only, so nothing upstream stamps it. Call idempotency rests on that id: when
// CallFromRecord minted it per DECODE, every retry attempt got a new one, `ON
// CONFLICT (id) DO NOTHING` never fired, and the reviewer's 2-call batch with
// failNth=2 landed as 3 rows / call_count 3. The id must be stamped onto the
// queued record itself, before decoding, so the retry re-uses it.
func TestUnstampedCall_RetryDoesNotDuplicate(t *testing.T) {
	exp, flaky := newExporter(t, fastRetry(t))
	defer func() { _ = exp.Shutdown(context.Background()) }()

	ld := plog.NewLogs()
	recs := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	for i := 0; i < 2; i++ {
		goldenCallUnstamped(t).CopyTo(recs.AppendEmpty())
	}
	for i := 0; i < recs.Len(); i++ {
		if _, ok := recs.At(i).Attributes().Get(otlpattr.AttrCallID); ok {
			t.Fatalf("record %d must reach the exporter unstamped", i)
		}
	}
	flaky.set(func(f *flakyStore) { f.failNth = 2 }) // the second write of the first attempt

	if err := exp.ConsumeLogs(context.Background(), ld); err != nil {
		t.Fatalf("ConsumeLogs: %v", err)
	}
	waitFor(t, "the retried batch to land", func() bool {
		a, _ := flaky.stats()
		return a >= 4 // 2 writes (1 ok, 1 failed) + 2 on the retry
	})
	attempts, failures := flaky.stats()
	if failures != 1 || attempts != 4 {
		t.Errorf("attempts/failures = %d/%d, want 4/1 (one failure, one full re-delivery)", attempts, failures)
	}

	calls, findings, err := flaky.Store.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || findings != 0 {
		t.Fatalf("store holds %d calls / %d findings, want exactly 2 / 0 — the retry minted new ids", calls, findings)
	}
	edges, err := flaky.Store.ListEdges(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].CallCount != 2 {
		t.Fatalf("edges = %+v, want one edge with call_count 2", edges)
	}
	// The ids the store holds are the ones now stamped on the queued records.
	for i := 0; i < recs.Len(); i++ {
		v, ok := recs.At(i).Attributes().Get(otlpattr.AttrCallID)
		if !ok || v.Str() == "" {
			t.Fatalf("record %d was not stamped with %s on the way through", i, otlpattr.AttrCallID)
		}
		if _, ok, err := flaky.Store.GetCall(v.Str()); err != nil || !ok {
			t.Errorf("stamped id %s of record %d is not the stored row (ok=%v err=%v)", v.Str(), i, ok, err)
		}
	}
}

// TestPoisonBatch_DroppedAfterOneAttempt (review of #46, 2026-09-08): a
// deterministic failure must not be retried for max_elapsed_time — that pins a
// queue consumer for 15 minutes on every re-delivery. Two shapes, one batch:
//   - a finding with no id is dropped by consumeLogs itself (the occurrence
//     ledger cannot make it idempotent) and the batch goes on;
//   - a record the STORE refuses (store.ErrRejected) fails the batch as
//     PERMANENT — one attempt, logged with the record id, no retry.
//
// A transient failure on the same exporter is still retried and lands.
func TestPoisonBatch_DroppedAfterOneAttempt(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	exp, flaky := newExporterLogging(t, fastRetry(t), zap.New(core))
	defer func() { _ = exp.Shutdown(context.Background()) }()

	ok1, poison, after := "0191e8c4-0000-7000-8000-000000000031", "0191e8c4-0000-7000-8000-000000000032", "0191e8c4-0000-7000-8000-000000000033"
	flaky.set(func(f *flakyStore) { f.rejectCallID = poison })

	ld := plog.NewLogs()
	recs := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	goldenCall(t, ok1).CopyTo(recs.AppendEmpty())
	if err := otlpattr.FindingToRecord(recs.AppendEmpty(), driftFinding("", ok1)); err != nil { // id-less
		t.Fatal(err)
	}
	goldenCall(t, poison).CopyTo(recs.AppendEmpty())
	goldenCall(t, after).CopyTo(recs.AppendEmpty())

	if err := exp.ConsumeLogs(context.Background(), ld); err != nil {
		t.Fatalf("ConsumeLogs must ACK (queue), got: %v", err)
	}
	waitFor(t, "the poison record to be attempted", func() bool {
		_, failures := flaky.stats()
		return failures >= 1
	})
	// Give the retry sender every chance to retry (it backs off 10 ms here):
	// a second attempt on the rejected record would show up as attempts > 2.
	time.Sleep(300 * time.Millisecond)
	if attempts, failures := flaky.stats(); attempts != 2 || failures != 1 {
		t.Fatalf("attempts/failures = %d/%d, want 2/1: ok1 written, poison refused ONCE, nothing retried", attempts, failures)
	}
	calls, findings, err := flaky.Store.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || findings != 0 {
		t.Fatalf("store holds %d calls / %d findings, want 1 / 0 (ok1 landed; the id-less finding was dropped; poison and what followed it went with the batch)", calls, findings)
	}
	if _, ok, _ := flaky.Store.GetCall(ok1); !ok {
		t.Errorf("ok1 must have landed before the batch was dropped")
	}
	var sawIDLess, sawRejected bool
	for _, entry := range logs.All() {
		switch {
		case strings.Contains(entry.Message, "without id"):
			sawIDLess = true
		case strings.Contains(entry.Message, "store rejected record"):
			sawRejected = true
			if entry.ContextMap()["id"] != poison {
				t.Errorf("the rejection log must name the record, got %v", entry.ContextMap())
			}
		}
	}
	if !sawIDLess || !sawRejected {
		t.Errorf("dropped records must be logged: id-less=%v rejected=%v (%d entries)", sawIDLess, sawRejected, logs.Len())
	}

	// Contrast: a TRANSIENT failure on the very next batch is retried and lands.
	flaky.set(func(f *flakyStore) {
		f.rejectCallID = ""
		f.failNth = f.attempts + 1 // the next write fails once
	})
	transient := "0191e8c4-0000-7000-8000-000000000034"
	if err := exp.ConsumeLogs(context.Background(), batch(t, []string{transient}, nil)); err != nil {
		t.Fatalf("ConsumeLogs (transient): %v", err)
	}
	waitFor(t, "the transiently failed write to be retried and land", func() bool {
		_, ok, _ := flaky.Store.GetCall(transient)
		return ok
	})
	if _, failures := flaky.stats(); failures != 2 {
		t.Errorf("failures = %d, want 2 (the rejection + the one transient failure)", failures)
	}
}
