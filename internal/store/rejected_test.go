package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestWriteError_RejectionIsErrRejected pins the error classification the
// store exporter's retry rests on (2026-09-08 review of #46): a write the
// backend refuses for the RECORD — a constraint the statement's ON CONFLICT
// does not absorb — comes back wrapped in ErrRejected on both backends, so the
// exporter can drop it after one attempt instead of retrying a deterministic
// failure for max_elapsed_time; a write that fails for the STORE (here: a
// closed handle) does not, and stays retryable.
//
// The rejection driven here is a real one: a findings row whose occurrence-
// ledger entry is gone (evictOldest prunes the ledger with the source call)
// meets the same record id again under a NEW signature. The ledger lets it
// through, `ON CONFLICT (signature)` does not apply, and `findings.id UNIQUE`
// refuses it — sqlite SQLITE_CONSTRAINT, postgres 23505.
func TestWriteError_RejectionIsErrRejected(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)

		c1 := makeEdgeCall(1, "api.acme.test", "client", "external")
		if err := s.InsertCall(c1); err != nil {
			t.Fatalf("insert call: %v", err)
		}
		if err := s.InsertFinding(driftFinding("f_1", c1.ID)); err != nil {
			t.Fatalf("insert finding: %v", err)
		}
		// Forget the ledger row, as eviction does when c1 leaves the window.
		if _, err := rawDB(t, s).Exec(`DELETE FROM finding_occurrences`); err != nil {
			t.Fatalf("prune ledger: %v", err)
		}

		dup := driftFinding("f_1", c1.ID)
		dup.Endpoint = "POST /v1/refunds"
		dup.Signature = dup.ComputeSignature()
		err := s.InsertFinding(dup)
		if err == nil {
			t.Fatal("re-using a finding id under a new signature must be refused")
		}
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("a constraint violation must be ErrRejected, got: %v", err)
		}
		if !strings.Contains(err.Error(), "insert finding") {
			t.Errorf("the rejection must keep the backend detail for the log line, got: %v", err)
		}
		// Nothing half-applied: the ledger insert rolled back with the tx.
		if n := ledgerRows(t, s); n != 0 {
			t.Errorf("ledger rows = %d after a rejected insert, want 0", n)
		}
		assertFindings(t, s, 1, 1, "f_1")

		// A failure of the store itself is NOT a rejection.
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		err = s.InsertCall(makeEdgeCall(2, "api.acme.test", "client", "external"))
		if err == nil {
			t.Fatal("a write on a closed store must fail")
		}
		if errors.Is(err, ErrRejected) {
			t.Fatalf("a store failure must stay retryable, got ErrRejected: %v", err)
		}
	})
}

// rawDB reaches the backend's handle for test setup the Store surface does not
// (and must not) offer.
func rawDB(t *testing.T, s Store) *sql.DB {
	t.Helper()
	switch v := s.(type) {
	case *sqliteStore:
		return v.db
	case *postgresStore:
		return v.db
	default:
		t.Fatalf("unknown store type %T", s)
		return nil
	}
}
