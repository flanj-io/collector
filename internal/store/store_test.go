package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// testBackend opens stores for one backend under test. open gives a FRESH
// store (empty data); reopen reconnects to the SAME data after Close — the
// restart simulation. The sqlite backend always runs; the postgres backend
// runs when FLANJ_TEST_PG_DSN points at a scratch database (see CI).
type testBackend struct {
	name  string
	pgDSN string // "" = sqlite
	path  string // sqlite file of the last open
}

func forEachBackend(t *testing.T, fn func(t *testing.T, b *testBackend)) {
	t.Helper()
	backends := []*testBackend{{name: "sqlite"}}
	if dsn := os.Getenv("FLANJ_TEST_PG_DSN"); dsn != "" {
		backends = append(backends, &testBackend{name: "postgres", pgDSN: dsn})
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) { fn(t, b) })
	}
}

func (b *testBackend) open(t *testing.T, maxRows int, maxBytes int64) Store {
	t.Helper()
	if b.pgDSN != "" {
		resetPG(t, b.pgDSN)
	} else {
		b.path = filepath.Join(t.TempDir(), "flanj.db")
	}
	return b.reopen(t, maxRows, maxBytes)
}

func (b *testBackend) reopen(t *testing.T, maxRows int, maxBytes int64) Store {
	t.Helper()
	var (
		s   Store
		err error
	)
	if b.pgDSN != "" {
		s, err = OpenPostgres(b.pgDSN, maxRows, maxBytes)
	} else {
		s, err = OpenSQLite(b.path, maxRows, maxBytes)
	}
	if err != nil {
		t.Fatalf("open %s store: %v", b.name, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// resetPG empties the shared scratch database between tests (the schema, if
// present, survives — OpenPostgres re-applies it idempotently anyway).
func resetPG(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg for reset: %v", err)
	}
	defer db.Close()
	// Truncate whichever of the store's tables exist (a test database that predates
	// a newer table must not break the reset; the schema is applied on open).
	if _, err := db.Exec(`DO $$ DECLARE t text; BEGIN
		FOR t IN SELECT table_name FROM information_schema.tables
		          WHERE table_schema = 'public'
		            AND table_name IN ('calls','findings','edges','spec_infos','settings') LOOP
			EXECUTE format('TRUNCATE %I RESTART IDENTITY', t);
		END LOOP;
	END $$;`); err != nil {
		t.Fatalf("reset pg: %v", err)
	}
}

func makeCall(i int) model.RedactedCall {
	id := fmt.Sprintf("0191e8c4-0000-7000-8000-%012d", i)
	return model.RedactedCall{
		SchemaVersion: model.SchemaVersion,
		ID:            id,
		CapturedAt:    "2026-08-18T08:00:00.000Z",
		Integration:   "acme-payments",
		Direction:     "client",
		Method:        "POST",
		URL:           "https://api.acme.test/v1/charges",
		Route:         "/v1/charges",
		StatusCode:    200,
		RequestBody:   `{"amount":1200,"currency":"usd"}`,
		ResponseBody:  `{"id":"ch_1","amount":"1200"}`,
		Correlation:   model.Correlation{RequestID: fmt.Sprintf("req_%d", i)},
		Redaction:     model.Redaction{Applied: true, Patterns: []string{}},
	}
}

func makeEdgeCall(i int, peerHost, direction, class string) model.RedactedCall {
	c := makeCall(i)
	c.PeerHost = peerHost
	c.Direction = direction
	c.EdgeClass = class
	return c
}

// TestInsertCall_IdempotentReplay: re-inserting the same call id is a no-op —
// one stored row, and the edge's call_count must not double-count the replay.
func TestInsertCall_IdempotentReplay(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		c := makeEdgeCall(1, "api.acme.test", "client", "external")
		for i := 0; i < 3; i++ {
			if err := s.InsertCall(c); err != nil {
				t.Fatalf("insert call (attempt %d): %v", i+1, err)
			}
		}
		calls, _, err := s.Counts()
		if err != nil {
			t.Fatalf("counts: %v", err)
		}
		if calls != 1 {
			t.Fatalf("stored calls = %d, want 1", calls)
		}
		edges, err := s.ListEdges(true)
		if err != nil {
			t.Fatalf("list edges: %v", err)
		}
		if len(edges) != 1 {
			t.Fatalf("edges = %d, want 1", len(edges))
		}
		if edges[0].CallCount != 1 {
			t.Fatalf("edge call_count = %d, want 1 (replays must not double-count)", edges[0].CallCount)
		}
	})
}

