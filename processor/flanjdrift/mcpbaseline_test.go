package flanjdrift

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/contract/diff"
	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// putMCP registers an observed MCP snapshot row the way the store persists
// one: role provider, format mcp, bound to the edge it was observed on.
func (f *fakeSource) putMCP(integration, host, loadedAt string, doc []byte) {
	f.infos = append(f.infos, model.SpecInfo{
		Integration: integration,
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatMCP,
		PeerHost:    host,
		Source:      model.SpecSourceObserved,
		LoadedAt:    loadedAt,
	})
	f.docs[integration] = doc
}

// mcpCallBatch is one MCP tools/call record to toolName, as a batch.
func mcpCallBatch(toolName string) plog.Logs {
	ld := plog.NewLogs()
	mcpCallRecord(ld, toolName, `{"amount":1200,"currency":"usd"}`)
	return ld
}

// findingsIn decodes the finding records a batch carries.
func findingsIn(t *testing.T, ld plog.Logs) []model.Finding {
	t.Helper()
	var out []model.Finding
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				if otlpattr.RecordType(recs.At(k)) != otlpattr.RecordTypeFinding {
					continue
				}
				f, err := otlpattr.FindingFromRecord(recs.At(k))
				if err != nil {
					t.Fatalf("decode finding: %v", err)
				}
				out = append(out, f)
			}
		}
	}
	return out
}

// mcpFindings runs one tools/call through the processor and returns the
// findings it produced.
func mcpFindings(t *testing.T, p *driftProcessor, toolName string) []model.Finding {
	t.Helper()
	out, err := p.processLogs(context.Background(), mcpCallBatch(toolName))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	return findingsIn(t, out)
}

// kicked reports whether an early refresh was requested, draining it.
func kicked(p *driftProcessor) bool {
	select {
	case <-p.kick:
		return true
	default:
		return false
	}
}

// The renamed list: get_balance became get_account_balance, schemas unchanged.
var mcpSnapshotRenamedJSON = func() string {
	const from, to = `"name":"get_balance"`, `"name":"get_account_balance"`
	i := len(mcpSnapshotJSON)
	for j := 0; j+len(from) <= len(mcpSnapshotJSON); j++ {
		if mcpSnapshotJSON[j:j+len(from)] == from {
			i = j
			break
		}
	}
	return mcpSnapshotJSON[:i] + to + mcpSnapshotJSON[i+len(from):]
}()

