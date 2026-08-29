package store

// Multi-pod correctness tests: several OpenPostgres handles ("pods") over ONE
// shared database, written to concurrently. This is the proof that the
// postgres backend upholds the store invariants (dedup, idempotency, pinning,
// eviction convergence) without the sqlite backend's process mutex. Postgres
// only — two handles on one sqlite file would violate its one-pod-per-file
// contract — and skipped unless FLANJ_TEST_PG_DSN is set.

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// pgPods opens n independent store handles on one freshly reset database.
func pgPods(t *testing.T, n, maxRows int, maxBytes int64) []Store {
	t.Helper()
	dsn := os.Getenv("FLANJ_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("FLANJ_TEST_PG_DSN not set — skipping postgres multi-pod test")
	}
	resetPG(t, dsn)
	pods := make([]Store, n)
	for i := range pods {
		s, err := OpenPostgres(dsn, maxRows, maxBytes)
		if err != nil {
			t.Fatalf("open pod %d: %v", i, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		pods[i] = s
	}
	return pods
}

// drain fails the test on the first error collected from the worker pods.
func drain(t *testing.T, errCh chan error) {
	t.Helper()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent worker: %v", err)
	}
}

// TestPGMultiPod_FindingDedupStorm: every pod concurrently reports the SAME
// drift signature with distinct candidate finding ids. Exactly one finding row
// must survive, its occurrence_count must equal the total number of reports
// (none lost, none double-counted), its id must be one of the candidates, and
// the edge's drift_count must be bumped exactly once.
func TestPGMultiPod_FindingDedupStorm(t *testing.T) {
	pods := pgPods(t, 3, 0, 0)
	call := makeEdgeCall(0, "api.acme.test", "client", "external")
	if err := pods[0].InsertCall(call); err != nil {
		t.Fatalf("seed call: %v", err)
	}

	const perPod = 200
	candidates := make(map[string]bool)
	for pi := range pods {
		for i := 0; i < perPod; i++ {
			candidates[fmt.Sprintf("0191e8c4-dddd-7000-8%03d-%012d", pi, i)] = true
		}
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(pods)*perPod)
	for pi, p := range pods {
		wg.Add(1)
		go func(pi int, p Store) {
			defer wg.Done()
			for i := 0; i < perPod; i++ {
				fid := fmt.Sprintf("0191e8c4-dddd-7000-8%03d-%012d", pi, i)
				if err := p.InsertFinding(driftFinding(fid, call.ID)); err != nil {
					errCh <- fmt.Errorf("pod %d insert %d: %w", pi, i, err)
					return
				}
			}
		}(pi, p)
	}
	wg.Wait()
	drain(t, errCh)

	findings, err := pods[1].ListFindings(10)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("dedup failed across pods: %d findings, want 1", len(findings))
	}
	f := findings[0]
	if want := len(pods) * perPod; f.OccurrenceCount != want {
		t.Errorf("occurrence_count = %d, want %d (no lost or doubled reports)", f.OccurrenceCount, want)
	}
	if !candidates[f.ID] {
		t.Errorf("finding id %q is not one of the submitted candidates", f.ID)
	}
	// Read again from another pod — the winning id must be stable.
	f2, ok, err := pods[2].GetFinding(f.ID)
	if err != nil || !ok {
		t.Fatalf("finding not readable from another pod (ok=%v err=%v)", ok, err)
	}
	if f2.Signature != f.Signature {
		t.Errorf("signature mismatch across pods: %q != %q", f2.Signature, f.Signature)
	}
	edges, err := pods[0].ListEdges(true)
	if err != nil || len(edges) != 1 {
		t.Fatalf("edges (err=%v len=%d)", err, len(edges))
	}
	if edges[0].DriftCount != 1 {
		t.Errorf("edge drift_count = %d, want 1 (bumped only by the winning insert)", edges[0].DriftCount)
	}
}