// driftFinding builds a live-vs-spec finding on one endpoint. Each carries the
// SAME signature (the drift is per-endpoint) but a distinct id + source call so
// dedup can be observed.
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
		DetectedAt:    "2026-08-18T08:00:01.000Z",
	}
	f.Signature = f.ComputeSignature()
	return f
}

// TestEdgeDiscovery proves edges are auto-discovered from client + server OTLP
// records — keyed by (peer_host, direction), role/orientation derived from
// direction, class carried through — with internal edges excluded from the
// external listing (GET /api/edges).
func TestEdgeDiscovery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)

		// Outbound: this org is the consumer calling an external provider (2 calls).
		if err := s.InsertCall(makeEdgeCall(1, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert client call 1: %v", err)
		}
		if err := s.InsertCall(makeEdgeCall(2, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert client call 2: %v", err)
		}
		// Inbound: this org is the provider serving an external consumer (1 call).
		if err := s.InsertCall(makeEdgeCall(3, "partner.acme.test", "server", "external")); err != nil {
			t.Fatalf("insert server call: %v", err)
		}
		// Internal same-team edge — must be classified out of the external listing.
		if err := s.InsertCall(makeEdgeCall(4, "billing.svc.cluster.local", "client", "internal")); err != nil {
			t.Fatalf("insert internal call: %v", err)
		}

		all, err := s.ListEdges(false)
		if err != nil {
			t.Fatalf("list all edges: %v", err)
		}
		if len(all) != 3 {
			t.Fatalf("discovered %d edges total, want 3 (2 external + 1 internal)", len(all))
		}

		ext, err := s.ListEdges(true)
		if err != nil {
			t.Fatalf("list external edges: %v", err)
		}
		if len(ext) != 2 {
			t.Fatalf("external edges = %d, want 2 (internal excluded)", len(ext))
		}

		byKey := map[string]model.Edge{}
		for _, e := range ext {
			byKey[e.PeerHost+"/"+e.Direction] = e
		}
		out, ok := byKey["api.acme.test/client"]
		if !ok {
			t.Fatalf("missing outbound edge api.acme.test/client; got %+v", ext)
		}
		if out.Role != "consumer" {
			t.Errorf("outbound role = %q, want consumer", out.Role)
		}
		if out.Class != "external" {
			t.Errorf("outbound class = %q, want external", out.Class)
		}
		if out.CallCount != 2 {
			t.Errorf("outbound call_count = %d, want 2", out.CallCount)
		}
		in, ok := byKey["partner.acme.test/server"]
		if !ok {
			t.Fatalf("missing inbound edge partner.acme.test/server; got %+v", ext)
		}
		if in.Role != "provider" {
			t.Errorf("inbound role = %q, want provider", in.Role)
		}
		if in.CallCount != 1 {
			t.Errorf("inbound call_count = %d, want 1", in.CallCount)
		}

		// A drift finding on the outbound edge's call bumps that edge's drift_count.
		if err := s.InsertFinding(driftFinding("0191e8c4-ffff-7000-8000-00000000aa01", makeCall(1).ID)); err != nil {
			t.Fatalf("insert finding: %v", err)
		}
		ext, _ = s.ListEdges(true)
		for _, e := range ext {
			if e.PeerHost == "api.acme.test" && e.DriftCount != 1 {
				t.Errorf("outbound drift_count = %d, want 1", e.DriftCount)
			}
			if e.PeerHost == "partner.acme.test" && e.DriftCount != 0 {
				t.Errorf("inbound drift_count = %d, want 0", e.DriftCount)
			}
		}
	})
}