// TestMCPBaselineSeedsFromTheSource is the tiered defect, at the processor
// level. A FRONT has no store; until 2026-09-07 its MCP baseline was whatever
// that one process had witnessed, so a client calling a tool the server had
// renamed — observed through a SIBLING front — was captured, not judged.
//
// Here the only contract source is the store pod (a fake). After one refresh
// the store's snapshot is this processor's baseline: the very first
// tools/call it sees to a tool the list does not declare is a stale_client.
func TestMCPBaselineSeedsFromTheSource(t *testing.T) {
	src := newFakeSource()
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-07T10:00:00.000Z", []byte(mcpSnapshotJSON))
	p := processorWith(t, nil)
	p.src = src

	// Before any refresh: no baseline, so the call is captured, not judged —
	// and the processor asks for an early refresh, the way the REST path
	// does for an uncovered host.
	if fs := mcpFindings(t, p, "get_account_balance"); len(fs) != 0 {
		t.Fatalf("findings with no baseline = %+v, want none (captured, not judged)", fs)
	}
	if !kicked(p) {
		t.Fatal("an MCP call with no baseline did not ask for a refresh")
	}

	p.refreshSpecs()
	if src.fetches["acme-payments"] != 1 {
		t.Fatalf("fetches after first refresh = %v, want the snapshot downloaded once", src.fetches)
	}
	fs := mcpFindings(t, p, "get_account_balance")
	if len(fs) != 1 || fs[0].Kind != model.KindStaleClient || fs[0].Rule != drift.RuleToolNotListed || fs[0].Endpoint != "get_account_balance" {
		t.Fatalf("findings after seed = %+v, want one stale_client tool-not-listed on get_account_balance", fs)
	}
	if kicked(p) {
		t.Error("a judged MCP call asked for a refresh")
	}
	// A listed tool on the seeded baseline is judged conforming, not stale.
	if fs := mcpFindings(t, p, "get_balance"); len(fs) != 0 {
		t.Fatalf("listed tool produced %+v", fs)
	}

	// Steady state downloads nothing: the row did not move.
	p.refreshSpecs()
	if src.fetches["acme-payments"] != 1 {
		t.Fatalf("unchanged row re-downloaded: %v", src.fetches)
	}

	// The row moved — a sibling front observed the rename and the store pod
	// now holds the new list. Re-downloaded and adopted over the seeded
	// baseline, and the diff between the two rides the next batch as
	// definition_change findings (the sibling that first listed the rename
	// may have had no baseline to diff it against — review finding 4; a
	// front that did report it dedups by signature). The calls in that batch
	// and after are judged against the NEW list.
	src.docs["acme-payments"] = []byte(mcpSnapshotRenamedJSON)
	src.infos[0].LoadedAt = "2026-09-07T11:00:00.000Z"
	p.refreshSpecs()
	if src.fetches["acme-payments"] != 2 {
		t.Fatalf("moved row not re-downloaded: %v", src.fetches)
	}
	changes, calls := splitByKind(mcpFindings(t, p, "get_account_balance"), model.KindDefinitionChange)
	if len(calls) != 0 {
		t.Fatalf("renamed tool still stale after the store moved: %+v", calls)
	}
	if len(changes) == 0 {
		t.Fatal("adopting the store's newer list over the seeded baseline reported no definition_change")
	}
	for _, f := range changes {
		if f.SnapshotObservedFrom != "2026-09-07T10:00:00.000Z" || f.SnapshotObservedAt != "2026-09-07T11:00:00.000Z" {
			t.Errorf("adopted diff spans %s → %s, want the store's two rows", f.SnapshotObservedFrom, f.SnapshotObservedAt)
		}
	}
	fs = mcpFindings(t, p, "get_balance")
	if len(fs) != 1 || fs[0].Kind != model.KindStaleClient || fs[0].Endpoint != "get_balance" {
		t.Fatalf("pre-rename name after the store moved = %+v, want stale_client on get_balance", fs)
	}

	// A row that leaves the store and comes back is offered again.
	src.infos = nil
	p.refreshSpecs()
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-07T11:00:00.000Z", []byte(mcpSnapshotRenamedJSON))
	p.refreshSpecs()
	if src.fetches["acme-payments"] != 3 {
		t.Fatalf("returning row not re-offered: %v", src.fetches)
	}
}

// TestMCPBaselineLiveStateIsForwardedNotOverridden: the conflict rule from
// the other side. This processor observed a list NEWER than the store's — it
// is ahead, and the store catches up from the spec_info record it forwards
// (the announce/forward path, unchanged). The store's older row is fetched
// once, declined, and not fetched again.
func TestMCPBaselineLiveStateIsForwardedNotOverridden(t *testing.T) {
	src := newFakeSource()
	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-07T10:00:00.000Z", []byte(mcpSnapshotJSON))
	p := processorWith(t, nil)
	p.src = src

	// Observe the renamed list ourselves, later than the store's row.
	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotRenamedJSON)
	stamp(ld, "2026-09-07T12:00:00.000Z")
	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if c := countRecords(out); c[otlpattr.RecordTypeSpecInfo] != 1 {
		t.Fatalf("observed snapshot did not forward a spec_info record: %v", c)
	}

	p.refreshSpecs()
	if src.fetches["acme-payments"] != 1 {
		t.Fatalf("fetches = %v, want the older row read once", src.fetches)
	}
	// Live wins: the renamed tool is listed, the old name is stale.
	if fs := mcpFindings(t, p, "get_account_balance"); len(fs) != 0 {
		t.Fatalf("older store row overrode a newer live baseline: %+v", fs)
	}
	if fs := mcpFindings(t, p, "get_balance"); len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
		t.Fatalf("live baseline lost: %+v", fs)
	}
	// Declined rows are remembered, not re-fetched every tick.
	p.refreshSpecs()
	if src.fetches["acme-payments"] != 1 {
		t.Fatalf("declined row re-downloaded: %v", src.fetches)
	}
}