// TestPGMultiPod_CallIdempotencyAndEdgeCounts: all pods insert the SAME set of
// call ids concurrently (the at-least-once delivery worst case). The store must
// hold each call once and the edge call_count must equal the number of DISTINCT
// calls — replays and racing duplicates never double-count.
func TestPGMultiPod_CallIdempotencyAndEdgeCounts(t *testing.T) {
	pods := pgPods(t, 3, 0, 0)
	const distinct = 300

	var wg sync.WaitGroup
	errCh := make(chan error, len(pods))
	for pi, p := range pods {
		wg.Add(1)
		go func(pi int, p Store) {
			defer wg.Done()
			for i := 0; i < distinct; i++ {
				if err := p.InsertCall(makeEdgeCall(i, "api.acme.test", "client", "external")); err != nil {
					errCh <- fmt.Errorf("pod %d insert %d: %w", pi, i, err)
					return
				}
			}
		}(pi, p)
	}
	wg.Wait()
	drain(t, errCh)

	calls, _, err := pods[0].Counts()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if calls != distinct {
		t.Errorf("stored calls = %d, want %d", calls, distinct)
	}
	edges, err := pods[1].ListEdges(true)
	if err != nil || len(edges) != 1 {
		t.Fatalf("edges (err=%v len=%d)", err, len(edges))
	}
	if edges[0].CallCount != distinct {
		t.Errorf("edge call_count = %d, want %d (exactly one count per distinct call)", edges[0].CallCount, distinct)
	}
}

// TestPGMultiPod_EvictionConverges: pods hammer inserts far past the row cap
// concurrently. Eviction is best-effort per pod (advisory try-lock), so the
// window may transiently overshoot — but after the storm quiesces, one more
// insert must drain it back to the cap, with the pinned row retained. The test
// timeout is the deadlock detector.
func TestPGMultiPod_EvictionConverges(t *testing.T) {
	const cap = 50
	pods := pgPods(t, 3, cap, 0)

	// Pin one call up front so eviction pressure has evidence to preserve.
	pinned := makeEdgeCall(999999, "api.acme.test", "client", "external")
	if err := pods[0].InsertCall(pinned); err != nil {
		t.Fatalf("seed pinned call: %v", err)
	}
	if err := pods[0].InsertFinding(driftFinding("0191e8c4-ffff-7000-8000-00000000cc01", pinned.ID)); err != nil {
		t.Fatalf("pin via finding: %v", err)
	}

	const perPod = 300
	var wg sync.WaitGroup
	errCh := make(chan error, len(pods))
	for pi, p := range pods {
		wg.Add(1)
		go func(pi int, p Store) {
			defer wg.Done()
			for i := 0; i < perPod; i++ {
				// Distinct id ranges per pod: this storm is about volume, not replays.
				if err := p.InsertCall(makeCall(pi*perPod + i)); err != nil {
					errCh <- fmt.Errorf("pod %d insert %d: %w", pi, i, err)
					return
				}
			}
		}(pi, p)
	}
	wg.Wait()
	drain(t, errCh)

	// Quiesced: a final insert takes the eviction lock uncontended and drains
	// any overshoot left from skipped best-effort passes.
	if err := pods[1].InsertCall(makeCall(999998)); err != nil {
		t.Fatalf("final insert: %v", err)
	}

	rows, _, err := pods[2].Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if rows != cap {
		t.Errorf("row count after quiesce = %d, want %d", rows, cap)
	}
	if _, ok, _ := pods[0].GetCall(pinned.ID); !ok {
		t.Errorf("pinned call evicted under concurrent pressure — evidence lost")
	}
}