// TestDriftDedup is the acceptance oracle for per-endpoint dedup: N drifting
// calls on ONE endpoint collapse into exactly ONE finding with occurrence_count=N
// (CONTRACTS §4). The stored finding id stays stable (flag idempotency).
func TestDriftDedup(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		const N = 2000

		firstID := ""
		for i := 0; i < N; i++ {
			call := makeEdgeCall(i, "api.acme.test", "client", "external")
			if err := s.InsertCall(call); err != nil {
				t.Fatalf("insert call %d: %v", i, err)
			}
			fid := fmt.Sprintf("0191e8c4-dddd-7000-8000-%012d", i)
			if i == 0 {
				firstID = fid
			}
			if err := s.InsertFinding(driftFinding(fid, call.ID)); err != nil {
				t.Fatalf("insert finding %d: %v", i, err)
			}
		}

		findings, err := s.ListFindings(100)
		if err != nil {
			t.Fatalf("list findings: %v", err)
		}
		if len(findings) != 1 {
			t.Fatalf("dedup failed: %d findings, want exactly 1 for one endpoint", len(findings))
		}
		f := findings[0]
		if f.OccurrenceCount != N {
			t.Errorf("occurrence_count = %d, want %d", f.OccurrenceCount, N)
		}
		if f.ID != firstID {
			t.Errorf("finding id = %q, want the FIRST call's finding id %q (stable across re-drift)", f.ID, firstID)
		}
		_, findingCount, err := s.Counts()
		if err != nil {
			t.Fatalf("counts: %v", err)
		}
		if findingCount != 1 {
			t.Errorf("findings table holds %d rows, want 1", findingCount)
		}
	})
}

