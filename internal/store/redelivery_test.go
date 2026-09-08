package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/flanj-io/collector/internal/model"
)

// TestInsertFinding_IdempotentRedelivery is the regression for launch-week
// item 5: the store exporter now queues and RETRIES a batch whose write failed,
// and a front re-sends a batch whose ACK it never got — so the store receives
// the SAME finding record twice. Calls were already idempotent on their id; a
// finding's second arrival was indistinguishable from a repeat occurrence and
// bumped occurrence_count, counting one drifting call twice (and, on a
// partially applied batch, re-counting every finding before the record that
// failed). The occurrence ledger (recordOccurrence) closes that: the same
// record id applies once, on both backends.
func TestInsertFinding_IdempotentRedelivery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)

		c1 := makeEdgeCall(1, "api.acme.test", "client", "external")
		if err := s.InsertCall(c1); err != nil {
			t.Fatalf("insert call: %v", err)
		}
		f1 := driftFinding("f_1", c1.ID)
		// The first occurrence, delivered three times (a retried batch).
		for i := 0; i < 3; i++ {
			if err := s.InsertFinding(f1); err != nil {
				t.Fatalf("insert f_1 (delivery %d): %v", i+1, err)
			}
		}
		assertFindings(t, s, 1, 1, "f_1")
		assertEdgeDrift(t, s, 1)

		// A genuinely NEW occurrence (new id, new call) still counts…
		c2 := makeEdgeCall(2, "api.acme.test", "client", "external")
		if err := s.InsertCall(c2); err != nil {
			t.Fatalf("insert call 2: %v", err)
		}
		f2 := driftFinding("f_2", c2.ID)
		if err := s.InsertFinding(f2); err != nil {
			t.Fatalf("insert f_2: %v", err)
		}
		assertFindings(t, s, 1, 2, "f_1")

		// …and its own re-delivery does not, nor does an OLD record arriving
		// again out of order (cross-request reordering across the tiered hop).
		for _, f := range []model.Finding{f2, f1, f2} {
			if err := s.InsertFinding(f); err != nil {
				t.Fatalf("re-deliver %s: %v", f.ID, err)
			}
		}
		assertFindings(t, s, 1, 2, "f_1")
		// Repeats never re-attribute drift to the edge — unchanged.
		assertEdgeDrift(t, s, 1)

		// A repeat that is a NEW detection on an already-drifted call (fresh id:
		// the processor mints one per detection) is a real occurrence — this is
		// what keeps a restart's re-seeded replay counting (see
		// TestDefinitionChangeRefreshesEvidence).
		if err := s.InsertFinding(driftFinding("f_3", c2.ID)); err != nil {
			t.Fatalf("insert f_3: %v", err)
		}
		assertFindings(t, s, 1, 3, "f_1")

		// Per-call drift marks survive the no-op path (they were set on first
		// application and nothing un-sets them).
		calls, err := s.ListCalls(10)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range calls {
			if !c.Drifted {
				t.Errorf("call %s produced a finding but is not marked drifted", c.ID)
			}
		}

		// Call-less kinds ride the same ledger: one record, delivered twice.
		dc := definitionChangeFinding("dc_1", "sha256:aaa", "sha256:bbb",
			"2026-08-26T09:00:00.000Z", "2026-08-26T09:00:01.000Z", `"Refund a captured payment."`)
		for i := 0; i < 2; i++ {
			if err := s.InsertFinding(dc); err != nil {
				t.Fatalf("insert dc_1 (delivery %d): %v", i+1, err)
			}
		}
		fs, err := s.ListFindings(10)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if f.Kind == model.KindDefinitionChange && f.OccurrenceCount != 1 {
				t.Errorf("definition_change occurrence_count = %d after a re-delivery, want 1", f.OccurrenceCount)
			}
		}
	})
}