// stamp sets every record's timestamp in the batch, so a snapshot's
// observed_at is under the test's control.
func stamp(ld plog.Logs, iso string) {
	at, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		panic(err)
	}
	ts := pcommon.NewTimestampFromTime(at)
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				recs.At(k).SetTimestamp(ts)
			}
		}
	}
}

// TestMCPBaselineOneBadRowDoesNotBlindTheRest: a snapshot that will not
// parse costs its own edge and nothing else — and is fetched once, not every
// ten seconds.
func TestMCPBaselineOneBadRowDoesNotBlindTheRest(t *testing.T) {
	src := newFakeSource()
	src.putMCP("broken", "mcp.broken.test", "v1", []byte("this is not a tools/list"))
	src.putMCP("acme-payments", "mcp.acme.test", "v1", []byte(mcpSnapshotJSON))
	p := processorWith(t, nil)
	p.src = src

	infos, _ := src.listSpecs()
	adopted, _, errs := p.mcpSeeds.reconcile(infos, src, p.mcp)
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly the unparseable row", errs)
	}
	if len(adopted) != 1 || adopted[0].peerHost != "mcp.acme.test" {
		t.Fatalf("adopted = %+v, want the healthy row", adopted)
	}
	if _, _, errs := p.mcpSeeds.reconcile(infos, src, p.mcp); len(errs) != 0 || src.fetches["broken"] != 1 {
		t.Errorf("bad row refetched: errs=%v fetches=%v", errs, src.fetches)
	}
	if fs := mcpFindings(t, p, "nope"); len(fs) != 1 {
		t.Errorf("healthy edge lost to its broken sibling: %+v", fs)
	}
}

// TestMCPBaselineNilSafe: a processor with no source, no detector or no
// seeds tracker degrades to nothing, never to a panic on the refresh path.
func TestMCPBaselineNilSafe(t *testing.T) {
	var s *mcpSeeds
	if a, f, e := s.reconcile(nil, newFakeSource(), drift.NewMCPDetector()); a != nil || f != nil || e != nil {
		t.Error("nil tracker did something")
	}
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}
	p.refreshSpecs() // no src: silent
	src := newFakeSource()
	src.putMCP("acme-payments", "mcp.acme.test", "v1", []byte(mcpSnapshotJSON))
	if a, f, e := p.mcpSeeds.reconcile(src.infos, src, nil); a != nil || f != nil || e != nil {
		t.Error("nil detector was offered rows")
	}
}

// TestRestartSeedsTheMCPBaselineFromTheStore is the single-pod restart, against
// the real backend: the store holds the snapshot an earlier process persisted,
// and Start's first refresh makes it this process's baseline — the same path a
// tiered front takes over the contract channel, one implementation of
// specSource over.
func TestRestartSeedsTheMCPBaselineFromTheStore(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutSpecInfo(model.SpecInfo{
		Integration: "acme-payments", Role: model.SpecRoleProvider, Format: model.SpecFormatMCP,
		PeerHost: "mcp.acme.test", EdgeClass: "external", Source: model.SpecSourceObserved,
		LoadedAt: "2026-09-07T10:00:00.000Z",
	}, []byte(mcpSnapshotJSON)); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}

	p := &driftProcessor{
		cfg:   &Config{},
		mcp:   drift.NewMCPDetector(),
		specs: newSpecCache(),
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	host := extHost{exts: map[component.ID]component.Component{
		component.MustNewID("flanjstore"): &storeExtStub{st: st},
	}}
	if err := p.start(context.Background(), host); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = p.shutdown(context.Background()) })

	fs := mcpFindings(t, p, "get_account_balance")
	if len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
		t.Fatalf("first call after restart = %+v, want stale_client against the persisted baseline", fs)
	}
}