// definitionChangeFinding builds an MCP DESCRIPTION definition_change on one
// tool + field. Successive changes to the SAME field share the signature by
// design (integration|endpoint|kind|rule|field_path) — only the evidence (the
// before/after fragments, the two snapshot hashes and the observed-at times)
// moves. That is exactly why the stored doc cannot stay frozen for this kind.
func definitionChangeFinding(id, from, to, observedAt, detectedAt, actual string) model.Finding {
	f := model.Finding{
		SchemaVersion:        model.SchemaVersion,
		ID:                   id,
		Kind:                 model.KindDefinitionChange,
		Severity:             model.SeverityWarning,
		Integration:          "acme-tools",
		Endpoint:             "create_refund",
		FieldPath:            model.Ptr("description"),
		Expected:             `"Refund a payment."`,
		Actual:               actual,
		Rule:                 model.RuleDescriptionChanged,
		SpecVersionFrom:      model.Ptr(from),
		SpecVersionTo:        model.Ptr(to),
		DetectedAt:           detectedAt,
		Detail:               "Definition change (DESCRIPTION): description-changed on `create_refund` at description.",
		SnapshotObservedAt:   observedAt,
		SnapshotObservedFrom: "2026-08-26T08:00:00.000Z",
	}
	f.Signature = f.ComputeSignature()
	return f
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestDefinitionChangeRefreshesEvidence is the regression oracle for
// ux-design-v2 §2.8, run against the REAL store (a fake that keys findings by
// id instead of signature cannot see this bug at all).
//
// A second description change on the same tool and field dedups onto the row
// the first one created. If the doc stayed frozen there, spec_version_to would
// never advance — so an acknowledgement keyed on the evidence version could
// never stop matching, and the second change would render silently
// PRE-acknowledged; a flag would also disclose the FIRST change's two
// definitions and timestamps to the provider as their own published text.
//
// So: one row, the SAME finding id (flag_<id> is the flag idempotency key), the
// LATEST evidence. A replay of the same change refreshes nothing, and an
// occurrence-counted kind keeps its frozen doc.
func TestDefinitionChangeRefreshesEvidence(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)

		first := definitionChangeFinding("f_one", "sha256:aaa", "sha256:bbb",
			"2026-08-26T09:00:00.000Z", "2026-08-26T09:00:01.000Z", `"Refund a captured payment."`)
		if err := s.InsertFinding(first); err != nil {
			t.Fatalf("insert first definition change: %v", err)
		}
		second := definitionChangeFinding("f_two", "sha256:bbb", "sha256:ccc",
			"2026-08-26T10:00:00.000Z", "2026-08-26T10:00:01.000Z", `"Refund a captured payment, in full or in part."`)
		if err := s.InsertFinding(second); err != nil {
			t.Fatalf("insert second definition change: %v", err)
		}

		fs, err := s.ListFindings(100)
		if err != nil {
			t.Fatalf("list findings: %v", err)
		}
		if len(fs) != 1 {
			t.Fatalf("findings = %d, want exactly 1 (the signature is stable across successive changes)", len(fs))
		}
		f := fs[0]
		if f.ID != "f_one" {
			t.Errorf("finding id = %q, want the FIRST occurrence's id (the flag idempotency key must stay stable)", f.ID)
		}
		if f.SpecVersionTo == nil || *f.SpecVersionTo != "sha256:ccc" {
			t.Fatalf("spec_version_to = %q, want sha256:ccc — a frozen after-hash means the evidence-version ack key can NEVER fire and the second change arrives silently pre-acknowledged", derefStr(f.SpecVersionTo))
		}
		if f.SpecVersionFrom == nil || *f.SpecVersionFrom != "sha256:bbb" {
			t.Errorf("spec_version_from = %q, want sha256:bbb", derefStr(f.SpecVersionFrom))
		}
		if f.Actual != second.Actual {
			t.Errorf("actual = %q, want the SECOND change's published text — a flag would otherwise disclose the wrong text to the provider", f.Actual)
		}
		if f.SnapshotObservedAt != second.SnapshotObservedAt {
			t.Errorf("snapshot_observed_at = %q, want %q (the flag's evidence line and prefilled date read this)", f.SnapshotObservedAt, second.SnapshotObservedAt)
		}
		if f.FirstSeen != first.DetectedAt {
			t.Errorf("first_seen = %q, want the FIRST occurrence's %q", f.FirstSeen, first.DetectedAt)
		}
		if f.OccurrenceCount != 2 {
			t.Errorf("occurrence_count = %d, want 2", f.OccurrenceCount)
		}

		// A REPLAY of the same change (same after-hash — e.g. a restart re-seeding
		// from the stored snapshots) is not new evidence: count only, doc untouched.
		replay := definitionChangeFinding("f_three", "sha256:bbb", "sha256:ccc",
			"2026-08-26T10:00:00.000Z", "2026-08-26T11:00:00.000Z", `"tampered"`)
		if err := s.InsertFinding(replay); err != nil {
			t.Fatalf("insert replay: %v", err)
		}
		fs, err = s.ListFindings(100)
		if err != nil {
			t.Fatalf("list findings after replay: %v", err)
		}
		if len(fs) != 1 || fs[0].ID != "f_one" || fs[0].Actual != second.Actual {
			t.Fatalf("replay rewrote the doc: id=%q actual=%q (want f_one / the second change's text)", fs[0].ID, fs[0].Actual)
		}
		if fs[0].OccurrenceCount != 3 {
			t.Errorf("occurrence_count after replay = %d, want 3", fs[0].OccurrenceCount)
		}

		// An OLDER transition arriving late (a front collector replaying, a
		// re-delivered batch) must never revert the row to stale evidence —
		// that would re-open the silent-pre-ack hole from the other direction.
		stale := definitionChangeFinding("f_four", "sha256:aaa", "sha256:bbb",
			"2026-08-26T09:00:00.000Z", "2026-08-26T12:00:00.000Z", `"Refund a captured payment."`)
		if err := s.InsertFinding(stale); err != nil {
			t.Fatalf("insert stale replay: %v", err)
		}
		fs, err = s.ListFindings(100)
		if err != nil {
			t.Fatalf("list findings after stale replay: %v", err)
		}
		if fs[0].SpecVersionTo == nil || *fs[0].SpecVersionTo != "sha256:ccc" {
			t.Errorf("a LATE, OLDER change reverted the evidence to %q — the row must keep the newest", derefStr(fs[0].SpecVersionTo))
		}
		if fs[0].Actual != second.Actual {
			t.Errorf("a LATE, OLDER change reverted the published text to %q", fs[0].Actual)
		}

		// Occurrence-counted kinds are NOT refreshed: recurrence there is expected
		// and is surfaced as a count, so the doc stays the first occurrence's.
		d1 := driftFinding("d_one", "call-1")
		if err := s.InsertFinding(d1); err != nil {
			t.Fatalf("insert drift finding: %v", err)
		}
		d2 := driftFinding("d_two", "call-2")
		d2.Actual = `type=boolean (true)`
		if err := s.InsertFinding(d2); err != nil {
			t.Fatalf("insert repeat drift finding: %v", err)
		}
		fs, err = s.ListFindings(100)
		if err != nil {
			t.Fatalf("list findings after drift: %v", err)
		}
		var live *model.Finding
		for i := range fs {
			if fs[i].Kind == model.KindLiveVsSpec {
				live = &fs[i]
			}
		}
		if live == nil {
			t.Fatal("live-vs-spec finding missing")
		}
		if live.ID != "d_one" || live.Actual != d1.Actual {
			t.Errorf("occurrence-counted doc was rewritten: id=%q actual=%q, want d_one / %q", live.ID, live.Actual, d1.Actual)
		}
	})
}

