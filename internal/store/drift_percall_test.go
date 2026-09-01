package store

import (
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// TestDriftIsPerCallNotPerEndpoint is the regression for the bug Idan hit:
// "once I sent a single drifted API call, all previous conforming calls are
// also marked as drifted."
//
// The UI could only ask "has this ENDPOINT ever drifted?", because the store
// pinned only the FIRST occurrence of a signature and recorded nothing about
// any other call. Every call to a drifted endpoint therefore rendered as
// drifted — including the ones that conformed, and including ones captured
// BEFORE the drift ever happened. That is the same false-assurance class as
// CONFORMING-with-no-evidence, pointing the other way.
//
// The store must record drift ON THE CALL, for every occurrence.
func TestDriftIsPerCallNotPerEndpoint(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		call := func(id string) model.RedactedCall {
			return model.RedactedCall{
				SchemaVersion: 1, ID: id, CapturedAt: "2026-09-01T10:00:00Z",
				Integration: "acme-payments", PeerHost: "api.acme.test", Direction: "client",
				EdgeClass: "external", Method: "POST", Route: "/v1/charges", StatusCode: 200,
			}
		}
		// Two conforming calls on the endpoint, THEN one that drifts, THEN a
		// second drifting one (same signature -> dedup'd into the same finding).
		for _, id := range []string{"conforming_1", "conforming_2", "drifted_1", "drifted_2"} {
			if err := st.InsertCall(call(id)); err != nil {
				t.Fatalf("insert %s: %v", id, err)
			}
		}
		finding := func(id, srcID string) model.Finding {
			src := srcID
			f := model.Finding{
				SchemaVersion: 1, ID: id, Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
				Integration: "acme-payments", Endpoint: "POST /v1/charges", Rule: "type",
				SourceCallID: &src, DetectedAt: "2026-09-01T10:00:01Z", OccurrenceCount: 1,
				FirstSeen: "2026-09-01T10:00:01Z", LastSeen: "2026-09-01T10:00:01Z",
			}
			f.Signature = f.ComputeSignature()
			return f
		}
		if err := st.InsertFinding(finding("fnd_1", "drifted_1")); err != nil {
			t.Fatal(err)
		}
		// The REPEAT is the half that was lost: same signature, different call.
		if err := st.InsertFinding(finding("fnd_2", "drifted_2")); err != nil {
			t.Fatal(err)
		}

		got := map[string]bool{}
		rows, err := st.ListCalls(50)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range rows {
			got[c.ID] = c.Drifted
		}
		for _, id := range []string{"drifted_1", "drifted_2"} {
			if !got[id] {
				t.Errorf("%s produced a finding but is not marked drifted", id)
			}
		}
		for _, id := range []string{"conforming_1", "conforming_2"} {
			if got[id] {
				t.Errorf("%s never drifted, but the store marked it drifted — "+
					"this is the bug: one drifting call relabelling its neighbours", id)
			}
		}
	})
}

// TestLatePin_RepairsDrifted: the per-call `drifted` flag must survive the
// finding-before-call ordering the store is required to tolerate.
//
// BUG (cross-repo review, 2026-09-01): InsertFinding marks drift with
// `UPDATE calls SET drifted=1 WHERE id=?`, which matches zero rows when the
// call has not landed yet. latePin then repaired `pinned` and the edge's
// drift_count and nothing else, so the call was kept as evidence, counted as a
// drift on its edge — and still rendered `conforming` in the UI. False
// assurance about the exact call the operator kept the evidence for.
//
// Non-negotiable 6 makes this a store guarantee, not a caller's problem: "the
// store is order-independent for call/finding pairs (late pin) — nothing
// upstream may rely on or compensate for record order."
func TestLatePin_RepairsDrifted(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		call := makeEdgeCall(2000, "api.acme.test", "client", "external")

		// Finding first, call second — the order latePin exists for.
		if err := s.InsertFinding(driftFinding("0191e8c4-eeee-7000-8000-000000000001", call.ID)); err != nil {
			t.Fatalf("insert finding before call: %v", err)
		}
		if err := s.InsertCall(call); err != nil {
			t.Fatalf("insert late call: %v", err)
		}

		got, ok, err := s.GetCall(call.ID)
		if err != nil || !ok {
			t.Fatalf("GetCall(%s) ok=%v err=%v", call.ID, ok, err)
		}
		if !got.Drifted {
			t.Errorf("late-arriving call: drifted=false, want true — the store holds a "+
				"live-vs-spec finding against it and would render it `conforming`")
		}

		// ListCalls must agree with GetCall — they read the column by different paths.
		calls, err := s.ListCalls(10)
		if err != nil {
			t.Fatalf("ListCalls: %v", err)
		}
		var seen bool
		for _, c := range calls {
			if c.ID == call.ID {
				seen = true
				if !c.Drifted {
					t.Errorf("ListCalls: drifted=false for the late call, want true")
				}
			}
		}
		if !seen {
			t.Fatalf("late call missing from ListCalls")
		}
	})
}

// TestLatePin_DoesNotMarkNonDriftKinds: the flag means "this call drifted from
// its contract", so only a live-vs-spec finding may set it. A call pinned by
// some other finding kind must stay unmarked — the repair must not overshoot
// what InsertFinding itself does.
func TestLatePin_DoesNotMarkNonDriftKinds(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		call := makeEdgeCall(2100, "mcp.acme.test", "client", "external")

		f := driftFinding("0191e8c4-eeee-7000-8000-000000000002", call.ID)
		f.Kind = model.KindOutputMismatch
		if err := s.InsertFinding(f); err != nil {
			t.Fatalf("insert non-drift finding before call: %v", err)
		}
		if err := s.InsertCall(call); err != nil {
			t.Fatalf("insert late call: %v", err)
		}

		got, ok, err := s.GetCall(call.ID)
		if err != nil || !ok {
			t.Fatalf("GetCall(%s) ok=%v err=%v", call.ID, ok, err)
		}
		if got.Drifted {
			t.Errorf("call pinned by a %s finding: drifted=true, want false — "+
				"the late repair must mirror InsertFinding, which marks live-vs-spec only", f.Kind)
		}
	})
}
