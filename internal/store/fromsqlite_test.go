package store

// One-shot sqlite → postgres migration tests. Postgres-gated like the rest of
// the postgres suite: skipped unless FLANJ_TEST_PG_DSN is set.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// buildLegacyStore writes a realistic sqlite store: unpinned traffic, one
// pinned call referenced by a deduped finding (occurrence 3), two edges.
// Returns the file path and the pinned call / expected finding ids.
func buildLegacyStore(t *testing.T) (path, pinnedID, findingID string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "flanj.db")
	s, err := OpenSQLite(path, 0, 0)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	defer s.Close()

	for i := 0; i < 5; i++ {
		if err := s.InsertCall(makeEdgeCall(i, "api.acme.test", "client", "external")); err != nil {
			t.Fatalf("insert unpinned call %d: %v", i, err)
		}
	}
	pinned := makeEdgeCall(100, "api.acme.test", "client", "external")
	if err := s.InsertCall(pinned); err != nil {
		t.Fatalf("insert pinned call: %v", err)
	}
	if err := s.InsertCall(makeEdgeCall(200, "partner.acme.test", "server", "external")); err != nil {
		t.Fatalf("insert inbound call: %v", err)
	}
	findingID = "0191e8c4-eeee-7000-8000-000000000001"
	for i := 0; i < 3; i++ {
		fid := fmt.Sprintf("0191e8c4-eeee-7000-8000-%012d", i+1)
		if err := s.InsertFinding(driftFinding(fid, pinned.ID)); err != nil {
			t.Fatalf("insert finding %d: %v", i, err)
		}
	}
	// Per-deployment settings (e.g. the Connect key) must cross over too.
	if err := s.PutSetting("connect.collector_key", "ck_legacy_01"); err != nil {
		t.Fatalf("put legacy setting: %v", err)
	}
	return path, pinned.ID, findingID
}

func TestMigrateFromSQLite_CopiesDurableEvidence(t *testing.T) {
	pods := pgPods(t, 1, 0, 0)
	pg := pods[0]
	path, pinnedID, findingID := buildLegacyStore(t)

	sum, err := MigrateFromSQLite(pg, path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !sum.Ran {
		t.Fatalf("migration did not run despite the legacy file existing")
	}
	if sum.PinnedCalls != 1 || sum.Findings != 1 || sum.Edges != 2 || sum.Settings != 1 {
		t.Errorf("summary = %+v, want 1 pinned call, 1 finding, 2 edges, 1 setting", sum)
	}
	if v, ok, _ := pg.GetSetting("connect.collector_key"); !ok || v != "ck_legacy_01" {
		t.Errorf("setting after migration = %q ok=%v, want ck_legacy_01", v, ok)
	}

	// Pinned evidence crossed over; unpinned window traffic did not.
	if _, ok, _ := pg.GetCall(pinnedID); !ok {
		t.Errorf("pinned call missing after migration")
	}
	if _, ok, _ := pg.GetCall(makeCall(0).ID); ok {
		t.Errorf("unpinned call was migrated — only pinned evidence should cross")
	}
	calls, findings, err := pg.Counts()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if calls != 1 || findings != 1 {
		t.Errorf("counts = (%d calls, %d findings), want (1, 1)", calls, findings)
	}

	// Finding id + dedup state survive (flag idempotency key = flag_<id>).
	f, ok, err := pg.GetFinding(findingID)
	if err != nil || !ok {
		t.Fatalf("migrated finding missing (ok=%v err=%v)", ok, err)
	}
	if f.OccurrenceCount != 3 {
		t.Errorf("occurrence_count = %d, want 3", f.OccurrenceCount)
	}

	// Edge discovery history survives with its counters.
	edges, err := pg.ListEdges(true)
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(edges))
	}
	for _, e := range edges {
		if e.PeerHost == "api.acme.test" && e.CallCount != 6 {
			t.Errorf("outbound call_count = %d, want 6 (5 unpinned + 1 pinned)", e.CallCount)
		}
		if e.PeerHost == "api.acme.test" && e.DriftCount != 1 {
			t.Errorf("outbound drift_count = %d, want 1", e.DriftCount)
		}
	}

	// The source file is tombstoned so the import never re-runs.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("legacy file still present — should be renamed")
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Errorf("tombstone %s.migrated missing: %v", path, err)
	}

	// Second run: steady state, no-op, nothing duplicated.
	sum2, err := MigrateFromSQLite(pg, path)
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if sum2.Ran {
		t.Errorf("second migration ran — the tombstone rename must make it a no-op")
	}
	calls2, findings2, _ := pg.Counts()
	if calls2 != calls || findings2 != findings {
		t.Errorf("counts changed on re-run: (%d,%d) -> (%d,%d)", calls, findings, calls2, findings2)
	}
}

// TestMigrateFromSQLite_ConflictsAreKept: rows already living in postgres win —
// the import must not overwrite or duplicate them (ON CONFLICT DO NOTHING).
func TestMigrateFromSQLite_ConflictsAreKept(t *testing.T) {
	pods := pgPods(t, 1, 0, 0)
	pg := pods[0]

	// Postgres already holds the same drift signature with its own id and count.
	call := makeEdgeCall(500, "api.acme.test", "client", "external")
	if err := pg.InsertCall(call); err != nil {
		t.Fatalf("seed pg call: %v", err)
	}
	existing := driftFinding("0191e8c4-aaaa-7000-8000-000000000001", call.ID)
	if err := pg.InsertFinding(existing); err != nil {
		t.Fatalf("seed pg finding: %v", err)
	}

	path, _, _ := buildLegacyStore(t)
	sum, err := MigrateFromSQLite(pg, path)
	if err != nil {
		t.Fatalf("migrate over populated pg: %v", err)
	}
	if sum.Findings != 0 {
		t.Errorf("conflicting finding was counted as copied: %+v", sum)
	}

	findings, err := pg.ListFindings(10)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1 (no duplicate for the shared signature)", len(findings))
	}
	if findings[0].ID != existing.ID {
		t.Errorf("finding id = %q, want the pre-existing %q (postgres row wins)", findings[0].ID, existing.ID)
	}
}