// TestRingBuffer_StableFill_RowCap fills far past the row cap and asserts the
// window holds at the cap (oldest evicted first, FIFO).
func TestRingBuffer_StableFill_RowCap(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		const cap = 10
		s := b.open(t, cap, 0)
		for i := 0; i < 100; i++ {
			if err := s.InsertCall(makeCall(i)); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		rows, _, err := s.Stats()
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if rows != cap {
			t.Fatalf("row count = %d, want stable fill at %d", rows, cap)
		}
		// FIFO: the survivors must be the newest `cap` ids (90..99).
		calls, err := s.ListCalls(cap)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(calls) != cap {
			t.Fatalf("listed %d calls, want %d", len(calls), cap)
		}
		newest := makeCall(99).ID
		if calls[0].ID != newest {
			t.Errorf("newest survivor = %s, want %s", calls[0].ID, newest)
		}
		if _, ok, _ := s.GetCall(makeCall(0).ID); ok {
			t.Errorf("oldest call (0) should have been evicted")
		}
	})
}

// TestRingBuffer_ByteCap evicts on the byte ceiling as well as the row ceiling.
func TestRingBuffer_ByteCap(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		// Each row's doc is a few hundred bytes; a 2KB cap keeps only a handful.
		s := b.open(t, 0, 2048)
		for i := 0; i < 50; i++ {
			if err := s.InsertCall(makeCall(i)); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		rows, bytes, err := s.Stats()
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if bytes > 2048 {
			t.Fatalf("byte size = %d, exceeds cap 2048", bytes)
		}
		if rows == 0 || rows >= 50 {
			t.Fatalf("expected a bounded but non-empty window, got %d rows", rows)
		}
	})
}

// TestRingBuffer_PinnedSurvive is the evidence-preservation invariant: a pinned
// call (pin-on-finding) is never evicted, even when the window is hammered far
// past its caps by unpinned traffic.
func TestRingBuffer_PinnedSurvive(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		const cap = 5
		s := b.open(t, cap, 0)

		// Insert the call that a finding will reference.
		pinned := makeCall(1000)
		if err := s.InsertCall(pinned); err != nil {
			t.Fatalf("insert pinned call: %v", err)
		}
		// A finding on it pins it.
		src := pinned.ID
		if err := s.InsertFinding(model.Finding{
			SchemaVersion: model.SchemaVersion,
			ID:            "0191e8c4-ffff-7000-8000-000000000001",
			Kind:          model.KindLiveVsSpec,
			Severity:      model.SeverityBreaking,
			Integration:   "acme-payments",
			Endpoint:      "POST /v1/charges",
			Expected:      "type=integer",
			Actual:        `type=string ("1200")`,
			Rule:          "type-mismatch",
			SourceCallID:  &src,
			DetectedAt:    "2026-08-18T08:00:01.000Z",
		}); err != nil {
			t.Fatalf("insert finding: %v", err)
		}

		// Flood with unpinned traffic well past the cap.
		for i := 0; i < 100; i++ {
			if err := s.InsertCall(makeCall(i)); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}

		if _, ok, _ := s.GetCall(pinned.ID); !ok {
			t.Fatalf("pinned call was evicted — evidence lost")
		}
		// The row cap is a hard total ceiling and pinned rows count toward it, so the
		// window holds at cap: 1 pinned survivor + (cap-1) newest unpinned.
		rows, _, err := s.Stats()
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if rows != cap {
			t.Errorf("row count = %d, want stable fill at %d with the pinned row retained", rows, cap)
		}

		// evict-after-promote: unpin + stamp promoted_at, then the once-pinned call
		// re-enters the pool and evicts on the next insert.
		if err := s.MarkPromoted(pinned.ID); err != nil {
			t.Fatalf("mark promoted: %v", err)
		}
		for i := 100; i < 110; i++ {
			if err := s.InsertCall(makeCall(i)); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		if _, ok, _ := s.GetCall(pinned.ID); ok {
			t.Errorf("promoted call should re-enter the eviction pool and evict")
		}
		rows, _, _ = s.Stats()
		if rows != cap {
			t.Errorf("post-promote row count = %d, want %d", rows, cap)
		}
	})
}

