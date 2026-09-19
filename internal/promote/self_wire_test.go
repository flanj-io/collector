package promote

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// A service name is the org's internal topology and never leaves the collector
// (CONTRACTS §3). An inbound call — and a finding raised against the self
// spec — is keyed locally by the service the call reached, so everything that
// crosses to the control plane carries "self" in its place: the call, the
// finding, and the finding's signature, which starts with the key.

const serviceSentinel = "orders-svc-sentinel"

func inboundCall() model.RedactedCall {
	return model.RedactedCall{
		SchemaVersion: 1, ID: "call_in", CapturedAt: "2026-09-19T10:00:00Z",
		Integration: serviceSentinel, ServiceName: serviceSentinel, Direction: "server",
		PeerHost: "partner.acme.test", EdgeClass: "external",
		Method: "GET", URL: "https://api.example.test/v1/orders/ord_1", Route: "/v1/orders/{id}", StatusCode: 200,
		Redaction: model.Redaction{Patterns: []string{}},
	}
}

func selfSpecFinding() model.Finding {
	callID := "call_in"
	f := model.Finding{
		SchemaVersion: 1, ID: "fnd_self", Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
		Integration: serviceSentinel, Endpoint: "GET /v1/orders/{id}", FieldPath: model.Ptr("total"),
		Expected: "type=integer", Actual: "type=string", Rule: "type-mismatch",
		SourceCallID: &callID, DetectedAt: "2026-09-19T10:00:01Z",
		OccurrenceCount: 1, FirstSeen: "2026-09-19T10:00:01Z", LastSeen: "2026-09-19T10:00:01Z",
	}
	f.Signature = f.ComputeSignature()
	return f
}

func assertNoServiceName(t *testing.T, what string, wire []byte) {
	t.Helper()
	if bytes.Contains(wire, []byte(serviceSentinel)) {
		t.Errorf("%s carries the service name %q: %s", what, serviceSentinel, wire)
	}
}

func TestBuild_InboundCallSendsSelfNeverTheServiceName(t *testing.T) {
	call := inboundCall()
	req := Build(Input{Call: &call, Finding: selfSpecFinding()})
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	assertNoServiceName(t, "the flag request", wire)
	var body struct {
		Call    struct{ Integration string } `json:"call"`
		Finding struct {
			Integration string `json:"integration"`
			Signature   string `json:"signature"`
		} `json:"finding"`
	}
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatal(err)
	}
	if body.Call.Integration != "self" || body.Finding.Integration != "self" {
		t.Errorf("integration on the wire: call %q, finding %q; want self on both", body.Call.Integration, body.Finding.Integration)
	}
	if want := "self|GET /v1/orders/{id}|live-vs-spec|type-mismatch|total"; body.Finding.Signature != want {
		t.Errorf("finding signature = %q, want %q", body.Finding.Signature, want)
	}
	// The caller's records are untouched: the store keeps the local key.
	if call.Integration != serviceSentinel || call.ServiceName != serviceSentinel {
		t.Errorf("Build mutated the caller's call: %+v", call)
	}
}

// The finding's call has aged out: the store's record says it was inbound.
func TestBuild_CallLessInboundFindingSendsSelf(t *testing.T) {
	req := Build(Input{Finding: selfSpecFinding(), Inbound: true})
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	assertNoServiceName(t, "the call-less flag request", wire)
	if req.Call != nil || req.Finding.Integration != "self" {
		t.Errorf("call %v, finding integration %q; want no call and self", req.Call, req.Finding.Integration)
	}
}

// An outbound call's host key crosses unchanged.
func TestBuild_OutboundKeyCrossesUnchanged(t *testing.T) {
	call := inboundCall()
	call.Direction, call.Integration = "client", "api-acme-test"
	f := selfSpecFinding()
	f.Integration = "api-acme-test"
	f.Signature = f.ComputeSignature()
	req := Build(Input{Call: &call, Finding: f})
	if req.Call.Integration != "api-acme-test" || req.Finding.Integration != "api-acme-test" || req.Finding.Signature != f.Signature {
		t.Errorf("outbound flag rewrote the key: call %q, finding %q / %q", req.Call.Integration, req.Finding.Integration, req.Finding.Signature)
	}
}

func TestBuildFindingShapes_SelfFindingNeverSendsTheServiceName(t *testing.T) {
	provider := selfSpecFinding()
	provider.ID, provider.Integration = "fnd_out", "api-acme-test"
	provider.Signature = provider.ComputeSignature()
	shapes := BuildFindingShapes([]model.Finding{selfSpecFinding(), provider}, map[string]bool{"fnd_self": true})
	wire, err := json.Marshal(FindingsRequest{Findings: shapes})
	if err != nil {
		t.Fatal(err)
	}
	assertNoServiceName(t, "the findings sync", wire)
	for _, s := range shapes {
		want := map[string]string{"fnd_self": "self", "fnd_out": "api-acme-test"}[s.FindingID]
		if s.Integration != want || s.Signature[:len(want)+1] != want+"|" {
			t.Errorf("%s: integration %q, signature %q; want both keyed %q", s.FindingID, s.Integration, s.Signature, want)
		}
	}
}
