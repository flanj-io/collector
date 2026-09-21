package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// Resolving a finding — the store half.
//
// A resolution is bound either to the finding's evidence version or to its
// occurrence count (model.Resolution.Covers). Both bindings rest on the store:
// the count is advanced here, and the evidence version can only advance if the
// row's document is refreshed here when a newer record lands on it. These tests
// are the oracle for that, the way TestDefinitionChangeRefreshesEvidence is for
// the definition_change arm.

func liveDriftFinding(id, callID, detectedAt string) model.Finding {
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              id,
		Kind:            model.KindLiveVsSpec,
		Severity:        model.SeverityBreaking,
		Integration:     "api.example.com",
		Endpoint:        "GET /v1/orders/{id}",
		FieldPath:       model.Ptr("$.status"),
		Expected:        "string",
		Actual:          "integer",
		Rule:            "type-mismatch",
		SourceCallID:    model.Ptr(callID),
		DetectedAt:      detectedAt,
		OccurrenceCount: 1,
		FirstSeen:       detectedAt,
		LastSeen:        detectedAt,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func versionDiffFinding(id, from, to, detectedAt string) model.Finding {
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              id,
		Kind:            model.KindVersionDiff,
		Severity:        model.SeverityBreaking,
		Integration:     "api.example.com",
		Endpoint:        "GET /v1/orders/{id}",
		FieldPath:       model.Ptr("status"),
		Expected:        "spec " + from,
		Actual:          "spec " + to,
		Rule:            "response-property-removed",
		SpecVersionFrom: model.Ptr(from),
		SpecVersionTo:   model.Ptr(to),
		DetectedAt:      detectedAt,
		OccurrenceCount: 1,
		FirstSeen:       detectedAt,
		LastSeen:        detectedAt,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func deprecatedOperationFinding(id, callID, actual, detectedAt string) model.Finding {
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              id,
		Kind:            model.KindDeprecation,
		Severity:        model.SeverityWarning,
		Integration:     "api.example.com",
		Endpoint:        "GET /v1/orders/{id}",
		Location:        model.Ptr("$.operation"),
		Expected:        "not deprecated",
		Actual:          actual,
		Rule:            "deprecated-operation",
		SourceCallID:    model.Ptr(callID),
		DetectedAt:      detectedAt,
		OccurrenceCount: 1,
		FirstSeen:       detectedAt,
		LastSeen:        detectedAt,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func onlyFinding(t *testing.T, s Store) model.Finding {
	t.Helper()
	fs, err := s.ListFindings(100)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want exactly 1", len(fs))
	}
	return fs[0]
}

func resolutionOf(t *testing.T, s Store, id string) (model.Resolution, bool) {
	t.Helper()
	all, err := s.FindingResolutions()
	if err != nil {
		t.Fatalf("finding resolutions: %v", err)
	}
	r, ok := all[id]
	return r, ok
}

// A version-diff row has a stable signature, so a later version that changes
// the same field lands on it. If the document stayed frozen, spec_version_to
// could never advance and a resolution bound to it would cover every future
// version — the hole the evidence-version binding exists to close.
func TestVersionDiffRefreshesEvidence(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := s.InsertFinding(versionDiffFinding("f_v2", "1.0.0", "2.0.0", "2026-09-01T09:00:00.000Z")); err != nil {
			t.Fatalf("insert v2 diff: %v", err)
		}
		if err := s.InsertFinding(versionDiffFinding("f_v4", "3.0.0", "4.0.0", "2026-09-10T09:00:00.000Z")); err != nil {
			t.Fatalf("insert v4 diff: %v", err)
		}
		f := onlyFinding(t, s)
		if f.ID != "f_v2" {
			t.Errorf("finding id = %q, want the first occurrence's (a thread keys on it)", f.ID)
		}
		if got := f.EvidenceVersion(); got != "4.0.0" {
			t.Fatalf("evidence version = %q, want 4.0.0 — a frozen version means a resolution of 2.0.0 silently covers 4.0.0", got)
		}
		if f.Actual != "spec 4.0.0" {
			t.Errorf("actual = %q, want the newer version's", f.Actual)
		}

		// An older diff delivered late must not drag the row back.
		if err := s.InsertFinding(versionDiffFinding("f_late", "1.0.0", "2.0.0", "2026-09-01T09:00:00.000Z")); err != nil {
			t.Fatalf("insert late v2 diff: %v", err)
		}
		if got := onlyFinding(t, s).EvidenceVersion(); got != "4.0.0" {
			t.Fatalf("evidence version after a late replay = %q, want 4.0.0", got)
		}
	})
}

