package store

import (
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// TestDriftIsPerCallNotPerEndpoint is the regression for the reported bug:
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
			t.Errorf("late-arriving call: drifted=false, want true — the store holds a " +
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

// TestOutputMismatchMarksItsCall: an MCP output_mismatch is a PER-CALL finding
// and must mark the call it names, exactly as live-vs-spec does on REST.
//
// BUG (2026-09-02): the gate read `f.Kind == model.KindLiveVsSpec`, so an
// output_mismatch left `calls.drifted` at 0 even though the finding carries a
// SourceCallID pointing straight at the offending call. The Traffic tab had
// nothing per-call to read for MCP and fell back to a set keyed by
// (integration, tool) — "does this tool CURRENTLY have a mismatch?" — which:
//   - relabelled every historic call of the tool, including ones captured
//     before the mismatch and ones whose results conformed;
//   - accused the provider over calls the processor never judged (an isError
//     result, whose own tooltip says it is an execution failure and not
//     contract drift).
//
// The whole point of this column is that a verdict is a property of the CALL.
func TestOutputMismatchMarksItsCall(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()

		// Two calls of the SAME tool: one whose result violated the declared
		// outputSchema, one the processor never produced a finding for.
		bad := makeEdgeCall(2200, "mcp.acme.test", "client", "external")
		clean := makeEdgeCall(2201, "mcp.acme.test", "client", "external")
		for _, c := range []model.RedactedCall{bad, clean} {
			if err := s.InsertCall(c); err != nil {
				t.Fatalf("insert %s: %v", c.ID, err)
			}
		}

		f := driftFinding("0191e8c4-eeee-7000-8000-000000000002", bad.ID)
		f.Kind = model.KindOutputMismatch
		f.Integration = "acme-tools"
		f.Endpoint = "get_balance"
		f.Signature = f.ComputeSignature()
		if err := s.InsertFinding(f); err != nil {
			t.Fatalf("insert output_mismatch: %v", err)
		}

		got, ok, err := s.GetCall(bad.ID)
		if err != nil || !ok {
			t.Fatalf("GetCall(%s) ok=%v err=%v", bad.ID, ok, err)
		}
		if !got.Drifted {
			t.Errorf("the call an output_mismatch names: drifted=false, want true — " +
				"without it the UI has to guess per tool, and relabels the tool's whole history")
		}

		// The neighbour is the other half: marking the call must not mark the TOOL.
		other, ok, err := s.GetCall(clean.ID)
		if err != nil || !ok {
			t.Fatalf("GetCall(%s) ok=%v err=%v", clean.ID, ok, err)
		}
		if other.Drifted {
			t.Errorf("a sibling call of the same tool produced no finding but is marked drifted")
		}
	})
}

// TestLatePin_MarksOutputMismatch: and it must survive the finding-before-call
// ordering too. latePin's repair mirrors InsertFinding off ONE kind list; a kind
// honoured on one path and not the other would make an MCP call's verdict depend
// on which record the store happened to see first.
func TestLatePin_MarksOutputMismatch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()
		call := makeEdgeCall(2300, "mcp.acme.test", "client", "external")

		f := driftFinding("0191e8c4-eeee-7000-8000-000000000003", call.ID)
		f.Kind = model.KindOutputMismatch
		f.Signature = f.ComputeSignature()
		if err := s.InsertFinding(f); err != nil {
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
			t.Errorf("late-arriving MCP call: drifted=false, want true — the store holds an " +
				"output_mismatch against it and the UI would render it `conforming`")
		}
	})
}

// TestLatePin_DoesNotMarkNonDriftKinds: the flag means "this call departed from
// a contract", so only the per-call kinds may set it. A call pinned by a
// stale_client finding must stay unmarked: that finding is about the CONSUMER's
// own stale arguments, and marking it drifted would file our bug against the
// provider. The repair must not overshoot what InsertFinding itself does.
func TestLatePin_DoesNotMarkNonDriftKinds(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()
		call := makeEdgeCall(2100, "mcp.acme.test", "client", "external")

		f := driftFinding("0191e8c4-eeee-7000-8000-000000000004", call.ID)
		f.Kind = model.KindStaleClient
		f.Signature = f.ComputeSignature()
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
				"the late repair must mirror InsertFinding's kind list exactly", f.Kind)
		}
	})
}

