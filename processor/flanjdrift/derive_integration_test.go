package flanjdrift

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// The collector derives every record's integration at ingest and ignores any
// SDK-sent flanj.integration (CONTRACTS §2): an outbound HTTP call and every
// MCP record key by the peer host, an inbound HTTP call by the service it
// reached (its resource service.name). These tests drive the processor with a
// deliberately WRONG SDK id on every record, so a regression that reads it
// back shows up as that id.

const sdkSentID = "sdk-sent-id"

// emitted decodes the finding and spec_info records a processed batch carries.
func emitted(t *testing.T, out plog.Logs) ([]model.Finding, []model.SpecInfo) {
	t.Helper()
	var fs []model.Finding
	var infos []model.SpecInfo
	rls := out.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				switch lr := recs.At(k); otlpattr.RecordType(lr) {
				case otlpattr.RecordTypeFinding:
					f, err := otlpattr.FindingFromRecord(lr)
					if err != nil {
						t.Fatalf("decode finding: %v", err)
					}
					fs = append(fs, f)
				case otlpattr.RecordTypeSpecInfo:
					info, _, err := otlpattr.SpecInfoFromRecord(lr)
					if err != nil {
						t.Fatalf("decode spec_info: %v", err)
					}
					infos = append(infos, info)
				}
			}
		}
	}
	return fs, infos
}

func wantKey(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s integration = %q, want %q", what, got, want)
	}
}

// A snapshot that carries no flanj.integration at all — what every current
// SDK sends — is a catalogue like any other: derived from its host, emitted,
// and never mistaken for the session listing the empty-key guard skips.
func TestDerive_SnapshotWithoutSDKIntegrationIsKept(t *testing.T) {
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}
	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotJSON)
	ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().Remove(otlpattr.AttrIntegration)

	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	_, infos := emitted(t, out)
	if len(infos) != 1 {
		t.Fatalf("spec_info records = %d, want 1: a snapshot without an SDK id must not be dropped", len(infos))
	}
	wantKey(t, "catalogue", infos[0].Integration, "mcp-acme-test")
}

// The call rule and the snapshot rule land on ONE key: the UI joins a server's
// calls to its catalogue by integration.
func TestDerive_MCPIgnoresSDKIntegration(t *testing.T) {
	p := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()}
	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotJSON)
	mcpCallRecord(ld, "get_balance", `{"amount":"1200","currency":"usd"}`)
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		ld.ResourceLogs().At(i).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr(otlpattr.AttrIntegration, sdkSentID)
	}
	out, err := p.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	fs, infos := emitted(t, out)
	if len(fs) != 1 || len(infos) != 1 {
		t.Fatalf("findings = %d, spec_infos = %d; want 1 and 1", len(fs), len(infos))
	}
	wantKey(t, "catalogue", infos[0].Integration, "mcp-acme-test")
	wantKey(t, "MCP finding", fs[0].Integration, "mcp-acme-test")
	if !strings.HasPrefix(fs[0].Signature, "mcp-acme-test|") {
		t.Errorf("signature = %q, want it keyed by the derived integration", fs[0].Signature)
	}
}

// Outbound HTTP keys by the peer host.
func TestDerive_OutboundIgnoresSDKIntegration(t *testing.T) {
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})
	out, err := p.processLogs(context.Background(), goldenBatchWith(t, otlpattr.AttrIntegration, sdkSentID))
	if err != nil {
		t.Fatalf("processLogs: %v", err)
	}
	fs, _ := emitted(t, out)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	wantKey(t, "outbound finding", fs[0].Integration, "api-acme-test")
}

// Inbound HTTP keys by the service the call reached: a self-spec finding is
// keyed by the resource's service.name, and by "unknown-integration" when the
// resource names none — never by the SDK id, and no longer by a constant.
func TestDerive_InboundKeysByServiceName(t *testing.T) {
	self, err := drift.LoadSpecData(specV1(t))
	if err != nil {
		t.Fatalf("load self spec: %v", err)
	}
	inbound := func(service string) plog.Logs {
		ld := goldenBatchWith(t, otlpattr.AttrDirection, "server")
		rl := ld.ResourceLogs().At(0)
		if service != "" {
			rl.Resource().Attributes().PutStr(otlpattr.ResourceServiceName, service)
		} else {
			rl.Resource().Attributes().Remove(otlpattr.ResourceServiceName)
		}
		a := rl.ScopeLogs().At(0).LogRecords().At(0).Attributes()
		a.PutStr(otlpattr.AttrPeerHost, "partner.acme.test")
		a.PutStr(otlpattr.AttrIntegration, sdkSentID)
		return ld
	}
	for _, c := range []struct{ service, want string }{
		{"orders-svc", "orders-svc"},
		{"", "unknown-integration"},
	} {
		p := processorWith(t, nil)
		p.selfDoc = self
		out, err := p.processLogs(context.Background(), inbound(c.service))
		if err != nil {
			t.Fatalf("processLogs: %v", err)
		}
		fs, _ := emitted(t, out)
		if len(fs) != 1 {
			t.Fatalf("service %q: findings = %d, want 1", c.service, len(fs))
		}
		wantKey(t, "self-spec finding", fs[0].Integration, c.want)
		if !strings.HasPrefix(fs[0].Signature, c.want+"|") {
			t.Errorf("signature = %q, want it keyed by %q", fs[0].Signature, c.want)
		}
	}
}

// A finding born from an INBOUND call leaves the processor marked as such on
// its internal finding record — the fact that keeps its service-name key off
// the control-plane wire even when the store never holds the call. The marker
// is an attribute of the record, never a field of the finding's JSON, and it
// survives the front→store hop (the OTLP encoding round-trip below).
func TestDerive_InboundFindingRecordCarriesTheMarker(t *testing.T) {
	const marker = "flanj.finding.inbound" // otlpattr.AttrFindingInbound
	self, err := drift.LoadSpecData(specV1(t))
	if err != nil {
		t.Fatal(err)
	}
	p := processorWith(t, map[string][]byte{"api.acme.test": specV1(t)})
	p.selfDoc = self
	in := goldenBatchWith(t, otlpattr.AttrDirection, "server")
	in.ResourceLogs().At(0).Resource().Attributes().PutStr(otlpattr.ResourceServiceName, "orders-svc")
	out := goldenCallBatch(t) // outbound, same drift, against the provider contract
	for _, c := range []struct {
		name string
		ld   plog.Logs
		want bool
	}{{"inbound", in, true}, {"outbound", out, false}} {
		processed, err := p.processLogs(context.Background(), c.ld)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := (&plog.ProtoMarshaler{}).MarshalLogs(processed)
		if err != nil {
			t.Fatal(err)
		}
		hopped, err := (&plog.ProtoUnmarshaler{}).UnmarshalLogs(wire)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		rls := hopped.ResourceLogs()
		for i := 0; i < rls.Len(); i++ {
			recs := rls.At(i).ScopeLogs().At(0).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				if otlpattr.RecordType(lr) != otlpattr.RecordTypeFinding {
					continue
				}
				found++
				v, ok := lr.Attributes().Get(marker)
				if got := ok && v.Bool(); got != c.want {
					t.Errorf("%s finding record: %s = %v (present %v), want %v", c.name, marker, got, ok, c.want)
				}
				if raw, _ := lr.Attributes().Get(otlpattr.AttrFindingJSON); strings.Contains(raw.Str(), "inbound") {
					t.Errorf("%s finding JSON carries the marker: %s", c.name, raw.Str())
				}
			}
		}
		if found != 1 {
			t.Fatalf("%s: finding records = %d, want 1", c.name, found)
		}
	}
}