// TestPersistence_SurvivesReopen proves data survives a collector restart: close
// the store and reopen the same file / reconnect to the same database.
func TestPersistence_SurvivesReopen(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		c := makeCall(7)
		if err := s.InsertCall(c); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		s2 := b.reopen(t, 0, 0)
		got, ok, err := s2.GetCall(c.ID)
		if err != nil || !ok {
			t.Fatalf("call did not survive reopen (ok=%v err=%v)", ok, err)
		}
		if got.Correlation.RequestID != c.Correlation.RequestID {
			t.Errorf("reopened call corrupted: %q != %q", got.Correlation.RequestID, c.Correlation.RequestID)
		}
	})
}

// TestEdgeCallCountsSince proves the observed-RPM source query: only calls at or
// after the threshold count, grouped per (peer_host, direction).
func TestEdgeCallCountsSince(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		old := makeEdgeCall(1, "api.acme.test", "client", "external")
		old.CapturedAt = "2026-08-18T07:00:00.000Z"
		recentOut := makeEdgeCall(2, "api.acme.test", "client", "external")
		recentOut.CapturedAt = "2026-08-18T08:00:30.000Z"
		recentIn := makeEdgeCall(3, "api.consumer-a.test", "server", "external")
		recentIn.CapturedAt = "2026-08-18T08:00:45.000Z"
		for _, c := range []model.RedactedCall{old, recentOut, recentIn} {
			if err := s.InsertCall(c); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}

		counts, err := s.EdgeCallCountsSince("2026-08-18T08:00:00Z")
		if err != nil {
			t.Fatalf("counts: %v", err)
		}
		if got := counts["api.acme.test|client"]; got != 1 {
			t.Errorf("outbound count: got %d want 1 (old call must not count)", got)
		}
		if got := counts["api.consumer-a.test|server"]; got != 1 {
			t.Errorf("inbound count: got %d want 1", got)
		}
	})
}

// TestSpecInfo_RoundTrip proves the provider-contract record the drift processor
// writes at Start: upsert by integration, list metadata, fetch the raw doc.
func TestSpecInfo_RoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		info := model.SpecInfo{
			Integration: "acme-payments",
			PeerHost:    "api.acme.test",
			Format:      "openapi",
			Title:       "Acme Payments API",
			Version:     "1.4.0",
			DocsURL:     "https://docs.acme.test/api",
			Endpoints:   3,
			LoadedAt:    "2026-08-18T08:00:00Z",
		}
		raw := []byte("openapi: 3.0.3\ninfo:\n  title: Acme Payments API\n")
		if err := s.PutSpecInfo(info, raw); err != nil {
			t.Fatalf("put: %v", err)
		}
		// Upsert: a reload replaces, never duplicates.
		info.Version = "1.5.0"
		if err := s.PutSpecInfo(info, raw); err != nil {
			t.Fatalf("re-put: %v", err)
		}
		// A self contract (we-as-provider) lists alongside, self first.
		if err := s.PutSpecInfo(model.SpecInfo{
			Integration: "self", Role: model.SpecRoleSelf, Format: "openapi",
			Title: "Org API", Version: "0.9.0", LoadedAt: "2026-08-18T08:00:00Z",
		}, []byte("openapi: 3.0.3\ninfo:\n  title: Org API\n")); err != nil {
			t.Fatalf("put self: %v", err)
		}

		infos, err := s.ListSpecInfos()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(infos) != 2 {
			t.Fatalf("want 2 spec infos, got %d", len(infos))
		}
		if infos[0].Role != model.SpecRoleSelf || infos[0].Title != "Org API" {
			t.Errorf("self contract must list first: %+v", infos[0])
		}
		if infos[1].Version != "1.5.0" || infos[1].Title != "Acme Payments API" || infos[1].PeerHost != "api.acme.test" {
			t.Errorf("listed info corrupted: %+v", infos[1])
		}
		// Role defaults to provider when the writer omitted it.
		if infos[1].Role != model.SpecRoleProvider {
			t.Errorf("role default: got %q want provider", infos[1].Role)
		}

		doc, format, ok, err := s.GetSpecDoc("acme-payments")
		if err != nil || !ok {
			t.Fatalf("get doc (ok=%v err=%v)", ok, err)
		}
		if format != "openapi" || string(doc) != string(raw) {
			t.Errorf("doc round-trip corrupted: format=%q", format)
		}

		if _, _, ok, _ := s.GetSpecDoc("unknown"); ok {
			t.Error("unknown integration must not resolve a spec doc")
		}
	})
}

