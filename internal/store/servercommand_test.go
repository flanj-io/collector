package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// stdioCommand is a stdio MCP server's launch line as the SDK sends it
// (flanj.mcp.server.command): a compact JSON array, capped form included.
const stdioCommand = `["npx","-y","@stripe/mcp@0.2.1","…"]`

func stdioSpec(command string) model.SpecInfo {
	return model.SpecInfo{
		Integration: "stripe-mcp", Role: model.SpecRoleProvider, PeerHost: "stripe-mcp",
		EdgeClass: model.EdgeClassLocalProcess, Format: model.SpecFormatMCP, Source: model.SpecSourceObserved,
		Title: "stripe-mcp", Version: "0.2.1", Endpoints: 1, LoadedAt: "2026-09-18T10:00:00Z",
		ServerCommand: command,
	}
}

const stdioSnapshot = `{"tools":[{"name":"list_charges","inputSchema":{"type":"object"}}]}`

// onlySpec lists the store's contracts and returns the one row asked for.
func onlySpec(t *testing.T, s Store, integration, when string) model.SpecInfo {
	t.Helper()
	infos, err := ListContractsAndCatalogues(s)
	if err != nil {
		t.Fatalf("list %s: %v", when, err)
	}
	for _, si := range infos {
		if si.Integration == integration {
			return si
		}
	}
	t.Fatalf("%s: no %s row in %+v", when, integration, infos)
	return model.SpecInfo{}
}

// TestSpecInfo_ServerCommandRoundTrip: a stdio server's launch line survives
// the observed-contract write, the LIST (the only path the Contracts tab reads)
// and a restart, on both backends. A re-observation rewrites it — the row
// describes the latest observation, like its title and version — without
// moving the snapshot's loaded_at anchor, and a row that never had one reads
// empty.
func TestSpecInfo_ServerCommandRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := PutSpecRecord(s, stdioSpec(stdioCommand), []byte(stdioSnapshot)); err != nil {
			t.Fatalf("put: %v", err)
		}
		if got := onlySpec(t, s, "stripe-mcp", "after put"); got.ServerCommand != stdioCommand {
			t.Fatalf("after put: server_command = %q, want %q", got.ServerCommand, stdioCommand)
		}

		_ = s.Close()
		s = b.reopen(t, 0, 0)
		if got := onlySpec(t, s, "stripe-mcp", "after reopen"); got.ServerCommand != stdioCommand {
			t.Errorf("after reopen: server_command = %q, want %q", got.ServerCommand, stdioCommand)
		}

		// The same tools/list, launched differently (a pinned version bump).
		const bumped = `["npx","-y","@stripe/mcp@0.2.2"]`
		later := stdioSpec(bumped)
		later.LoadedAt = "2026-09-18T11:00:00Z"
		if err := PutSpecRecord(s, later, []byte(stdioSnapshot)); err != nil {
			t.Fatalf("re-observe: %v", err)
		}
		got := onlySpec(t, s, "stripe-mcp", "after a re-observation")
		if got.ServerCommand != bumped {
			t.Errorf("after a re-observation: server_command = %q, want the latest %q", got.ServerCommand, bumped)
		}
		if got.LoadedAt != "2026-09-18T10:00:00Z" {
			t.Errorf("loaded_at = %q: an unchanged document must keep its first stamp", got.LoadedAt)
		}

		// A URL-addressed server never carries one.
		remote := stdioSpec("")
		remote.Integration, remote.PeerHost, remote.EdgeClass = "acme-tools", "mcp.acme.test", model.EdgeClassExternal
		if err := PutSpecRecord(s, remote, []byte(stdioSnapshot)); err != nil {
			t.Fatalf("put remote: %v", err)
		}
		if got := onlySpec(t, s, "acme-tools", "remote row"); got.ServerCommand != "" {
			t.Errorf("a row written without a command reads %q, want empty", got.ServerCommand)
		}
	})
}

