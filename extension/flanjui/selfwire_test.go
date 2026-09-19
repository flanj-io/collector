package flanjui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// An inbound call is keyed locally by the service it reached, and so is a
// finding raised against the self spec. Neither name may leave the collector
// (CONTRACTS §3): the flag relay and the findings sync both carry "self" in its
// place — read off the store, so the sync, which has no call in hand, gets it
// right too.

const selfWireService = "orders-svc-sentinel"

func seedSelfFinding(t *testing.T, r *testRig) {
	t.Helper()
	callID := "call_in"
	if err := r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-09-19T10:00:00Z",
		Integration: selfWireService, ServiceName: selfWireService, Direction: "server", PeerHost: "partner.acme.test",
		Method: "GET", URL: "https://api.example.test/v1/orders/ord_1", Route: "/v1/orders/{id}", StatusCode: 200,
		ResponseBody: `{"total":"10"}`, Redaction: model.Redaction{Patterns: []string{}}}); err != nil {
		t.Fatal(err)
	}
	f := model.Finding{SchemaVersion: 1, ID: "fnd_self", Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
		Integration: selfWireService, Endpoint: "GET /v1/orders/{id}", FieldPath: model.Ptr("total"), Expected: "integer",
		Actual: "string", Rule: "type-mismatch", SourceCallID: &callID, DetectedAt: "2026-09-19T10:00:01Z"}
	f.Signature = f.ComputeSignature()
	if err := r.st.InsertFinding(f); err != nil {
		t.Fatal(err)
	}
}

func TestSelfFinding_SyncCarriesSelf(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedSelfFinding(t, r)

	r.ext.syncFindingsOnce(context.Background())
	if r.cp.findingsCallCount() != 1 {
		t.Fatalf("findings posts = %d, want 1", r.cp.findingsCallCount())
	}
	raw := r.cp.findingsBody(0)
	if strings.Contains(string(raw), selfWireService) {
		t.Errorf("the findings sync carries the service name: %s", raw)
	}
	var body struct {
		Findings []struct {
			FindingID   string `json:"finding_id"`
			Integration string `json:"integration"`
			Signature   string `json:"signature"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, f := range body.Findings {
		if f.FindingID == "fnd_self" && (f.Integration != "self" || !strings.HasPrefix(f.Signature, "self|")) {
			t.Errorf("self finding on the wire: integration %q, signature %q; want self", f.Integration, f.Signature)
		}
		if f.FindingID == "fnd_1" && f.Integration != "acme-payments" {
			t.Errorf("outbound finding on the wire: integration %q, want it unchanged", f.Integration)
		}
	}
}

func TestSelfFinding_FlagCarriesSelf(t *testing.T) {
	r := connectedRig(t)
	seedSelfFinding(t, r)

	resp, _, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_self", "allowed_domains": nil})
	if resp.StatusCode != 201 {
		t.Fatalf("flag: %d %s", resp.StatusCode, raw)
	}
	wire, err := json.Marshal(r.cp.lastFlagBody)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), selfWireService) {
		t.Errorf("the flag request carries the service name: %s", wire)
	}
	call, _ := r.cp.lastFlagBody["call"].(map[string]any)
	finding, _ := r.cp.lastFlagBody["finding"].(map[string]any)
	if call["integration"] != "self" || finding["integration"] != "self" {
		t.Errorf("integration on the wire: call %v, finding %v; want self on both", call["integration"], finding["integration"])
	}
}
