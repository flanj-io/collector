package flanjdrift

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
)

// snapshotOfSize returns a parseable tools/list JSON of at least n bytes.
func snapshotOfSize(t *testing.T, n int) []byte {
	t.Helper()
	doc := []byte(`{"tools":[{"name":"pad","description":"` + strings.Repeat("p", n) + `"}]}`)
	var probe struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(doc, &probe); err != nil {
		t.Fatalf("the padded snapshot is not valid JSON: %v", err)
	}
	if len(doc) < n {
		t.Fatalf("padded snapshot is %d bytes, want at least %d", len(doc), n)
	}
	return doc
}

// observedFront builds a front-shaped processor with a countable logger.
func observedFront(t *testing.T, src specSource) (*driftProcessor, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	p := &driftProcessor{
		cfg:    &Config{},
		logger: zap.New(core),
		mcp:    drift.NewMCPDetector(),
		specs:  newSpecCache(),
		src:    src,
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	return p, logs
}

// TestOverCapRowIsSkippedWithoutARequest: a front that can see from the LISTING
// that a document is past the cap does not ask for it.
//
// Before doc_bytes the only way to learn this was to attempt the transfer and
// read the 413, so every front re-requested every oversized document on every
// ten-second tick, forever — a request the store pod answered by reading the
// whole oversized document into its own heap.
func TestOverCapRowIsSkippedWithoutARequest(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.putMCP("acme-tools", "mcp.acme.test", "2026-09-08T10:00:00Z", snapshotOfSize(t, maxSpecBytes+4096))

	det := drift.NewMCPDetector()
	for i := 0; i < 5; i++ {
		var seeds mcpSeeds
		_, _, errs := seeds.reconcile(src.infos, src, det)
		if len(errs) != 1 {
			t.Fatalf("tick %d: errs = %v, want exactly the cap refusal", i, errs)
		}
		if _, ok := asOverCap(errs[0]); !ok {
			t.Fatalf("tick %d: %v is not the typed cap condition, so it cannot be deduped", i, errs[0])
		}
	}
	if n := src.fetches["acme-tools"]; n != 0 {
		t.Errorf("the front requested the oversized document %d times, want 0", n)
	}
	if det.HasBaseline("mcp.acme.test", "client") {
		t.Error("an over-cap snapshot became the baseline — nothing partial may ever reach the detector")
	}
}

// TestOverCapKeepsTheBaselineItAlreadyHad: the row is skipped, not applied and
// not dropped. A front that already seeded this edge keeps judging against what
// it has; only the update is refused.
func TestOverCapKeepsTheBaselineItAlreadyHad(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-08T10:00:00Z", []byte(mcpSnapshotJSON))

	det := drift.NewMCPDetector()
	var seeds mcpSeeds
	if _, _, errs := seeds.reconcile(src.infos, src, det); len(errs) != 0 {
		t.Fatalf("seeding a normal row failed: %v", errs)
	}
	if !det.HasBaseline("mcp.acme.test", "client") {
		t.Fatal("the first snapshot did not seed the detector")
	}

	// The server's catalogue grows past the cap and the store row moves.
	src.infos[0].LoadedAt = "2026-09-08T11:00:00Z"
	src.infos[0].DocBytes = maxSpecBytes + 1
	src.docs["acme-payments"] = snapshotOfSize(t, maxSpecBytes+1)

	_, _, errs := seeds.reconcile(src.infos, src, det)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the cap refusal", errs)
	}
	if !det.HasBaseline("mcp.acme.test", "client") {
		t.Error("the refusal cost the edge the baseline it already had")
	}
}