func TestMigrateFromSQLite_MissingFileIsSteadyState(t *testing.T) {
	pods := pgPods(t, 1, 0, 0)
	sum, err := MigrateFromSQLite(pods[0], filepath.Join(t.TempDir(), "never-existed.db"))
	if err != nil {
		t.Fatalf("missing file must be a clean no-op, got: %v", err)
	}
	if sum.Ran {
		t.Errorf("migration claims to have run on a missing file")
	}
}

// TestMigrateFromSQLite_CarriesUploadedContracts: switching backend
// sqlite→postgres must not destroy the operator's uploaded provider contracts.
//
// BUG (cross-repo review, 2026-09-01): MigrateFromSQLite skipped spec_infos,
// justified by "the drift processor re-records loaded contracts at every
// Start". That was true while provider contracts came from
// `flanjdrift.spec_path` — this slice DELETED that key, so an uploaded
// contract now exists ONLY as a spec_infos row. The migration then renamed the
// sqlite file to `<path>.migrated`, so it destroyed the only copy and
// tombstoned the source in one step. Every provider would have silently gone
// back to "captured, not validated" on a backend switch.
func TestMigrateFromSQLite_CarriesUploadedContracts(t *testing.T) {
	pods := pgPods(t, 1, 0, 0)
	pg := pods[0]

	path := filepath.Join(t.TempDir(), "flanj.db")
	s, err := OpenSQLite(path, 0, 0)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	doc := []byte("openapi: 3.0.0\ninfo:\n  title: Acme Payments API\n  version: 1.0.0\n")
	if _, err := s.PutUploadedSpec(model.SpecInfo{
		Integration: "api-acme-test",
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatOpenAPI,
		PeerHost:    "api.acme.test",
		Title:       "Acme Payments API",
		Version:     "1.0.0",
		Source:      model.SpecSourceUpload,
	}, doc); err != nil {
		t.Fatalf("upload legacy contract: %v", err)
	}
	s.Close()

	if _, err := MigrateFromSQLite(pg, path); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	infos, err := pg.ListSpecInfos()
	if err != nil {
		t.Fatalf("ListSpecInfos after migration: %v", err)
	}
	var found *model.SpecInfo
	for i := range infos {
		if infos[i].PeerHost == "api.acme.test" {
			found = &infos[i]
		}
	}
	if found == nil {
		t.Fatalf("uploaded contract did not survive the backend switch — it existed ONLY here, "+
			"and the sqlite file has been renamed to .migrated (infos=%+v)", infos)
	}
	if found.Title != "Acme Payments API" || found.Source != model.SpecSourceUpload {
		t.Errorf("migrated contract = %+v, want the uploaded title and source preserved", *found)
	}

	// The DOCUMENT itself must cross, not just the metadata row — a front reads
	// the doc over the spec endpoint, and metadata alone validates nothing.
	raw, _, ok, err := pg.GetSpecDoc("api-acme-test")
	if err != nil || !ok {
		t.Fatalf("GetSpecDoc after migration: ok=%v err=%v", ok, err)
	}
	if string(raw) != string(doc) {
		t.Errorf("migrated contract document differs from the uploaded one")
	}
}

// TestMigrateFromSQLite_CarriesDriftedFlag: the per-call drift mark is part of
// the evidence, so it must cross the backend switch with the call.
//
// BUG (cross-repo review, 2026-09-01): copyPinnedCalls omitted `drifted` from
// both its SELECT and its INSERT, so every migrated call took the column's
// DEFAULT 0. Since every call it copies is `pinned=1` — i.e. by pin-on-finding
// every one of them produced a finding — a backend switch relabelled the whole
// retained evidence window as `conforming`.
func TestMigrateFromSQLite_CarriesDriftedFlag(t *testing.T) {
	pods := pgPods(t, 1, 0, 0)
	pg := pods[0]

	path := filepath.Join(t.TempDir(), "flanj.db")
	s, err := OpenSQLite(path, 0, 0)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	call := makeEdgeCall(300, "api.acme.test", "client", "external")
	if err := s.InsertCall(call); err != nil {
		t.Fatalf("insert call: %v", err)
	}
	if err := s.InsertFinding(driftFinding("0191e8c4-dddd-7000-8000-000000000001", call.ID)); err != nil {
		t.Fatalf("insert finding: %v", err)
	}
	if got, ok, _ := s.GetCall(call.ID); !ok || !got.Drifted {
		t.Fatalf("precondition: legacy call drifted=%v ok=%v, want true", got.Drifted, ok)
	}
	s.Close()

	if _, err := MigrateFromSQLite(pg, path); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, ok, err := pg.GetCall(call.ID)
	if err != nil || !ok {
		t.Fatalf("GetCall after migration: ok=%v err=%v", ok, err)
	}
	if !got.Drifted {
		t.Errorf("migrated call: drifted=false, want true — a backend switch must not "+
			"relabel retained drift evidence as conforming")
	}
}