// TestOccurrenceLedger_RetryAfterEviction is the regression for the 2026-09-08
// review finding: the ledger used to be pruned WITH the call a finding named,
// so a re-delivery that outlived the rolling window found no ledger row and
// counted the finding again. The window rolls on TRAFFIC (seconds on a busy
// edge); a re-delivery horizon runs on the CLOCK (the exporter retries for 15
// minutes, a front for 5). Coupling the two was the bug.
//
// Here the repeat's source call is evicted while the exporter is still
// entitled to retry that very record. Under the old prune this bumped
// occurrence_count to 3.
func TestOccurrenceLedger_RetryAfterEviction(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 2, 0) // window: two rows

		c1 := makeEdgeCall(1, "api.acme.test", "client", "external")
		c2 := makeEdgeCall(2, "api.acme.test", "client", "external")
		for _, c := range []model.RedactedCall{c1, c2} {
			if err := s.InsertCall(c); err != nil {
				t.Fatalf("insert %s: %v", c.ID, err)
			}
		}
		// f_1 creates the finding and pins c1 (the representative); f_2 is a
		// REPEAT on the same signature, so c2 is never pinned — it is ordinary
		// evictable traffic, which is the whole point.
		for _, f := range []model.Finding{driftFinding("f_1", c1.ID), driftFinding("f_2", c2.ID)} {
			if err := s.InsertFinding(f); err != nil {
				t.Fatalf("insert %s: %v", f.ID, err)
			}
		}
		assertFindings(t, s, 1, 2, "f_1")
		if n := ledgerRows(t, s); n != 2 {
			t.Fatalf("ledger rows = %d before eviction, want 2", n)
		}

		// A third call overflows the window: c2 (oldest unpinned) goes.
		if err := s.InsertCall(makeEdgeCall(3, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert call 3: %v", err)
		}
		if _, ok, _ := s.GetCall(c2.ID); ok {
			t.Fatalf("c2 should have been evicted (window of 2, c1 pinned)")
		}
		if _, ok, _ := s.GetCall(c1.ID); !ok {
			t.Fatalf("c1 is pinned and must survive")
		}
		if n := ledgerRows(t, s); n != 2 {
			// Not fatal: the double-count below is the symptom that matters.
			t.Errorf("ledger rows = %d after evicting c2, want 2 — the ledger outlives the window it dedups for", n)
		}

		// THE REGRESSION: the exporter retries f_2 after the window rolled past
		// its source call. It must still be recognised as already applied.
		if err := s.InsertFinding(driftFinding("f_2", c2.ID)); err != nil {
			t.Fatalf("re-deliver f_2 after eviction: %v", err)
		}
		assertFindings(t, s, 1, 2, "f_1")
		assertEdgeDrift(t, s, 1)

		// And the pinned representative's own record is still a no-op too.
		if err := s.InsertFinding(driftFinding("f_1", c1.ID)); err != nil {
			t.Fatal(err)
		}
		assertFindings(t, s, 1, 2, "f_1")
	})
}

// TestOccurrenceLedger_PrunedByStoreClock pins the ledger's new bound: rows
// live occurrenceTTL on the STORE's clock and are then dropped, from BOTH
// paths that write the ledger. The finding path matters on its own — a
// deployment whose findings are all call-less (a flapping MCP snapshot
// producing definition_change after definition_change) never reaches the
// eviction pass at all, and that is exactly the shape whose rows used to
// accumulate forever, one per detection.
func TestOccurrenceLedger_PrunedByStoreClock(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)

		c1 := makeEdgeCall(1, "api.acme.test", "client", "external")
		if err := s.InsertCall(c1); err != nil {
			t.Fatalf("insert call: %v", err)
		}
		if err := s.InsertFinding(driftFinding("f_1", c1.ID)); err != nil {
			t.Fatalf("insert f_1: %v", err)
		}
		if err := s.InsertFinding(definitionChangeFinding("dc_1", "sha256:aaa", "sha256:bbb",
			"2026-08-26T09:00:00.000Z", "2026-08-26T09:00:01.000Z", `"Refund a captured payment."`)); err != nil {
			t.Fatalf("insert dc_1: %v", err)
		}
		if n := ledgerRows(t, s); n != 2 {
			t.Fatalf("ledger rows = %d, want 2 (one per record: a call-backed and a call-less finding)", n)
		}

		// Inside the horizon nothing is pruned, however much traffic runs.
		if err := s.InsertCall(makeEdgeCall(2, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert call 2: %v", err)
		}
		if n := ledgerRows(t, s); n != 2 {
			t.Fatalf("ledger rows = %d after more traffic, want 2 — a fresh row is still re-deliverable", n)
		}

		// Age every row past the TTL. seen_at is the store's own stamp, so
		// moving the rows is the only way a test reaches the far side of it.
		backdateLedger(t, s, occurrenceTTL+time.Minute)

		// The CALL path prunes: the eviction pass runs on every InsertCall.
		if err := s.InsertCall(makeEdgeCall(3, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert call 3: %v", err)
		}
		if n := ledgerRows(t, s); n != 0 {
			t.Errorf("ledger rows = %d after the TTL passed, want 0", n)
		}

		// The FINDING path prunes too — the only path a call-less deployment
		// ever takes. dc_2's own row is written first and stays; the aged one
		// it is measured against goes.
		if err := s.InsertFinding(definitionChangeFinding("dc_2", "sha256:bbb", "sha256:ccc",
			"2026-08-26T10:00:00.000Z", "2026-08-26T10:00:01.000Z", `"Refund a settled payment."`)); err != nil {
			t.Fatalf("insert dc_2: %v", err)
		}
		if n := ledgerRows(t, s); n != 1 {
			t.Fatalf("ledger rows = %d after dc_2, want 1", n)
		}
		backdateLedger(t, s, occurrenceTTL+time.Minute)
		if err := s.InsertFinding(definitionChangeFinding("dc_3", "sha256:ccc", "sha256:ddd",
			"2026-08-26T11:00:00.000Z", "2026-08-26T11:00:01.000Z", `"Refund a captured charge."`)); err != nil {
			t.Fatalf("insert dc_3: %v", err)
		}
		if n := ledgerRows(t, s); n != 1 {
			t.Errorf("ledger rows = %d, want 1 (dc_3's fresh row; dc_2's aged one pruned) — "+
				"call-less findings are what grew this table without bound", n)
		}
	})
}