// The traffic arm of a deprecation is bound to the announcement, which rides
// `actual`. A sunset date appearing has to reach the stored row, and the row has
// to keep the source call it PINNED — the refreshed record names a newer call
// nothing pinned, which would age out and leave the finding without evidence.
func TestDeprecationRefreshesAnnouncementAndKeepsItsPinnedCall(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := s.InsertFinding(deprecatedOperationFinding("f_dep", "call_first", "deprecated", "2026-09-01T09:00:00.000Z")); err != nil {
			t.Fatalf("insert deprecation: %v", err)
		}
		if err := s.InsertFinding(deprecatedOperationFinding("f_dep2", "call_later", "deprecated, sunset 2026-12-31", "2026-09-05T09:00:00.000Z")); err != nil {
			t.Fatalf("insert dated deprecation: %v", err)
		}
		f := onlyFinding(t, s)
		if got := f.EvidenceVersion(); got != "deprecated, sunset 2026-12-31" {
			t.Fatalf("evidence version = %q, want the dated announcement", got)
		}
		if f.ID != "f_dep" {
			t.Errorf("finding id = %q, want f_dep", f.ID)
		}
		if derefStr(f.SourceCallID) != "call_first" {
			t.Errorf("source_call_id = %q, want call_first — the pinned one", derefStr(f.SourceCallID))
		}
	})
}

func TestResolveFindingKeepsTheRowAndReopens(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := s.InsertFinding(liveDriftFinding("f_live", "call_1", "2026-09-01T09:00:00.000Z")); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, ok := resolutionOf(t, s, "f_live"); ok {
			t.Fatal("a new finding must carry no resolution")
		}

		want := model.Resolution{ResolvedAt: "2026-09-01T10:00:00Z", OccurrenceCount: 1, Note: "fixed in the client"}
		ok, err := s.ResolveFinding("f_live", want)
		if err != nil || !ok {
			t.Fatalf("resolve: ok=%v err=%v", ok, err)
		}
		got, has := resolutionOf(t, s, "f_live")
		if !has || got != want {
			t.Fatalf("resolution = %+v (present %v), want %+v", got, has, want)
		}
		// Resolving is not deleting: the row is still listed, untouched.
		if f := onlyFinding(t, s); f.ID != "f_live" || f.OccurrenceCount != 1 {
			t.Fatalf("finding after resolve = %q ×%d, want f_live ×1", f.ID, f.OccurrenceCount)
		}

		// It survives a restart — it is a row's state, not a process's.
		_ = s.Close()
		s = b.reopen(t, 0, 0)
		if got, has := resolutionOf(t, s, "f_live"); !has || got != want {
			t.Fatalf("resolution after reopen of the store = %+v (present %v)", got, has)
		}

		ok, err = s.ReopenFinding("f_live")
		if err != nil || !ok {
			t.Fatalf("reopen: ok=%v err=%v", ok, err)
		}
		if _, has := resolutionOf(t, s, "f_live"); has {
			t.Fatal("a reopened finding must carry no resolution")
		}

		if ok, err := s.ResolveFinding("f_missing", want); err != nil || ok {
			t.Fatalf("resolve of an unknown id: ok=%v err=%v, want false/nil", ok, err)
		}
		if ok, err := s.ReopenFinding("f_missing"); err != nil || ok {
			t.Fatalf("reopen of an unknown id: ok=%v err=%v, want false/nil", ok, err)
		}
	})
}

// THE SAFETY PROPERTY: a resolution can never hide new trouble.
func TestRecurrenceAfterResolveReopensTheFinding(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		first := liveDriftFinding("f_live", "call_1", "2026-09-01T09:00:00.000Z")
		if err := s.InsertFinding(first); err != nil {
			t.Fatalf("insert: %v", err)
		}
		f := onlyFinding(t, s)
		res := model.Resolution{ResolvedAt: "2026-09-01T10:00:00Z", OccurrenceCount: f.OccurrenceCount}
		if _, err := s.ResolveFinding(f.ID, res); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !res.Covers(onlyFinding(t, s)) {
			t.Fatal("a just-resolved finding must be covered")
		}

		// The SAME record delivered again (an exporter retry) is not a recurrence.
		if err := s.InsertFinding(first); err != nil {
			t.Fatalf("re-deliver: %v", err)
		}
		if stored, _ := resolutionOf(t, s, "f_live"); !stored.Covers(onlyFinding(t, s)) {
			t.Fatal("a re-delivered record reopened the finding; only a new occurrence may")
		}

		// A new occurrence is. Its timestamp is deliberately EARLIER than
		// resolved_at: the detecting pod's clock is not the UI pod's, and a
		// resolution measured in time would hide this one.
		if err := s.InsertFinding(liveDriftFinding("f_again", "call_2", "2026-09-01T09:59:59.000Z")); err != nil {
			t.Fatalf("insert recurrence: %v", err)
		}
		after := onlyFinding(t, s)
		if after.ID != "f_live" {
			t.Fatalf("finding id = %q, want f_live — a reopened finding keeps its id and its thread", after.ID)
		}
		stored, has := resolutionOf(t, s, "f_live")
		if !has {
			t.Fatal("the lapsed resolution must stay readable — the row says when it was resolved and that it came back")
		}
		if stored.Covers(after) {
			t.Fatalf("a recurrence after resolve is still covered (count %d, resolved at %d) — a resolution is hiding new trouble", after.OccurrenceCount, stored.OccurrenceCount)
		}
	})
}