// TestPGMultiPod_CrossPodPromote: evidence pinned by pod A is promoted (flag
// succeeded) via pod B, after which it must re-enter the eviction pool and be
// evicted by traffic on pod C.
func TestPGMultiPod_CrossPodPromote(t *testing.T) {
	const cap = 5
	pods := pgPods(t, 3, cap, 0)

	pinned := makeEdgeCall(1000, "api.acme.test", "client", "external")
	if err := pods[0].InsertCall(pinned); err != nil {
		t.Fatalf("insert pinned call: %v", err)
	}
	if err := pods[0].InsertFinding(driftFinding("0191e8c4-ffff-7000-8000-00000000dd01", pinned.ID)); err != nil {
		t.Fatalf("pin via finding: %v", err)
	}
	for i := 0; i < 50; i++ {
		if err := pods[1].InsertCall(makeCall(i)); err != nil {
			t.Fatalf("flood insert %d: %v", i, err)
		}
	}
	if _, ok, _ := pods[2].GetCall(pinned.ID); !ok {
		t.Fatalf("pinned call evicted before promote")
	}

	if err := pods[1].MarkPromoted(pinned.ID); err != nil {
		t.Fatalf("promote from another pod: %v", err)
	}
	for i := 50; i < 60; i++ {
		if err := pods[2].InsertCall(makeCall(i)); err != nil {
			t.Fatalf("post-promote insert %d: %v", i, err)
		}
	}
	if _, ok, _ := pods[0].GetCall(pinned.ID); ok {
		t.Errorf("promoted call should re-enter the eviction pool and evict")
	}
}

// TestPGMultiPod_LatePinRace: one pod inserts a call while another pod
// concurrently inserts the finding that references it — many rounds, each with
// a fresh call id and its own signature, so the two writers genuinely race on
// the same id in both orders. Whatever the interleaving, the call must end up
// pinned (it survives a flood past the cap) and the edge drift_count must be
// bumped exactly once per finding — never zero (write-skew: neither side saw
// the other's uncommitted row), never twice (both sides bumped). This is the
// proof of the per-call advisory lock (pgLockNSCallPin) plus the late pin.
func TestPGMultiPod_LatePinRace(t *testing.T) {
	const (
		rounds = 300
		cap    = 10
	)
	pods := pgPods(t, 3, cap, 0)

	var wg sync.WaitGroup
	errCh := make(chan error, 2*rounds)
	ids := make([]string, rounds)
	for r := 0; r < rounds; r++ {
		call := makeEdgeCall(100000+r, "api.acme.test", "client", "external")
		ids[r] = call.ID
		f := driftFinding(fmt.Sprintf("0191e8c4-aaaa-7000-8000-%012d", r), call.ID)
		f.Rule = fmt.Sprintf("type-mismatch-%d", r) // one signature per round
		f.Signature = f.ComputeSignature()
		wg.Add(2)
		go func(c model.RedactedCall) {
			defer wg.Done()
			if err := pods[0].InsertCall(c); err != nil {
				errCh <- fmt.Errorf("pod 0 insert call %s: %w", c.ID, err)
			}
		}(call)
		go func(f model.Finding) {
			defer wg.Done()
			if err := pods[1].InsertFinding(f); err != nil {
				errCh <- fmt.Errorf("pod 1 insert finding %s: %w", f.ID, err)
			}
		}(f)
		wg.Wait() // race within a round; rounds sequential so the flood is meaningful
	}
	drain(t, errCh)

	// Flood from a third pod: everything unpinned must leave; every raced call
	// must stay (it is referenced by a finding, whichever side pinned it).
	for i := 0; i < 5*cap; i++ {
		if err := pods[2].InsertCall(makeCall(i)); err != nil {
			t.Fatalf("flood insert %d: %v", i, err)
		}
	}
	for r, id := range ids {
		if _, ok, _ := pods[2].GetCall(id); !ok {
			t.Fatalf("round %d: raced call %s was evicted — pin lost under concurrency", r, id)
		}
	}
	if _, findings, err := pods[2].Counts(); err != nil || findings != rounds {
		t.Fatalf("findings = %d (%v), want %d", findings, err, rounds)
	}
	edges, err := pods[2].ListEdges(true)
	if err != nil || len(edges) != 1 {
		t.Fatalf("edges = %d (%v), want 1", len(edges), err)
	}
	if edges[0].DriftCount != rounds {
		t.Errorf("edge drift_count = %d, want exactly %d (one bump per finding, regardless of arrival order)", edges[0].DriftCount, rounds)
	}
	if edges[0].CallCount != rounds {
		t.Errorf("edge call_count = %d, want %d", edges[0].CallCount, rounds)
	}
}