// edgeByKey returns the discovered edge for peer_host|direction (external only
// are listed), failing the test if it is absent.
func edgeByKey(t *testing.T, s Store, peerHost, direction string) model.Edge {
	t.Helper()
	edges, err := s.ListEdges(true)
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	for _, e := range edges {
		if e.PeerHost == peerHost && e.Direction == direction {
			return e
		}
	}
	t.Fatalf("edge %s|%s not discovered (have %d edges)", peerHost, direction, len(edges))
	return model.Edge{}
}

// TestLatePin_FindingBeforeCall: a finding can reach the store BEFORE the call
// it references (front→store hop reordering, a partially applied batch that is
// re-delivered, a call re-sent after eviction). The store must be
// order-independent for call/finding pairs: when the call lands it is pinned
// (so it survives the rolling window) and the edge drift attribution that
// InsertFinding could not perform is repaired — exactly once, and without
// disturbing dedup counts, call_count idempotency, or evict-after-promote.
func TestLatePin_FindingBeforeCall(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		const cap = 5
		s := b.open(t, cap, 0)

		call := makeEdgeCall(1000, "api.acme.test", "client", "external")

		// 1. The finding arrives first — its source call is not in the store yet.
		if err := s.InsertFinding(driftFinding("0191e8c4-ffff-7000-8000-000000000001", call.ID)); err != nil {
			t.Fatalf("insert finding (before call): %v", err)
		}
		// 2. Then the call lands.
		if err := s.InsertCall(call); err != nil {
			t.Fatalf("insert late call: %v", err)
		}
		// 3. Flood with unpinned traffic well past the cap.
		for i := 0; i < 100; i++ {
			if err := s.InsertCall(makeCall(i)); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		if _, ok, _ := s.GetCall(call.ID); !ok {
			t.Fatalf("late-arriving evidence call was evicted — late pin failed")
		}
		if rows, _, _ := s.Stats(); rows != cap {
			t.Errorf("row count = %d, want stable fill at %d with the pinned row retained", rows, cap)
		}

		// Dedup state untouched: one finding, first occurrence.
		fs, err := s.ListFindings(10)
		if err != nil || len(fs) != 1 {
			t.Fatalf("findings = %d (%v), want 1", len(fs), err)
		}
		if fs[0].OccurrenceCount != 1 {
			t.Errorf("occurrence_count = %d, want 1", fs[0].OccurrenceCount)
		}
		// Edge attribution repaired: exactly one drift on the call's edge, and
		// call_count counts the call once.
		e := edgeByKey(t, s, "api.acme.test", "client")
		if e.CallCount != 1 || e.DriftCount != 1 {
			t.Errorf("edge after late pin: call_count=%d drift_count=%d, want 1/1", e.CallCount, e.DriftCount)
		}

		// 4. Idempotent replay of the same call: no double count, no double repair.
		if err := s.InsertCall(call); err != nil {
			t.Fatalf("replay call: %v", err)
		}
		e = edgeByKey(t, s, "api.acme.test", "client")
		if e.CallCount != 1 || e.DriftCount != 1 {
			t.Errorf("edge after replay: call_count=%d drift_count=%d, want 1/1", e.CallCount, e.DriftCount)
		}

		// 5. A repeat occurrence of the drift bumps the count only.
		if err := s.InsertFinding(driftFinding("0191e8c4-ffff-7000-8000-000000000002", call.ID)); err != nil {
			t.Fatalf("repeat finding: %v", err)
		}
		fs, _ = s.ListFindings(10)
		if len(fs) != 1 || fs[0].OccurrenceCount != 2 {
			t.Errorf("after repeat: findings=%d occurrence=%d, want 1/2", len(fs), fs[0].OccurrenceCount)
		}
		if e = edgeByKey(t, s, "api.acme.test", "client"); e.DriftCount != 1 {
			t.Errorf("drift_count after repeat = %d, want 1 (one per signature)", e.DriftCount)
		}

		// 6. evict-after-promote still applies to a late-pinned call.
		if err := s.MarkPromoted(call.ID); err != nil {
			t.Fatalf("mark promoted: %v", err)
		}
		for i := 100; i < 110; i++ {
			if err := s.InsertCall(makeCall(i)); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		if _, ok, _ := s.GetCall(call.ID); ok {
			t.Errorf("promoted call should re-enter the eviction pool and evict")
		}
	})
}