// backdateLedger moves every ledger row `by` into the past, so a test can look
// at the far side of occurrenceTTL without waiting an hour. A literal stamp,
// not a placeholder: it is the store's own fixed-width format and the
// statement then runs unchanged on both drivers.
func backdateLedger(t *testing.T, s Store, by time.Duration) {
	t.Helper()
	stamp := isoTime(time.Now().Add(-by))
	if _, err := rawDB(t, s).Exec(`UPDATE finding_occurrences SET seen_at = '` + stamp + `'`); err != nil {
		t.Fatalf("backdate ledger: %v", err)
	}
}

// TestMigrate_UpgradesLedgerIndexes: an EXISTING database carries the
// pre-2026-09-08 ledger index (idx_finding_occurrences_call, which served the
// prune that rode on call eviction) and none on seen_at. Opening it must swap
// them. This is a boot test as much as an index one: the swap lives in the
// schema statement, and a DDL statement that errors there aborts migrate() —
// the pod does not start, and the store is the pod that holds the evidence.
func TestMigrate_UpgradesLedgerIndexes(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0) // creates the CURRENT schema
		// Put the database back into the OLD shape.
		db := rawDB(t, s)
		if _, err := db.Exec(`DROP INDEX IF EXISTS idx_finding_occurrences_seen`); err != nil {
			t.Fatalf("drop new index: %v", err)
		}
		if _, err := db.Exec(`CREATE INDEX idx_finding_occurrences_call ON finding_occurrences(source_call_id)`); err != nil {
			t.Fatalf("plant old index: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		s2 := b.reopen(t, 0, 0) // the upgrade
		got := indexNames(t, rawDB(t, s2), b.pgDSN != "")
		if got["idx_finding_occurrences_call"] {
			t.Errorf("the old ledger index survived the upgrade: %v", got)
		}
		if !got["idx_finding_occurrences_seen"] {
			t.Errorf("the TTL prune's index is missing after the upgrade: %v", got)
		}
		// …and the upgraded store still writes.
		c := makeEdgeCall(1, "api.acme.test", "client", "external")
		if err := s2.InsertCall(c); err != nil {
			t.Fatalf("insert after upgrade: %v", err)
		}
		if err := s2.InsertFinding(driftFinding("f_1", c.ID)); err != nil {
			t.Fatalf("insert finding after upgrade: %v", err)
		}
	})
}

// indexNames lists the indexes the backend actually holds.
func indexNames(t *testing.T, db *sql.DB, pg bool) map[string]bool {
	t.Helper()
	q := `SELECT name FROM sqlite_master WHERE type='index'`
	if pg {
		q = `SELECT indexname FROM pg_indexes WHERE schemaname='public'`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	return out
}

// assertFindings: exactly `rows` findings, the first with `occurrences` and `id`.
func assertFindings(t *testing.T, s Store, rows, occurrences int, id string) {
	t.Helper()
	fs, err := s.ListFindings(10)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	var drift []model.Finding
	for _, f := range fs {
		if f.Kind == model.KindLiveVsSpec {
			drift = append(drift, f)
		}
	}
	if len(drift) != rows {
		t.Fatalf("live-vs-spec findings = %d, want %d", len(drift), rows)
	}
	if drift[0].OccurrenceCount != occurrences {
		t.Errorf("occurrence_count = %d, want %d", drift[0].OccurrenceCount, occurrences)
	}
	if drift[0].ID != id {
		t.Errorf("finding id = %q, want %q (the first occurrence's id is the flag key)", drift[0].ID, id)
	}
}

func assertEdgeDrift(t *testing.T, s Store, want int) {
	t.Helper()
	edges, err := s.ListEdges(true)
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].DriftCount != want {
		t.Errorf("edge drift_count = %d, want %d", edges[0].DriftCount, want)
	}
}

// ledgerRows reads the occurrence ledger directly — it has no Store surface on
// purpose (nothing outside this package should reason about it).
func ledgerRows(t *testing.T, s Store) int {
	t.Helper()
	var db *sql.DB
	switch v := s.(type) {
	case *sqliteStore:
		db = v.db
	case *postgresStore:
		db = v.db
	default:
		t.Fatalf("unknown store type %T", s)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finding_occurrences`).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	return n
}
