package store

import (
	"database/sql"
	"testing"

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

// TestOccurrenceLedger_FollowsEviction pins the ledger's bound: a call leaving
// the rolling window takes its occurrence rows with it, and a pinned
// representative keeps its own. Without this the ledger would grow one row per
// drifting call forever, while the window it protects stays capped.
func TestOccurrenceLedger_FollowsEviction(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 2, 0) // window: two rows

		c1 := makeEdgeCall(1, "api.acme.test", "client", "external")
		c2 := makeEdgeCall(2, "api.acme.test", "client", "external")
		for _, c := range []model.RedactedCall{c1, c2} {
			if err := s.InsertCall(c); err != nil {
				t.Fatalf("insert %s: %v", c.ID, err)
			}
		}
		// f_1 pins c1 (the representative); f_2 is a repeat on c2, which stays
		// unpinned and therefore evictable.
		for _, f := range []model.Finding{driftFinding("f_1", c1.ID), driftFinding("f_2", c2.ID)} {
			if err := s.InsertFinding(f); err != nil {
				t.Fatalf("insert %s: %v", f.ID, err)
			}
		}
		if n := ledgerRows(t, s); n != 2 {
			t.Fatalf("ledger rows = %d before eviction, want 2", n)
		}

		// A third call overflows the window: c2 (oldest unpinned) is evicted.
		if err := s.InsertCall(makeEdgeCall(3, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert call 3: %v", err)
		}
		if _, ok, _ := s.GetCall(c2.ID); ok {
			t.Fatalf("c2 should have been evicted (window of 2, c1 pinned)")
		}
		if _, ok, _ := s.GetCall(c1.ID); !ok {
			t.Fatalf("c1 is pinned and must survive")
		}
		if n := ledgerRows(t, s); n != 1 {
			t.Errorf("ledger rows = %d after evicting c2, want 1 (f_1 on the pinned c1)", n)
		}

		// The finding itself is untouched by the prune: still one row, two
		// occurrences, and the pinned representative's record is still a no-op
		// on re-delivery.
		if err := s.InsertFinding(driftFinding("f_1", c1.ID)); err != nil {
			t.Fatal(err)
		}
		assertFindings(t, s, 1, 2, "f_1")
	})
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