// splitByKind partitions findings into those of kind and the rest.
func splitByKind(fs []model.Finding, kind string) (of, rest []model.Finding) {
	for _, f := range fs {
		if f.Kind == kind {
			of = append(of, f)
		} else {
			rest = append(rest, f)
		}
	}
	return of, rest
}

// TestMCPBaselineAdoptionReportsTheDiffOnTheNextBatch is review finding 4 at
// the processor level. This front OBSERVED V1. The rename was first listed
// through a sibling front with no baseline, which had nothing to diff,
// reported nothing and forwarded V2; the store row moved. Adopting V2 over
// the live V1 reports the V1→V2 diff — and since the refresh loop has no
// batch of its own, the findings ride the next one, exactly once.
func TestMCPBaselineAdoptionReportsTheDiffOnTheNextBatch(t *testing.T) {
	src := newFakeSource()
	p := processorWith(t, nil)
	p.src = src

	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotJSON)
	stamp(ld, "2026-09-07T10:00:00.000Z")
	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	if fs := findingsIn(t, out); len(fs) != 0 {
		t.Fatalf("first observation reported %+v", fs)
	}

	src.putMCP("acme-payments", "mcp.acme.test", "2026-09-07T11:00:00.000Z", []byte(mcpSnapshotRenamedJSON))
	p.refreshSpecs()

	changes, calls := splitByKind(mcpFindings(t, p, "get_account_balance"), model.KindDefinitionChange)
	if len(calls) != 0 {
		t.Fatalf("renamed tool judged stale after adoption: %+v", calls)
	}
	if len(changes) == 0 {
		t.Fatal("adopting V2 over an observed V1 reported no definition_change")
	}
	renamed := false
	for _, f := range changes {
		if f.SnapshotObservedFrom != "2026-09-07T10:00:00.000Z" || f.SnapshotObservedAt != "2026-09-07T11:00:00.000Z" {
			t.Errorf("adopted diff spans %s → %s, want this front's V1 → the store's V2", f.SnapshotObservedFrom, f.SnapshotObservedAt)
		}
		if f.Rule == diff.RuleOperationRenamed && f.Endpoint == "get_balance" {
			renamed = true
		}
	}
	if !renamed {
		t.Errorf("no %s on get_balance among %+v", diff.RuleOperationRenamed, changes)
	}
	// Once: the batch after carries only its own findings.
	if fs := mcpFindings(t, p, "get_balance"); len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
		t.Fatalf("batch after the drain = %+v, want just the stale_client", fs)
	}
}

// mcpStorePod serves rows and documents the way the store pod's contract
// channel does, counting document downloads, so a front can be STARTED
// against it — the wiring under test, not a fake source handed in.
type mcpStorePod struct {
	*httptest.Server
	mu      sync.Mutex
	fetches map[string]int
}

func newMCPStorePod(t *testing.T, infos []model.SpecInfo, docs map[string][]byte) *mcpStorePod {
	t.Helper()
	pod := &mcpStorePod{fetches: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/contracts", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"contracts": infos})
	})
	mux.HandleFunc("GET /internal/contracts/doc", func(w http.ResponseWriter, r *http.Request) {
		integration := r.URL.Query().Get("integration")
		pod.mu.Lock()
		pod.fetches[integration]++
		pod.mu.Unlock()
		doc, ok := docs[integration]
		if !ok {
			http.Error(w, "no such contract", http.StatusNotFound)
			return
		}
		_, _ = w.Write(doc)
	})
	pod.Server = httptest.NewServer(mux)
	t.Cleanup(pod.Close)
	return pod
}

