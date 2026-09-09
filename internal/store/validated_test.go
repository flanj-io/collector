package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// The drift processor's per-call verdict (model.RedactedCall.Validated) through
// the store: it round-trips on both backends, its `drifted` companion is set on
// insert from the stamp, and a row from BEFORE the column existed reads back
// EMPTY — distinct from the explicit unknown a stamp-less record decodes to —
// so the UI can fall back to its edge-based guess for pre-migration rows and
// for nothing else.

func TestValidated_RoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()

		type row struct {
			verdict, reason string
			wantDrifted     bool
		}
		rows := []row{
			{model.ValidatedClean, "", false},
			{model.ValidatedDrifted, "", true},
			{model.ValidatedNot, model.NotValidatedNoContract, false},
			{model.ValidatedNot, model.NotValidatedNotRoutable, false},
			{model.ValidatedUnknown, "", false},
			// A test-made call with no verdict at all: stored as "", read as "".
			{"", "", false},
		}
		ids := make([]string, len(rows))
		for i, r := range rows {
			c := makeEdgeCall(3000+i, "api.acme.test", "client", "external")
			c.Validated = r.verdict
			c.ValidatedReason = r.reason
			ids[i] = c.ID
			if err := s.InsertCall(c); err != nil {
				t.Fatalf("insert %s: %v", c.ID, err)
			}
		}

		listed, err := s.ListCalls(50)
		if err != nil {
			t.Fatalf("list calls: %v", err)
		}
		byID := map[string]model.RedactedCall{}
		for _, c := range listed {
			byID[c.ID] = c
		}
		for i, r := range rows {
			got, ok := byID[ids[i]]
			if !ok {
				t.Fatalf("row %d (%q) missing from ListCalls", i, r.verdict)
			}
			if got.Validated != r.verdict || got.ValidatedReason != r.reason {
				t.Errorf("row %d: ListCalls validated=%q reason=%q, want %q / %q", i, got.Validated, got.ValidatedReason, r.verdict, r.reason)
			}
			if got.Drifted != r.wantDrifted {
				t.Errorf("row %d (%q): drifted=%v, want %v — the insert must read the stamp", i, r.verdict, got.Drifted, r.wantDrifted)
			}
			single, ok, err := s.GetCall(ids[i])
			if err != nil || !ok {
				t.Fatalf("GetCall(%s) ok=%v err=%v", ids[i], ok, err)
			}
			if single.Validated != r.verdict || single.ValidatedReason != r.reason || single.Drifted != r.wantDrifted {
				t.Errorf("row %d: GetCall validated=%q reason=%q drifted=%v, want %q / %q / %v",
					i, single.Validated, single.ValidatedReason, single.Drifted, r.verdict, r.reason, r.wantDrifted)
			}
		}

		// On the wire: an empty verdict is ABSENT, a set one is present.
		blank, _, _ := s.GetCall(ids[len(ids)-1])
		if doc, _ := json.Marshal(blank); strings.Contains(string(doc), `"validated"`) {
			t.Errorf("an empty verdict must be omitted from the JSON, got %s", doc)
		}
		stamped, _, _ := s.GetCall(ids[0])
		if doc, _ := json.Marshal(stamped); !strings.Contains(string(doc), `"validated":"clean"`) {
			t.Errorf("a clean verdict must be on the wire, got %s", doc)
		}
	})
}

// TestValidated_DriftedFromStampSurvivesTheFindingPath: the finding that names a
// stamped-drifted call arrives afterwards and marks it again; idempotent, and the
// occurrence path (a repeat signature) must not un-mark anything.
func TestValidated_DriftedFromStampSurvivesTheFindingPath(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()
		c := makeEdgeCall(3100, "api.acme.test", "client", "external")
		c.Validated = model.ValidatedDrifted
		if err := s.InsertCall(c); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := s.InsertFinding(driftFinding("0191e8c4-eeee-7000-8000-00000000310"+string(rune('0'+i)), c.ID)); err != nil {
				t.Fatalf("insert finding %d: %v", i, err)
			}
		}
		got, _, _ := s.GetCall(c.ID)
		if !got.Drifted || got.Validated != model.ValidatedDrifted {
			t.Errorf("after findings: drifted=%v validated=%q, want true / drifted", got.Drifted, got.Validated)
		}
	})
}