func TestNewEvidenceVersionResurfacesAResolvedChange(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := s.InsertFinding(versionDiffFinding("f_v2", "1.0.0", "2.0.0", "2026-09-01T09:00:00.000Z")); err != nil {
			t.Fatalf("insert: %v", err)
		}
		f := onlyFinding(t, s)
		res := model.Resolution{ResolvedAt: "2026-09-02T10:00:00Z", EvidenceVersion: f.EvidenceVersion(), OccurrenceCount: f.OccurrenceCount}
		if _, err := s.ResolveFinding(f.ID, res); err != nil {
			t.Fatalf("resolve: %v", err)
		}

		// The same diff re-run (the same document bound again) is the same evidence.
		if err := s.InsertFinding(versionDiffFinding("f_rerun", "1.0.0", "2.0.0", "2026-09-03T09:00:00.000Z")); err != nil {
			t.Fatalf("insert re-run: %v", err)
		}
		if !res.Covers(onlyFinding(t, s)) {
			t.Fatal("re-running the same diff reopened a resolved change")
		}

		if err := s.InsertFinding(versionDiffFinding("f_v4", "3.0.0", "4.0.0", "2026-09-10T09:00:00.000Z")); err != nil {
			t.Fatalf("insert v4: %v", err)
		}
		if res.Covers(onlyFinding(t, s)) {
			t.Fatal("a resolution of 2.0.0 still covers 4.0.0 — a new version arrived silently resolved")
		}
	})
}

// A backend switch carries resolutions — an operator's cleared view must not
// come back full — and a file from before the columns existed still imports:
// the source is opened read-only, so migrate() never ran on it, and selecting a
// column it lacks would abort the whole import.
func TestMigrateFromSQLite_CarriesResolutions(t *testing.T) {
	build := func(t *testing.T, dropColumns bool) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "flanj.db")
		s, err := OpenSQLite(path, 0, 0)
		if err != nil {
			t.Fatalf("open legacy sqlite: %v", err)
		}
		if err := s.InsertFinding(versionDiffFinding("f_v2", "1.0.0", "2.0.0", "2026-09-01T09:00:00.000Z")); err != nil {
			t.Fatal(err)
		}
		if !dropColumns {
			if _, err := s.ResolveFinding("f_v2", model.Resolution{
				ResolvedAt: "2026-09-02T10:00:00Z", EvidenceVersion: "2.0.0", OccurrenceCount: 1, Note: "pinned to v1"}); err != nil {
				t.Fatal(err)
			}
		}
		_ = s.Close()
		if dropColumns {
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			for _, col := range []string{"resolved_at", "resolved_evidence_version", "resolved_occurrence_count", "resolved_note"} {
				if _, err := raw.Exec(`ALTER TABLE findings DROP COLUMN ` + col); err != nil {
					t.Fatalf("drop %s on the legacy file: %v", col, err)
				}
			}
			_ = raw.Close()
		}
		return path
	}

	t.Run("current file", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		if _, err := MigrateFromSQLite(pg, build(t, false)); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		got, err := pg.FindingResolutions()
		if err != nil {
			t.Fatal(err)
		}
		want := model.Resolution{ResolvedAt: "2026-09-02T10:00:00Z", EvidenceVersion: "2.0.0", OccurrenceCount: 1, Note: "pinned to v1"}
		if got["f_v2"] != want {
			t.Errorf("migrated resolution = %+v, want %+v", got["f_v2"], want)
		}
	})
	t.Run("file from before the columns", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		if _, err := MigrateFromSQLite(pg, build(t, true)); err != nil {
			t.Fatalf("migrate of a pre-resolution file: %v", err)
		}
		fs, err := pg.ListFindings(10)
		if err != nil || len(fs) != 1 {
			t.Fatalf("findings after migrate = %d (%v), want the one finding carried", len(fs), err)
		}
		if got, _ := pg.FindingResolutions(); len(got) != 0 {
			t.Errorf("resolutions = %v, want none", got)
		}
	})
}