func (p *mcpStorePod) fetched(integration string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fetches[integration]
}

// TestMCPBaselineNeverSeedsLocalProcessRows: a stdio (local-process) row seeds
// nobody, over either source.
//
// A stdio MCP server has no host on the wire, so the SDK records its
// serverInfo.name as the peer_host — a name, not an identity. Every pod
// running its own `filesystem` subprocess writes ONE spec_infos row under that
// name, whatever each subprocess actually contains. #41 skipped such rows only
// over the store pod's channel, on the theory that a pod's own store holds
// only its own rows; a shared postgres is one database behind every pod, so it
// does not (see TestTwoPodsSharingAStoreNeverPingPongStdioBaselines).
func TestMCPBaselineNeverSeedsLocalProcessRows(t *testing.T) {
	rows := []model.SpecInfo{
		{Integration: "acme-payments", Role: model.SpecRoleProvider, Format: model.SpecFormatMCP, PeerHost: "mcp.acme.test",
			EdgeClass: model.EdgeClassExternal, Source: model.SpecSourceObserved, LoadedAt: "2026-09-08T10:00:00.000Z"},
		{Integration: "filesystem", Role: model.SpecRoleProvider, Format: model.SpecFormatMCP, PeerHost: "filesystem",
			EdgeClass: model.EdgeClassLocalProcess, Source: model.SpecSourceObserved, LoadedAt: "2026-09-08T10:00:00.000Z"},
	}
	docs := map[string][]byte{"acme-payments": []byte(mcpSnapshotJSON), "filesystem": []byte(stdioListRead)}

	// A tiered FRONT: no store, the store pod's channel as its only source.
	pod := newMCPStorePod(t, rows, docs)
	front := &driftProcessor{
		cfg:   &Config{StorePodEndpoint: pod.URL, StorePodToken: "shared"},
		mcp:   drift.NewMCPDetector(),
		specs: newSpecCache(),
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	if err := front.start(context.Background(), extHost{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = front.shutdown(context.Background()) })
	if !front.mcp.HasBaseline("mcp.acme.test", "client") {
		t.Fatal("the external row did not seed the front")
	}
	if front.mcp.HasBaseline("filesystem", "client") {
		t.Fatal("a local-process row seeded a front over the channel")
	}
	if n := pod.fetched("filesystem"); n != 0 {
		t.Errorf("the skipped row was downloaded %d times, want never", n)
	}
	if n := pod.fetched("acme-payments"); n != 1 {
		t.Errorf("the external row was downloaded %d times, want once", n)
	}

	// The SAME rows read from a pod's own co-located store. #41 seeded here;
	// on a shared postgres those rows are every sibling pod's too, so the
	// stdio one is skipped on this path as well and only the external row
	// seeds.
	src := newFakeSource()
	src.infos, src.docs = rows, docs
	own := processorWith(t, nil)
	own.src = src
	adopted, findings, errs := own.mcpSeeds.reconcile(rows, src, own.mcp)
	if len(errs) != 0 {
		t.Fatalf("co-located store: %v", errs)
	}
	if len(findings) != 0 {
		t.Errorf("seeding reported %d findings from rows nothing observed here", len(findings))
	}
	if len(adopted) != 1 || adopted[0].integration != "acme-payments" {
		t.Fatalf("co-located store adopted %+v, want the external row alone", adopted)
	}
	if own.mcp.HasBaseline("filesystem", "client") {
		t.Fatal("a local-process row seeded a pod from its own store — on a shared postgres that row is every pod's")
	}
}

// TestTwoPodsSharingAStoreNeverPingPongStdioBaselines is the defect #41 could
// not fix from inside its own change: two detectors over ONE store.
//
// `storeSpecSource` reads the shared database, so on a shared-postgres
// deployment a pod's "own" store serves every sibling pod's rows. Two pods
// each running their own build of a stdio server called `filesystem` write one
// row between them, and the row alternates as each re-observes. With the
// co-located path seeding local-process rows, each pod then adopts the other's
// list as a newer observation, diffs its own against it, and emits
// definition_change — forever, in both directions, over two servers that never
// drifted.
//
// Here pod-a runs a build exposing read_file and pod-b one exposing
// read_text_file. Each must keep judging its own subprocess: its own tool is
// declared, the sibling's is not, and no definition_change is ever reported.
func TestTwoPodsSharingAStoreNeverPingPongStdioBaselines(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	src := &countingSource{specSource: storeSpecSource{st: st}, fetches: map[string]int{}}

	newPod := func() *driftProcessor {
		p := processorWith(t, nil)
		p.src = src
		p.st = st // every pod of a shared-postgres deployment holds a handle
		return p
	}
	a, b := newPod(), newPod()

	observe := func(p *driftProcessor, list, at string) {
		t.Helper()
		ld := plog.NewLogs()
		mcpStdioSnapshotRecord(ld, "filesystem", list)
		stamp(ld, at)
		if _, err := p.processLogs(context.Background(), ld); err != nil {
			t.Fatalf("processLogs: %v", err)
		}
	}
	// judge drives one stdio tools/call through the pod and returns every
	// finding the batch carries — the call's own, plus anything the refresh
	// loop parked for it (holdFindings), which is where a seed's
	// definition_change would surface.
	judge := func(p *driftProcessor, tool string) []model.Finding {
		t.Helper()
		ld := plog.NewLogs()
		mcpStdioCallRecord(ld, "filesystem", tool)
		out, err := p.processLogs(context.Background(), ld)
		if err != nil {
			t.Fatalf("processLogs: %v", err)
		}
		return findingsIn(t, out)
	}

	// Round one: pod-a observes its build, pod-b observes its own a moment
	// later, so the shared row now holds pod-b's list.
	observe(a, stdioListRead, "2026-09-08T10:00:00.000Z")
	observe(b, stdioListReadText, "2026-09-08T10:00:05.000Z")
	a.refreshSpecs()
	b.refreshSpecs()

	if fs := judge(a, "read_file"); len(fs) != 0 {
		t.Fatalf("pod-a judged its OWN subprocess's tool against pod-b's list: %+v", fs)
	}
	if fs := judge(a, "read_text_file"); len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
		t.Fatalf("pod-a should not know pod-b's tool: got %+v, want one stale_client", fs)
	}

	// Round two: pod-a re-observes, so the row swings back. This is the flip
	// that made the old behaviour a permanent loop rather than a one-off.
	observe(a, stdioListRead, "2026-09-08T10:01:00.000Z")
	a.refreshSpecs()
	b.refreshSpecs()

	if fs := judge(b, "read_text_file"); len(fs) != 0 {
		t.Fatalf("pod-b judged its OWN subprocess's tool against pod-a's list: %+v", fs)
	}
	if fs := judge(b, "read_file"); len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
		t.Fatalf("pod-b should not know pod-a's tool: got %+v, want one stale_client", fs)
	}
	if n := src.fetches["filesystem"]; n != 0 {
		t.Errorf("a stdio row was downloaded %d times; it seeds nobody, so it should never be fetched", n)
	}

	// The row is still written and still listed — skipping the SEED must not
	// take the stdio server off the Contracts tab.
	infos, err := st.ListSpecInfos()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed bool
	for _, si := range infos {
		if si.Integration == "filesystem" {
			listed = true
			if si.EdgeClass != model.EdgeClassLocalProcess {
				t.Errorf("the stdio row lost its edge class: %q", si.EdgeClass)
			}
		}
	}
	if !listed {
		t.Error("the stdio server is no longer listed in the store")
	}
}