// TestCoLocatedStoreAppliesNoCap: on a SINGLE pod the document never crosses a
// boundary, so nothing refuses it and detection on that edge is untouched.
//
// This is the regression the cap check could easily have introduced: enforcing
// a channel's limit on a read that takes no channel would break working
// detection to satisfy a rule that does not apply.
func TestCoLocatedStoreAppliesNoCap(t *testing.T) {
	src := newFakeSource() // enforcesCap stays false — the co-located semantics
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-08T10:00:00Z", []byte(mcpSnapshotJSON))
	src.infos[0].DocBytes = maxSpecBytes * 4

	det := drift.NewMCPDetector()
	var seeds mcpSeeds
	adopted, _, errs := seeds.reconcile(src.infos, src, det)
	if len(errs) != 0 {
		t.Fatalf("a co-located read was refused for size: %v", errs)
	}
	if len(adopted) != 1 {
		t.Fatalf("adopted %d baselines, want 1 — the single-pod path must be untouched", len(adopted))
	}

	// And the real co-located source says the same thing for any row at all.
	if oc := (storeSpecSource{}).overCap(model.SpecInfo{DocBytes: maxSpecBytes * 100}); oc != nil {
		t.Errorf("storeSpecSource refused a row for size: %v", oc)
	}
}

// TestOverCapLogsTheStartNotEveryTick: the front logs the condition when it
// starts and stays quiet while it holds.
//
// The refresh loop re-derives it every ten seconds by design — that is what
// lets it clear on its own — so without the transition gate this is six
// identical Warn lines a minute per oversized edge, forever.
func TestOverCapLogsTheStartNotEveryTick(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.putMCP("acme-tools", "mcp.acme.test", "2026-09-08T10:00:00Z", snapshotOfSize(t, maxSpecBytes+4096))
	p, logs := observedFront(t, src)

	for i := 0; i < 12; i++ {
		p.refreshSpecs()
	}

	raised := logs.FilterMessage(msgSpecOverCap).All()
	if len(raised) != 1 {
		t.Fatalf("logged the refusal %d times over 12 refresh ticks, want exactly 1", len(raised))
	}
	fields := raised[0].ContextMap()
	if fields["integration"] != "acme-tools" {
		t.Errorf("integration = %v, want acme-tools", fields["integration"])
	}
	if fields["peer_host"] != "mcp.acme.test" {
		t.Errorf("peer_host = %v, want mcp.acme.test", fields["peer_host"])
	}
	if fields["bytes"] == nil {
		t.Error("the line carries no size — the operator acts on how far over the cap it is")
	}
	// And the generic per-row line must NOT also fire: one condition, one line.
	if n := logs.FilterMessage("mcp baseline seed failed").Len(); n != 0 {
		t.Errorf("the cap condition also reported %d times as a generic seed failure", n)
	}
}

// TestOverCapLogsWhenItClears: the end of the condition is the other event.
// Without it an operator who split the catalogue gets no confirmation from the
// front that the edge is being read again.
func TestOverCapLogsWhenItClears(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-08T10:00:00Z", snapshotOfSize(t, maxSpecBytes+4096))
	p, logs := observedFront(t, src)

	p.refreshSpecs()
	p.refreshSpecs()
	if n := logs.FilterMessage(msgSpecOverCap).Len(); n != 1 {
		t.Fatalf("the refusal was logged %d times, want 1", n)
	}

	src.infos[0].LoadedAt = "2026-09-08T11:00:00Z"
	src.infos[0].DocBytes = len(mcpSnapshotJSON)
	src.docs["acme-payments"] = []byte(mcpSnapshotJSON)
	p.refreshSpecs()

	cleared := logs.FilterMessage(msgSpecOverCapCleared).All()
	if len(cleared) != 1 {
		t.Fatalf("logged the clear %d times, want exactly 1", len(cleared))
	}
	if cleared[0].ContextMap()["integration"] != "acme-payments" {
		t.Errorf("clear line names %v, want acme-payments", cleared[0].ContextMap()["integration"])
	}
	if !p.mcp.HasBaseline("mcp.acme.test", "client") {
		t.Error("the edge did not seed once the document fit")
	}
	for i := 0; i < 4; i++ {
		p.refreshSpecs()
	}
	if n := logs.FilterMessage(msgSpecOverCapCleared).Len(); n != 1 {
		t.Errorf("the clear repeated %d times", n)
	}
}

// TestTransientFailuresStillReportEveryTick: the transition gate covers the
// STANDING condition only. A timeout or a 503 is a fresh event each time and
// must keep reporting — silencing those would be the opposite bug.
func TestTransientFailuresStillReportEveryTick(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-08T10:00:00Z", []byte(mcpSnapshotJSON))
	src.docErrOn = "acme-payments"
	p, logs := observedFront(t, src)

	for i := 0; i < 4; i++ {
		p.refreshSpecs()
	}
	if n := logs.FilterMessage("mcp baseline seed failed").Len(); n != 4 {
		t.Errorf("a transient failure reported %d times over 4 ticks, want 4", n)
	}
	if n := logs.FilterMessage(msgSpecOverCap).Len(); n != 0 {
		t.Errorf("a transient failure was filed as the cap condition %d times", n)
	}
}

