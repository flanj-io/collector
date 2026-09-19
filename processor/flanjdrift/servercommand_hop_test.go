package flanjdrift

import (
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// TestServerCommandCrossesTheFrontToStoreHop: in the tiered topology a stdio
// server's launch line is observed on a FRONT, which owns no store — the only
// way it reaches the store pod's Contracts card is inside the spec_info record
// the front emits. Drive it the whole way: a contract_snapshot carrying
// flanj.mcp.server.command → a store-less front's drift processor → the OTLP
// protobuf bytes otlphttp puts on the wire → decoded the way the store pod's
// exporter decodes it (otlpattr.SpecInfoFromRecord → PutSpecInfo) → a real
// store's listing.
func TestServerCommandCrossesTheFrontToStoreHop(t *testing.T) {
	const cmd = `["npx","-y","@stripe/mcp@0.2.1"]`
	front := &driftProcessor{cfg: &Config{}, mcp: drift.NewMCPDetector()} // no store: a front

	ld := plog.NewLogs()
	mcpSnapshotRecord(ld, mcpSnapshotJSON)
	snap := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	snap.Attributes().PutStr(otlpattr.AttrEdgeClass, "local-process")
	snap.Attributes().PutStr(otlpattr.AttrPeerHost, "acme-payments-mcp")
	snap.Attributes().PutStr(otlpattr.AttrMCPServerCommand, cmd)

	out, err := front.processLogs(context.Background(), ld)
	if err != nil {
		t.Fatalf("front processLogs: %v", err)
	}

	// The front → store pod hop is otlphttp: protobuf bytes.
	wire, err := (&plog.ProtoMarshaler{}).MarshalLogs(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	arrived, err := (&plog.ProtoUnmarshaler{}).UnmarshalLogs(wire)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	written := 0
	rls := arrived.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				if otlpattr.RecordType(lr) != otlpattr.RecordTypeSpecInfo {
					continue
				}
				info, raw, err := otlpattr.SpecInfoFromRecord(lr)
				if err != nil {
					t.Fatalf("decode spec_info: %v", err)
				}
				if err := st.PutSpecInfo(info, raw); err != nil {
					t.Fatalf("store pod PutSpecInfo: %v", err)
				}
				written++
			}
		}
	}
	if written != 1 {
		t.Fatalf("spec_info records across the hop = %d, want 1", written)
	}
	infos, err := st.ListSpecInfos()
	if err != nil || len(infos) != 1 {
		t.Fatalf("store pod listing: %v (%d rows)", err, len(infos))
	}
	if infos[0].ServerCommand != cmd || infos[0].EdgeClass != "local-process" {
		t.Errorf("store pod row: edge_class=%q server_command=%q, want local-process %q",
			infos[0].EdgeClass, infos[0].ServerCommand, cmd)
	}
}
