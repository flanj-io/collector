package store

import (
	"fmt"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// TestInboundFindings_RecordedAndDurable: a finding whose source call was
// inbound is recorded as such whichever of the two arrives first, a finding on
// an outbound call is not, and the record outlives the call — a flagged
// finding's call is unpinned and ages out, while the findings sync goes on
// posting the finding and must still send "self" for its service-name key.
func TestInboundFindings_RecordedAndDurable(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 3, 1<<30)
		call := func(id, direction, at string) model.RedactedCall {
			integration := "api-acme-test"
			if direction == "server" {
				integration = "orders-svc"
			}
			return model.RedactedCall{SchemaVersion: 1, ID: id, CapturedAt: at, Integration: integration,
				Direction: direction, PeerHost: "partner.acme.test", Method: "GET", Route: "/v1/orders/{id}", StatusCode: 200}
		}
		finding := func(id, callID, integration string) model.Finding {
			f := model.Finding{SchemaVersion: 1, ID: id, Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
				Integration: integration, Endpoint: "GET /v1/orders/{id}", FieldPath: model.Ptr(id), Rule: "type-mismatch",
				SourceCallID: &callID, DetectedAt: "2026-09-19T10:00:00Z", OccurrenceCount: 1,
				FirstSeen: "2026-09-19T10:00:00Z", LastSeen: "2026-09-19T10:00:00Z"}
			f.Signature = f.ComputeSignature()
			return f
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		// Call first, then its finding.
		must(st.InsertCall(call("c_in", "server", "2026-09-19T10:00:00Z")))
		must(st.InsertFinding(finding("f_in", "c_in", "orders-svc")))
		// Finding first (late pin), then its call.
		must(st.InsertFinding(finding("f_late", "c_late", "orders-svc")))
		must(st.InsertCall(call("c_late", "server", "2026-09-19T10:00:01Z")))
		// Outbound.
		must(st.InsertCall(call("c_out", "client", "2026-09-19T10:00:02Z")))
		must(st.InsertFinding(finding("f_out", "c_out", "api-acme-test")))

		want := map[string]bool{"f_in": true, "f_late": true}
		got, err := st.InboundFindingIDs()
		must(err)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("inbound findings = %v, want %v", got, want)
		}

		// Flag f_in (its call unpins), then let traffic evict that call.
		must(st.MarkPromoted("c_in"))
		for i := 0; i < 3; i++ {
			must(st.InsertCall(call(fmt.Sprintf("c_new_%d", i), "client", fmt.Sprintf("2026-09-19T11:00:0%dZ", i))))
		}
		if _, ok, err := st.GetCall("c_in"); err != nil || ok {
			t.Fatalf("GetCall(c_in) = ok %v, err %v; the test needs the call evicted", ok, err)
		}
		got, err = st.InboundFindingIDs()
		must(err)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("after the call aged out: inbound findings = %v, want %v", got, want)
		}
	})
}