// Two builds of one stdio server, published under the same serverInfo.name —
// the shape a shared spec_infos row cannot tell apart.
const (
	stdioListRead = `{"tools":[{"name":"read_file","description":"Read a file",` +
		`"inputSchema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],` +
		`"serverInfo":{"name":"filesystem","version":"1.0.0"}}`
	stdioListReadText = `{"tools":[{"name":"read_text_file","description":"Read a text file",` +
		`"inputSchema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],` +
		`"serverInfo":{"name":"filesystem","version":"2.0.0"}}`
)

// mcpStdioSnapshotRecord is an observed tools/list from a STDIO server: there
// is no host on the wire, so the SDK records serverInfo.name as the peer host
// and classifies the edge local-process.
func mcpStdioSnapshotRecord(ld plog.Logs, serverName, snapshotJSON string) {
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	a := lr.Attributes()
	a.PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
	a.PutStr(otlpattr.AttrTransport, otlpattr.TransportMCP)
	a.PutStr(otlpattr.AttrDirection, "client")
	a.PutStr(otlpattr.AttrPeerHost, serverName)
	a.PutStr(otlpattr.AttrEdgeClass, model.EdgeClassLocalProcess)
	a.PutStr(otlpattr.AttrIntegration, serverName)
	a.PutStr(otlpattr.AttrMCPContractSnapshot, snapshotJSON)
	a.PutInt(otlpattr.AttrMCPToolCount, 1)
	a.PutStr(otlpattr.AttrMCPServerName, serverName)
	a.PutStr(otlpattr.AttrMCPServerVersion, "1.0.0")
}

