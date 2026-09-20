package otlpattr

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/model"
)

// stdioSnapshotRecord is a contract_snapshot from a stdio (local-process)
// server, the one class flanj.mcp.server.command is sent for.
func stdioSnapshotRecord(command *string) plog.LogRecord {
	lr := plog.NewLogRecord()
	a := lr.Attributes()
	a.PutStr(AttrRecordType, RecordTypeContractSnapshot)
	a.PutStr(AttrTransport, TransportMCP)
	a.PutStr(AttrDirection, "client")
	a.PutStr(AttrPeerHost, "stripe-mcp")
	a.PutStr(AttrEdgeClass, model.EdgeClassLocalProcess)
	a.PutStr(AttrMCPServerName, "stripe-mcp")
	a.PutStr(AttrMCPServerVersion, "0.2.1")
	a.PutStr(AttrMCPContractSnapshot, `{"tools":[{"name":"list_charges","inputSchema":{"type":"object"}}]}`)
	a.PutInt(AttrMCPToolCount, 1)
	if command != nil {
		a.PutStr(AttrMCPServerCommand, *command)
	}
	return lr
}

// TestContractSnapshotCarriesServerCommand: a valid launch line is kept
// VERBATIM — the capped form's trailing "…" and raw non-ASCII included — and
// an absent one stays empty.
func TestContractSnapshotCarriesServerCommand(t *testing.T) {
	for _, raw := range []string{
		`["npx","-y","@stripe/mcp@0.2.1"]`,
		`["uvx","acme-mcp","--profile","café","…"]`,
		`["node"]`,
	} {
		snap, err := ContractSnapshotFromRecord(stdioSnapshotRecord(&raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if snap.ServerCommand != raw {
			t.Errorf("server command = %q, want %q verbatim", snap.ServerCommand, raw)
		}
	}
	snap, err := ContractSnapshotFromRecord(stdioSnapshotRecord(nil))
	if err != nil || snap.ServerCommand != "" {
		t.Errorf("absent attribute: command=%q err=%v, want empty and no error", snap.ServerCommand, err)
	}
}

// TestContractSnapshotDropsAMalformedServerCommand: anything that is not a
// non-empty JSON array of strings is DROPPED — and only the attribute is. The
// record still decodes, and the snapshot, the edge and the server identity
// arrive intact: a display-only field must never cost the contract it rides.
func TestContractSnapshotDropsAMalformedServerCommand(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":              `npx -y @stripe/mcp`,
		"a json string":         `"npx -y @stripe/mcp"`,
		"an object":             `{"command":"npx","args":["-y"]}`,
		"a number element":      `["npx",1]`,
		"a null element":        `["npx",null]`,
		"a nested array":        `["npx",["-y"]]`,
		"null":                  `null`,
		"an empty array":        `[]`,
		"trailing garbage":      `["npx"]x`,
		"two concatenated":      `["npx"]["-y"]`,
		"truncated mid-element": `["npx","-y","@stri`,
	} {
		t.Run(name, func(t *testing.T) {
			snap, err := ContractSnapshotFromRecord(stdioSnapshotRecord(&raw))
			if err != nil {
				t.Fatalf("a malformed server command failed the record: %v", err)
			}
			if snap.ServerCommand != "" {
				t.Errorf("server command = %q, want it dropped", snap.ServerCommand)
			}
			if snap.SnapshotJSON == "" || snap.ServerName != "stripe-mcp" || snap.EdgeClass != model.EdgeClassLocalProcess ||
				snap.Integration != "stripe-mcp" {
				t.Errorf("the rest of the record did not survive: %+v", snap)
			}
		})
	}
}

// TestSpecInfoRecordCarriesServerCommand: the tiered hop's record — what a
// front emits and the store pod's exporter decodes — carries the launch line,
// and a row without one stays without one.
func TestSpecInfoRecordCarriesServerCommand(t *testing.T) {
	const cmd = `["npx","-y","@stripe/mcp@0.2.1"]`
	info := model.SpecInfo{Integration: "stripe-mcp", Role: model.SpecRoleProvider, PeerHost: "stripe-mcp",
		EdgeClass: model.EdgeClassLocalProcess, Format: model.SpecFormatMCP, Source: model.SpecSourceObserved,
		Endpoints: 1, LoadedAt: "2026-09-18T10:00:00Z", ServerCommand: cmd}
	lr := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	if err := SpecInfoToRecord(lr, info, []byte(`{"tools":[]}`)); err != nil {
		t.Fatalf("to record: %v", err)
	}
	got, _, err := SpecInfoFromRecord(lr)
	if err != nil || got.ServerCommand != cmd {
		t.Errorf("server command across the hop = %q err=%v, want %q", got.ServerCommand, err, cmd)
	}
}