// TestValidated_PreMigrationRowReadsEmpty: a row written by a collector from
// before the column existed. The migration adds the column defaulting to the
// empty string, so the row reads back with NO verdict — and a row inserted
// after it on the same store carries its stamp, so the two are
// distinguishable on one screen.
func TestValidated_PreMigrationRowReadsEmpty(t *testing.T) {
	legacyDoc := func(id string) string {
		c := makeEdgeCall(0, "api.acme.test", "client", "external")
		c.ID = id
		b, _ := json.Marshal(c)
		return string(b)
	}

	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flanj.db")
		// The calls table as it shipped before `validated` (and before
		// `drifted`, which the migration also adds). Only this table is
		// pre-created; the migration creates the rest.
		raw, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`CREATE TABLE calls (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, captured_at TEXT NOT NULL,
  integration TEXT NOT NULL, peer_host TEXT, direction TEXT, edge_class TEXT, method TEXT NOT NULL,
  route TEXT NOT NULL, status_code INTEGER NOT NULL, request_id TEXT, idem_key TEXT, trace_id TEXT,
  byte_size INTEGER NOT NULL, pinned INTEGER NOT NULL DEFAULT 0, promoted_at TEXT, doc TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(
			`INSERT INTO calls (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, byte_size, doc)
			 VALUES ('legacy_1','2026-08-18T08:00:00.000Z','acme-payments','api.acme.test','client','external','POST','/v1/charges',200,10,?)`,
			legacyDoc("legacy_1")); err != nil {
			t.Fatal(err)
		}
		_ = raw.Close()

		s, err := OpenSQLite(path, 0, 0)
		if err != nil {
			t.Fatalf("open (migrate) legacy sqlite: %v", err)
		}
		defer s.Close()
		assertLegacyThenStamped(t, s)
	})

	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("FLANJ_TEST_PG_DSN")
		if dsn == "" {
			t.Skip("FLANJ_TEST_PG_DSN not set")
		}
		resetPG(t, dsn)
		first, err := OpenPostgres(dsn, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Roll the schema back to before the column, write a row the old way,
		// and reopen: the migration must re-add the column with its default.
		pg := first.(*postgresStore)
		if _, err := pg.db.Exec(`ALTER TABLE calls DROP COLUMN validated`); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.db.Exec(
			`INSERT INTO calls (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, byte_size, doc)
			 VALUES ('legacy_1','2026-08-18T08:00:00.000Z','acme-payments','api.acme.test','client','external','POST','/v1/charges',200,10,$1)`,
			legacyDoc("legacy_1")); err != nil {
			t.Fatal(err)
		}
		_ = first.Close()

		s, err := OpenPostgres(dsn, 0, 0)
		if err != nil {
			t.Fatalf("reopen (migrate) postgres: %v", err)
		}
		defer s.Close()
		assertLegacyThenStamped(t, s)
	})
}

func assertLegacyThenStamped(t *testing.T, s Store) {
	t.Helper()
	legacy, ok, err := s.GetCall("legacy_1")
	if err != nil || !ok {
		t.Fatalf("legacy row: ok=%v err=%v", ok, err)
	}
	if legacy.Validated != "" {
		t.Errorf("pre-migration row: validated=%q, want EMPTY (the UI's fallback case)", legacy.Validated)
	}
	fresh := makeEdgeCall(3200, "api.acme.test", "client", "external")
	fresh.Validated = model.ValidatedNot
	fresh.ValidatedReason = model.NotValidatedNoContract
	if err := s.InsertCall(fresh); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetCall(fresh.ID)
	if got.Validated != model.ValidatedNot || got.ValidatedReason != model.NotValidatedNoContract {
		t.Errorf("post-migration row: validated=%q reason=%q, want not-validated / no-contract", got.Validated, got.ValidatedReason)
	}
}

// TestMigrateFromSQLite_CarriesValidated: the one-shot sqlite→postgres import
// copies the verdict with the pinned evidence, and copes with a legacy file
// from before the column existed (an empty verdict, exactly as the default
// gives).
func TestMigrateFromSQLite_CarriesValidated(t *testing.T) {
	build := func(t *testing.T, dropColumn bool) (path string, pinnedID string) {
		t.Helper()
		path = filepath.Join(t.TempDir(), "flanj.db")
		s, err := OpenSQLite(path, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		pinned := makeEdgeCall(500, "api.acme.test", "client", "external")
		pinned.Validated = model.ValidatedDrifted
		if err := s.InsertCall(pinned); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertFinding(driftFinding("0191e8c4-eeee-7000-8000-000000000500", pinned.ID)); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		if dropColumn {
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`ALTER TABLE calls DROP COLUMN validated`); err != nil {
				t.Fatalf("drop column on the legacy file: %v", err)
			}
			_ = raw.Close()
		}
		return path, pinned.ID
	}

	t.Run("current file", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		path, id := build(t, false)
		if _, err := MigrateFromSQLite(pg, path); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		got, ok, _ := pg.GetCall(id)
		if !ok || got.Validated != model.ValidatedDrifted || !got.Drifted {
			t.Errorf("migrated call: ok=%v validated=%q drifted=%v, want drifted / true", ok, got.Validated, got.Drifted)
		}
	})
	t.Run("legacy file without the column", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		path, id := build(t, true)
		if _, err := MigrateFromSQLite(pg, path); err != nil {
			t.Fatalf("migrate a file predating `validated`: %v", err)
		}
		got, ok, _ := pg.GetCall(id)
		if !ok || got.Validated != "" {
			t.Errorf("migrated legacy call: ok=%v validated=%q, want present with an EMPTY verdict", ok, got.Validated)
		}
	})
}
