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