// mcpStdioCallRecord is one tools/call on that same stdio edge.
func mcpStdioCallRecord(ld plog.Logs, serverName, toolName string) {
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	a := lr.Attributes()
	a.PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeCall)
	a.PutStr(otlpattr.AttrTransport, otlpattr.TransportMCP)
	a.PutStr(otlpattr.AttrDirection, "client")
	a.PutStr(otlpattr.AttrPeerHost, serverName)
	a.PutStr(otlpattr.AttrEdgeClass, model.EdgeClassLocalProcess)
	a.PutStr(otlpattr.AttrIntegration, serverName)
	a.PutStr(otlpattr.AttrMethod, "tools/call")
	a.PutStr(otlpattr.AttrRoute, "/"+toolName)
	a.PutStr(otlpattr.AttrMCPToolName, toolName)
	a.PutStr(otlpattr.AttrReqBody, `{"path":"/etc/hosts"}`)
	a.PutStr(otlpattr.AttrReqContentType, "application/json")
	a.PutStr(otlpattr.AttrRespBody, `{"content":[{"type":"text","text":"ok"}]}`)
	a.PutStr(otlpattr.AttrRespContent, "application/json")
	a.PutInt(otlpattr.AttrStatusCode, 200)
}

// countingSource counts document downloads through any specSource.
type countingSource struct {
	specSource
	fetches map[string]int
}

func (c *countingSource) specDoc(integration string) ([]byte, error) {
	c.fetches[integration]++
	return c.specSource.specDoc(integration)
}

// forwardSpecInfos plays the front→store hop: every spec_info record a front
// emitted lands in the store, as the store exporter lands it. Returns what
// was forwarded.
func forwardSpecInfos(t *testing.T, st store.Store, ld plog.Logs) (forwarded []model.SpecInfo) {
	t.Helper()
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				if otlpattr.RecordType(lr) != otlpattr.RecordTypeSpecInfo {
					continue
				}
				info, raw, err := otlpattr.SpecInfoFromRecord(lr)
				if err != nil {
					t.Fatalf("decode spec_info: %v", err)
				}
				if err := st.PutSpecInfo(info, raw); err != nil {
					t.Fatalf("store spec_info: %v", err)
				}
				forwarded = append(forwarded, info)
			}
		}
	}
	return forwarded
}

