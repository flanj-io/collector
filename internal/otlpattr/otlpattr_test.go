package otlpattr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/vinifera-io/collector/internal/model"
)

func contractsDir() string { return filepath.Join("..", "..", "contracts") }

// recordFromFixture parses the first log record of an OTLP/HTTP-JSON fixture into
// a plog.LogRecord using the same attribute mapping the collector uses at runtime.
func recordFromFixture(t *testing.T, name string) plog.LogRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractsDir(), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue *string `json:"stringValue"`
							IntValue    *string `json:"intValue"`
							BoolValue   *bool   `json:"boolValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	lr := plog.NewLogRecord()
	rec := payload.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	for _, a := range rec.Attributes {
		switch {
		case a.Value.StringValue != nil:
			lr.Attributes().PutStr(a.Key, *a.Value.StringValue)
		case a.Value.IntValue != nil:
			var n int64
			for _, c := range *a.Value.IntValue {
				n = n*10 + int64(c-'0')
			}
			lr.Attributes().PutInt(a.Key, n)
		case a.Value.BoolValue != nil:
			lr.Attributes().PutBool(a.Key, *a.Value.BoolValue)
		}
	}
	return lr
}

// TestCallFromRecord_ClientDirection asserts the egress (client) golden call maps
// to a consumer-side edge with the external peer host + class carried through.
func TestCallFromRecord_ClientDirection(t *testing.T) {
	call := CallFromRecord(recordFromFixture(t, "golden-otlp-call.json"))
	if call.Direction != "client" {
		t.Errorf("direction = %q, want client", call.Direction)
	}
	if call.PeerHost != "api.acme.test" {
		t.Errorf("peer_host = %q, want api.acme.test", call.PeerHost)
	}
	if call.EdgeClass != "external" {
		t.Errorf("edge_class = %q, want external", call.EdgeClass)
	}
	if call.Route != "/v1/charges" || call.Method != "POST" {
		t.Errorf("route/method = %q %q", call.Method, call.Route)
	}
}

// TestCallFromRecord_ServerDirection asserts the ingress (server) golden call maps
// to a provider-side edge with the external peer host + class carried through.
func TestCallFromRecord_ServerDirection(t *testing.T) {
	call := CallFromRecord(recordFromFixture(t, "golden-otlp-server-call.json"))
	if call.Direction != "server" {
		t.Errorf("direction = %q, want server", call.Direction)
	}
	if call.PeerHost != "partner.acme.test" {
		t.Errorf("peer_host = %q, want partner.acme.test", call.PeerHost)
	}
	if call.EdgeClass != "external" {
		t.Errorf("edge_class = %q, want external", call.EdgeClass)
	}
}

// TestCallFromRecord_ClassFallback proves a missing vinifera.edge.class is
// reconstructed from the peer host via the shared heuristic.
func TestCallFromRecord_ClassFallback(t *testing.T) {
	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(AttrDirection, "client")
	lr.Attributes().PutStr(AttrPeerHost, "10.0.0.5:8080")
	lr.Attributes().PutStr(AttrMethod, "POST")
	lr.Attributes().PutStr(AttrRoute, "/internal")
	call := CallFromRecord(lr)
	if call.EdgeClass != "internal" {
		t.Errorf("edge_class fallback = %q, want internal (RFC1918)", call.EdgeClass)
	}
}

// TestSpecInfoRecord_RoundTrip: contract metadata + raw document survive the
// record encoding (this is what crosses the front→store hop), and the record is
// typed so the redaction/drift processors skip it and the store exporter routes
// it to PutSpecInfo.
func TestSpecInfoRecord_RoundTrip(t *testing.T) {
	info := model.SpecInfo{
		Integration: "acme-payments",
		Role:        model.SpecRoleProvider,
		PeerHost:    "api.acme.test",
		Format:      "openapi",
		Title:       "Acme Payments",
		Version:     "1.4.0",
		DocsURL:     "https://docs.acme.test",
		Endpoints:   3,
		LoadedAt:    "2026-08-23T10:00:00Z",
	}
	raw := []byte("openapi: 3.0.3\ninfo:\n  title: Acme Payments\n")

	lr := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	if err := SpecInfoToRecord(lr, info, raw); err != nil {
		t.Fatalf("to record: %v", err)
	}
	if got := RecordType(lr); got != RecordTypeSpecInfo {
		t.Fatalf("record type = %q, want %q", got, RecordTypeSpecInfo)
	}
	gotInfo, gotRaw, err := SpecInfoFromRecord(lr)
	if err != nil {
		t.Fatalf("from record: %v", err)
	}
	if gotInfo != info {
		t.Errorf("spec info round trip mismatch:\n got %+v\nwant %+v", gotInfo, info)
	}
	if string(gotRaw) != string(raw) {
		t.Errorf("raw doc round trip mismatch: got %q", gotRaw)
	}

	// Empty document is allowed (metadata still flows); a record without the
	// JSON attribute is rejected, never mistaken for a call.
	lr2 := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	if err := SpecInfoToRecord(lr2, info, nil); err != nil {
		t.Fatalf("to record (no raw): %v", err)
	}
	if _, gotRaw, err := SpecInfoFromRecord(lr2); err != nil || len(gotRaw) != 0 {
		t.Errorf("empty raw: raw=%q err=%v", gotRaw, err)
	}
	lr3 := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr3.Attributes().PutStr(AttrRecordType, RecordTypeSpecInfo)
	if _, _, err := SpecInfoFromRecord(lr3); err == nil {
		t.Errorf("record without %s should be rejected", AttrSpecInfoJSON)
	}
}