// TestStorePod413IsStillHonoured: a store pod on an image that predates
// doc_bytes reports nothing in its listing and refuses at transfer time. The
// front must recognise THAT as the same condition — one log line, not one per
// tick — and must still never accept a partial document.
func TestStorePod413IsStillHonoured(t *testing.T) {
	rows := []model.SpecInfo{{
		Integration: "acme-tools",
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatMCP,
		PeerHost:    "mcp.acme.test",
		Source:      model.SpecSourceObserved,
		LoadedAt:    "2026-09-08T10:00:00Z",
		// No DocBytes: the older store pod does not measure.
	}}
	asked := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/contracts", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"contracts": rows})
	})
	mux.HandleFunc("GET /internal/contracts/doc", func(w http.ResponseWriter, _ *http.Request) {
		asked++
		http.Error(w, "contract document larger than the cap", http.StatusRequestEntityTooLarge)
	})
	pod := httptest.NewServer(mux)
	t.Cleanup(pod.Close)

	p, logs := observedFront(t, newRemoteSpecSource(pod.URL, "shared"))
	for i := 0; i < 6; i++ {
		p.refreshSpecs()
	}
	if asked == 0 {
		t.Fatal("the front never asked, so the 413 path was not exercised")
	}
	if n := logs.FilterMessage(msgSpecOverCap).Len(); n != 1 {
		t.Fatalf("logged the 413 refusal %d times over 6 ticks, want exactly 1", n)
	}
	if p.mcp.HasBaseline("mcp.acme.test", "client") {
		t.Error("a refused document seeded the detector anyway")
	}
}

// TestOverCapOnAnOpenAPIRowKeepsTheCachedContract: the OpenAPI half of the same
// channel. The over-cap row is skipped, the cache keeps every other contract,
// and nothing partial is parsed.
func TestOverCapOnAnOpenAPIRowKeepsTheCachedContract(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.put("api-acme-test", "api.acme.test", "2026-08-31T10:00:00Z", specV1(t))
	src.put("api-big-test", "api.big.test", "2026-08-31T10:00:00Z", specV1(t))
	src.infos[1].DocBytes = maxSpecBytes + 1

	c := newSpecCache()
	changed, errs := c.refresh(src)
	if len(changed) != 1 || changed[0] != "api.acme.test" {
		t.Errorf("changed = %v, want only the row that fits", changed)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the one cap refusal", errs)
	}
	if oc, ok := asOverCap(errs[0]); !ok || oc.integration != "api-big-test" {
		t.Fatalf("errs[0] = %v, want the typed cap condition for api-big-test", errs[0])
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("the neighbouring contract lost its cache entry")
	}
	if _, ok := c.lookup("api.big.test"); ok {
		t.Error("an over-cap document was parsed into the cache")
	}
	if n := src.fetches["api-big-test"]; n != 0 {
		t.Errorf("the over-cap document was requested %d times, want 0", n)
	}
}

// TestOverCapClearsWhenTheRowDisappears: an oversized server that is removed
// altogether clears the condition — no stale line claiming a refusal for a row
// nobody holds.
func TestOverCapClearsWhenTheRowDisappears(t *testing.T) {
	src := newFakeSource()
	src.enforcesCap = true
	src.putMCP("acme-tools", "mcp.acme.test", "2026-09-08T10:00:00Z", snapshotOfSize(t, maxSpecBytes+1))
	p, logs := observedFront(t, src)
	p.refreshSpecs()

	src.infos = nil
	p.refreshSpecs()

	if n := logs.FilterMessage(msgSpecOverCapCleared).Len(); n != 1 {
		t.Errorf("removing the row cleared the condition %d times, want 1", n)
	}
	if p.overCap.Held("acme-tools") {
		t.Error("the condition is still held for a row the store no longer lists")
	}
}