// TestDeprecationPinsItsCallWithoutMarkingItDrifted is the store half of the
// deprecation rule (CONTRACTS §4): the evidence is kept, and nothing is
// accused.
//
// A deprecation names a call — that call is what proves the org actually USES
// the surface going away, so it must be PINNED and survive the rolling window
// like any other evidence. But it must not be marked `drifted`, and the edge's
// `drift_count` must not move: the operation is still declared and the response
// still conformed, so the call departed from nothing. Painting it red would
// accuse a provider of breaking a promise they are in fact keeping while giving
// notice of ending it.
//
// Both halves fall out of `deprecation` being its own KIND, outside
// model.PerCallDriftKinds — this pins that, so a future edit that folds it back
// into a per-call kind fails here rather than in a screenshot.
func TestDeprecationPinsItsCallWithoutMarkingItDrifted(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()

		call := makeEdgeCall(2200, "api.acme.test", "client", "external")
		if err := s.InsertCall(call); err != nil {
			t.Fatalf("insert call: %v", err)
		}
		before, err := s.ListEdges(false)
		if err != nil {
			t.Fatalf("list edges: %v", err)
		}
		driftBefore := map[string]int{}
		for _, e := range before {
			driftBefore[e.PeerHost] = e.DriftCount
		}

		f := driftFinding("0191e8c4-dddd-7000-8000-000000000001", call.ID)
		f.Kind = model.KindDeprecation
		f.Severity = model.SeverityWarning
		f.Rule = "deprecated-operation"
		f.Signature = f.ComputeSignature()
		if err := s.InsertFinding(f); err != nil {
			t.Fatalf("insert deprecation finding: %v", err)
		}

		got, ok, err := s.GetCall(call.ID)
		if err != nil || !ok {
			t.Fatalf("GetCall(%s) ok=%v err=%v", call.ID, ok, err)
		}
		if got.Drifted {
			t.Error("a deprecation marked its call drifted — the call conformed; " +
				"only a per-call drift KIND may set that flag")
		}

		after, err := s.ListEdges(false)
		if err != nil {
			t.Fatalf("list edges: %v", err)
		}
		for _, e := range after {
			if e.DriftCount != driftBefore[e.PeerHost] {
				t.Errorf("edge %s drift_count moved %d -> %d on a deprecation — the Edges row "+
					"would show a DRIFTED chip for traffic that conformed",
					e.PeerHost, driftBefore[e.PeerHost], e.DriftCount)
			}
		}

		// The evidence half, asked the way it matters rather than by reading a
		// column: a pinned call OUTLIVES the rolling window. Push far more
		// traffic through than the window holds; the deprecated call must
		// still be there.
		for i := 0; i < 60; i++ {
			other := makeEdgeCall(3000+i, "api.acme.test", "client", "external")
			if err := s.InsertCall(other); err != nil {
				t.Fatalf("insert filler %d: %v", i, err)
			}
		}
		if _, ok, err := s.GetCall(call.ID); err != nil || !ok {
			t.Errorf("the deprecated call was evicted (ok=%v err=%v) — the one call proving this "+
				"org USES the surface going away must be pinned as evidence", ok, err)
		}
	})
}

// TestLatePin_DeprecationDoesNotBumpTheEdge is the other arrival order of the
// same rule. A finding may land before its call (a front re-sending, a retried
// batch), and latePin repairs the attribution the insert could not make. That
// repair counts findings by kind, so it has to use the SAME list — a kind
// honoured on one path and not the other makes the Edges row's DRIFTED chip
// depend on which record happened to arrive first.
func TestLatePin_DeprecationDoesNotBumpTheEdge(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 50, 0)
		defer s.Close()
		call := makeEdgeCall(2300, "api.globex.test", "client", "external")

		f := driftFinding("0191e8c4-dddd-7000-8000-000000000002", call.ID)
		f.Kind = model.KindDeprecation
		f.Severity = model.SeverityWarning
		f.Rule = "deprecated-operation"
		f.Signature = f.ComputeSignature()
		if err := s.InsertFinding(f); err != nil {
			t.Fatalf("insert deprecation before its call: %v", err)
		}
		if err := s.InsertCall(call); err != nil {
			t.Fatalf("insert late call: %v", err)
		}

		got, ok, err := s.GetCall(call.ID)
		if err != nil || !ok {
			t.Fatalf("GetCall(%s) ok=%v err=%v", call.ID, ok, err)
		}
		if got.Drifted {
			t.Error("the late repair marked a deprecation's call drifted — it must mirror " +
				"InsertFinding's kind list exactly, in both arrival orders")
		}
		edges, err := s.ListEdges(false)
		if err != nil {
			t.Fatalf("list edges: %v", err)
		}
		for _, e := range edges {
			if e.PeerHost == "api.globex.test" && e.DriftCount != 0 {
				t.Errorf("edge %s drift_count = %d after a late deprecation, want 0",
					e.PeerHost, e.DriftCount)
			}
		}
	})
}