// TestSettings_RoundTrip: the per-deployment KV — absent → ok=false; put/get;
// overwrite wins; survives a reopen (restart); empty key rejected; values are
// opaque (JSON passes through untouched).
func TestSettings_RoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if _, ok, err := s.GetSetting("connect.collector_key"); err != nil || ok {
			t.Fatalf("absent key: ok=%v err=%v, want ok=false", ok, err)
		}
		if err := s.PutSetting("connect.collector_key", "ck_live_01"); err != nil {
			t.Fatalf("put: %v", err)
		}
		if v, ok, _ := s.GetSetting("connect.collector_key"); !ok || v != "ck_live_01" {
			t.Fatalf("get = %q ok=%v, want ck_live_01", v, ok)
		}
		if err := s.PutSetting("connect.collector_key", "ck_live_02"); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		if v, _, _ := s.GetSetting("connect.collector_key"); v != "ck_live_02" {
			t.Fatalf("after overwrite = %q, want ck_live_02", v)
		}
		js := `{"email":"ops@acme.test","confirmed_at":"2026-08-23T12:00:00Z"}`
		if err := s.PutSetting("connect.contact", js); err != nil {
			t.Fatalf("put json: %v", err)
		}
		if err := s.PutSetting("", "x"); err == nil {
			t.Errorf("empty key must be rejected")
		}
		// Restart.
		_ = s.Close()
		s2 := b.reopen(t, 0, 0)
		if v, ok, _ := s2.GetSetting("connect.contact"); !ok || v != js {
			t.Fatalf("after reopen: %q ok=%v", v, ok)
		}
		if v, _, _ := s2.GetSetting("connect.collector_key"); v != "ck_live_02" {
			t.Fatalf("after reopen key = %q", v)
		}
	})
}

// TestSettings_EdgeNameKeys (v1 phase 1 — edge naming): the rename KV shape —
// per-domain records `edge.name.<registrable_domain>` + the `edge.names` index
// array — persists across a reopen (the restart simulation; on the postgres
// backend the same reopen is a second pod sharing the deployment's database,
// so this is the cross-pod persistence oracle too). A cleared name is a
// tombstone (empty value), never a delete.
func TestSettings_EdgeNameKeys(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		rec := `{"name":"Guava Billing","source":"user","updated_at":"2026-08-30T10:00:00Z"}`
		if err := s.PutSetting("edge.name.zzguava.dev", rec); err != nil {
			t.Fatalf("put record: %v", err)
		}
		if err := s.PutSetting("edge.names", `["zzguava.dev"]`); err != nil {
			t.Fatalf("put index: %v", err)
		}
		// Restart / second pod.
		_ = s.Close()
		s2 := b.reopen(t, 0, 0)
		if v, ok, _ := s2.GetSetting("edge.name.zzguava.dev"); !ok || v != rec {
			t.Fatalf("record after reopen = %q ok=%v", v, ok)
		}
		if v, ok, _ := s2.GetSetting("edge.names"); !ok || v != `["zzguava.dev"]` {
			t.Fatalf("index after reopen = %q ok=%v", v, ok)
		}
		// Tombstone (the KV has no delete): the empty record persists as empty.
		if err := s2.PutSetting("edge.name.zzguava.dev", ""); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
		_ = s2.Close()
		s3 := b.reopen(t, 0, 0)
		if v, ok, _ := s3.GetSetting("edge.name.zzguava.dev"); !ok || v != "" {
			t.Fatalf("tombstone after reopen = %q ok=%v, want empty value present", v, ok)
		}
	})
}
