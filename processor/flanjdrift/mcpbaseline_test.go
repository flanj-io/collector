package flanjdrift

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

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
	// now holds the new list. Re-downloaded, adopted; the refresh itself
	// emits nothing (the sibling reported the change), and the next calls
	// are judged against the NEW list.
	src.docs["acme-payments"] = []byte(mcpSnapshotRenamedJSON)
	src.infos[0].LoadedAt = "2026-09-07T11:00:00.000Z"
	p.refreshSpecs()
	if src.fetches["acme-payments"] != 2 {
		t.Fatalf("moved row not re-downloaded: %v", src.fetches)
	}
	if fs := mcpFindings(t, p, "get_account_balance"); len(fs) != 0 {
		t.Fatalf("renamed tool still stale after the store moved: %+v", fs)
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
	adopted, errs := p.mcpSeeds.reconcile(infos, src, p.mcp)
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly the unparseable row", errs)
	}
	if len(adopted) != 1 || adopted[0].peerHost != "mcp.acme.test" {
		t.Fatalf("adopted = %+v, want the healthy row", adopted)
	}
	if _, errs := p.mcpSeeds.reconcile(infos, src, p.mcp); len(errs) != 0 || src.fetches["broken"] != 1 {
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
	if a, e := s.reconcile(nil, newFakeSource(), drift.NewMCPDetector()); a != nil || e != nil {
		t.Error("nil tracker did something")
	}
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}
	p.refreshSpecs() // no src: silent
	src := newFakeSource()
	src.putMCP("acme-payments", "mcp.acme.test", "v1", []byte(mcpSnapshotJSON))
	if a, e := p.mcpSeeds.reconcile(src.infos, src, nil); a != nil || e != nil {
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