// TestTwoFrontsObservingTheSameListNeverMoveTheRow is review finding 3 end to
// end, over a real store standing in for the store pod. front-a and front-b
// observe the IDENTICAL tools/list, each before its own tick could seed it,
// and each forwards the row with its OWN first-sighting stamp. Before the fix
// the store row alternated between the two stamps on every re-observation,
// every flip re-downloaded the document on every front, and the UI's "since
// this snapshot" anchor moved with it. Both halves are exercised here: the
// store keeps loaded_at for an unchanged document, and the detector converges
// each front on the earlier stamp, so both forward the same one from then on.
func TestTwoFrontsObservingTheSameListNeverMoveTheRow(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	src := &countingSource{specSource: storeSpecSource{st: st}, fetches: map[string]int{}}
	a := processorWith(t, nil)
	a.src = src
	b := processorWith(t, nil)
	b.src = src

	observe := func(p *driftProcessor, at string) string {
		t.Helper()
		ld := plog.NewLogs()
		mcpSnapshotRecord(ld, mcpSnapshotJSON)
		stamp(ld, at)
		out, err := p.processLogs(context.Background(), ld)
		if err != nil {
			t.Fatalf("processLogs: %v", err)
		}
		fw := forwardSpecInfos(t, st, out)
		if len(fw) != 1 {
			t.Fatalf("observation at %s forwarded %d spec_info records, want 1", at, len(fw))
		}
		return fw[0].LoadedAt
	}
	rowStamp := func() string {
		t.Helper()
		infos, err := st.ListSpecInfos()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, si := range infos {
			if si.Integration == "acme-payments" {
				return si.LoadedAt
			}
		}
		t.Fatal("acme-payments row missing from the store")
		return ""
	}
	const t1, t2 = "2026-09-07T10:00:00.000Z", "2026-09-07T10:00:05.000Z"

	// front-a first sights the list at t1; front-b sights the same list at
	// t2, before its tick — each forwards its own stamp.
	if got := observe(a, t1); got != t1 {
		t.Fatalf("front-a forwarded %s, want %s", got, t1)
	}
	if got := observe(b, t2); got != t2 {
		t.Fatalf("front-b forwarded %s, want its own first sighting %s", got, t2)
	}
	// Store side: an unchanged document does not move the anchor.
	if got := rowStamp(); got != t1 {
		t.Fatalf("front-b's re-observation moved the row to %s", got)
	}

	// A tick on each: front-a learns nothing (its own stamp); front-b
	// converges on the earlier one. Each downloads the row once to find out.
	a.refreshSpecs()
	b.refreshSpecs()
	if n := src.fetches["acme-payments"]; n != 2 {
		t.Fatalf("fetches after the first ticks = %d, want one per front", n)
	}
	// Detector side: front-b now forwards front-a's stamp, and so does front-a.
	if got := observe(b, "2026-09-07T10:01:00.000Z"); got != t1 {
		t.Fatalf("front-b still forwards its own stamp after seeding: %s, want %s", got, t1)
	}
	if got := observe(a, "2026-09-07T10:01:30.000Z"); got != t1 {
		t.Fatalf("front-a forwards %s, want %s", got, t1)
	}
	if got := rowStamp(); got != t1 {
		t.Fatalf("row moved to %s after both fronts converged", got)
	}
	// Steady state: ticks download nothing — the row never moved.
	for i := 0; i < 3; i++ {
		a.refreshSpecs()
		b.refreshSpecs()
	}
	if n := src.fetches["acme-payments"]; n != 2 {
		t.Fatalf("unchanged row re-downloaded: fetches = %d, want still 2", n)
	}
	// And both fronts judge calls against the one baseline.
	for name, p := range map[string]*driftProcessor{"a": a, "b": b} {
		if fs := mcpFindings(t, p, "nope"); len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
			t.Fatalf("front-%s judged %+v, want one stale_client", name, fs)
		}
	}
}