// TestSpecInfo_ServerCommandColumnWidensAnOldDatabase: a database created by
// a build before the column opens, lists and writes — the additive widening
// at open (sqlite's specInfoAddedColumns, postgres's ADD COLUMN IF NOT EXISTS)
// is what makes an upgrade a restart and not a failed start.
func TestSpecInfo_ServerCommandColumnWidensAnOldDatabase(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := PutSpecRecord(s, stdioSpec(""), []byte(stdioSnapshot)); err != nil {
			t.Fatalf("put: %v", err)
		}
		_ = s.Close()
		b.rawExec(t, `ALTER TABLE spec_infos DROP COLUMN server_command`)

		s = b.reopen(t, 0, 0)
		if got := onlySpec(t, s, "stripe-mcp", "pre-column row"); got.ServerCommand != "" {
			t.Errorf("pre-column row: server_command = %q, want empty", got.ServerCommand)
		}
		if err := PutSpecRecord(s, stdioSpec(stdioCommand), []byte(stdioSnapshot)); err != nil {
			t.Fatalf("write after widening: %v", err)
		}
		if got := onlySpec(t, s, "stripe-mcp", "after widening"); got.ServerCommand != stdioCommand {
			t.Errorf("after widening: server_command = %q, want %q", got.ServerCommand, stdioCommand)
		}
	})
}

// TestMigrateFromSQLite_CarriesServerCommand: the one-shot sqlite→postgres
// import carries the launch line, and — the trap this guards — a legacy file
// last written BEFORE the column existed still imports. The source is opened
// mode=ro, so nothing widens it first: naming the column unconditionally is
// "no such column: server_command", which aborts the start.
func TestMigrateFromSQLite_CarriesServerCommand(t *testing.T) {
	build := func(t *testing.T, dropColumn bool) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "flanj.db")
		s, err := OpenSQLite(path, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !dropColumn {
			if err := PutSpecRecord(s, stdioSpec(stdioCommand), []byte(stdioSnapshot)); err != nil {
				t.Fatal(err)
			}
		}
		_ = s.Close()
		if dropColumn {
			// A file last written before server_command (2026-09-18) also
			// predates mcp_catalogues (2026-09-19): its stdio snapshot is a
			// spec_infos row, in a spec_infos with no server_command column, and
			// the catalogue table does not exist. Rebuilt to that shape.
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			for _, stmt := range []string{
				`DROP TABLE mcp_catalogues`,
				`ALTER TABLE spec_infos DROP COLUMN server_command`,
				`INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, version, endpoints, loaded_at, doc, source)
				 VALUES ('stripe-mcp', 'provider', 'stripe-mcp', 'local-process', 'mcp', 'stripe-mcp', '0.2.1', 1,
				         '2026-09-18T10:00:00Z', '` + stdioSnapshot + `', 'observed')`,
			} {
				if _, err := raw.Exec(stmt); err != nil {
					t.Fatalf("shape the legacy file: %v", err)
				}
			}
			_ = raw.Close()
		}
		return path
	}

	t.Run("current file", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		if _, err := MigrateFromSQLite(pg, build(t, false)); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if got := onlySpec(t, pg, "stripe-mcp", "migrated"); got.ServerCommand != stdioCommand {
			t.Errorf("migrated row: server_command = %q, want %q", got.ServerCommand, stdioCommand)
		}
	})
	t.Run("legacy file without the column", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		if _, err := MigrateFromSQLite(pg, build(t, true)); err != nil {
			t.Fatalf("migrate a file predating server_command: %v", err)
		}
		got := onlySpec(t, pg, "stripe-mcp", "migrated legacy")
		if got.ServerCommand != "" || got.Title != "stripe-mcp" {
			t.Errorf("migrated legacy row: title=%q server_command=%q, want the row with an EMPTY command",
				got.Title, got.ServerCommand)
		}
	})
}
