package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// The 2026-09-19 split (ruling: the collector files REST contracts only; an MCP
// server's tools/list arrives with the traffic and is not a filed contract).
//
// THE BUG: spec_infos held both, keyed by integration alone, and both kinds'
// integration is the host-derived slug — so an MCP endpoint at api.acme.test/mcp
// and a REST contract uploaded for api.acme.test were ONE row. Every observed
// snapshot upserted the whole row and the uploaded contract was gone.

const (
	coexistHost        = "api.acme.test"
	coexistIntegration = "api-acme-test"
)

var (
	coexistOpenAPI = []byte("openapi: 3.0.0\ninfo:\n  title: Acme Payments API\n  version: 1.0.0\npaths: {}\n")
	coexistToolsV1 = []byte(`{"tools":[{"name":"charge","inputSchema":{"type":"object"}}]}`)
	coexistToolsV2 = []byte(`{"tools":[{"name":"charge","inputSchema":{"type":"object"}},{"name":"refund","inputSchema":{"type":"object"}}]}`)
)

func coexistUpload() model.SpecInfo {
	return model.SpecInfo{
		Integration: coexistIntegration, Role: model.SpecRoleProvider, PeerHost: coexistHost,
		Format: model.SpecFormatOpenAPI, Source: model.SpecSourceUpload, EdgeClass: model.EdgeClassExternal,
		Title: "Acme Payments API", Version: "1.0.0", Endpoints: 3, LoadedAt: "2026-09-19T10:00:00Z",
	}
}

func coexistSnapshot(loadedAt string) model.SpecInfo {
	return model.SpecInfo{
		Integration: coexistIntegration, Role: model.SpecRoleProvider, PeerHost: coexistHost,
		Format: model.SpecFormatMCP, Source: model.SpecSourceObserved, EdgeClass: model.EdgeClassExternal,
		Title: "acme-tools-mcp", Version: "0.3.0", Endpoints: 1, LoadedAt: loadedAt,
	}
}

// assertCoexist checks both rows are whole: the REST contract with its own
// document in the contracts listing, the MCP catalogue with its own document in
// the catalogue listing, and neither leaking into the other.
func assertCoexist(t *testing.T, s Store, wantTools []byte) {
	t.Helper()
	infos, err := s.ListSpecInfos()
	if err != nil {
		t.Fatalf("list contracts: %v", err)
	}
	if len(infos) != 1 || infos[0].Format != model.SpecFormatOpenAPI || infos[0].Source != model.SpecSourceUpload ||
		infos[0].Title != "Acme Payments API" {
		t.Fatalf("contracts listing = %+v, want exactly the uploaded REST contract", infos)
	}
	raw, format, ok, err := s.GetSpecDoc(coexistIntegration)
	if err != nil || !ok || format != model.SpecFormatOpenAPI || string(raw) != string(coexistOpenAPI) {
		t.Fatalf("REST document = %q (format %q ok=%v err=%v), want the uploaded OpenAPI document", raw, format, ok, err)
	}
	cats, err := s.ListMCPCatalogues()
	if err != nil {
		t.Fatalf("list catalogues: %v", err)
	}
	if len(cats) != 1 || cats[0].Format != model.SpecFormatMCP || cats[0].Source != model.SpecSourceObserved ||
		cats[0].Title != "acme-tools-mcp" || cats[0].PeerHost != coexistHost {
		t.Fatalf("catalogue listing = %+v, want exactly the observed MCP catalogue", cats)
	}
	if cats[0].DocBytes != len(wantTools) {
		t.Errorf("catalogue DocBytes = %d, want %d (measured at list time, like a contract)", cats[0].DocBytes, len(wantTools))
	}
	doc, ok, err := s.GetMCPCatalogueDoc(coexistIntegration)
	if err != nil || !ok || string(doc) != string(wantTools) {
		t.Fatalf("MCP document = %q (ok=%v err=%v), want %q", doc, ok, err, wantTools)
	}
	both, err := ListContractsAndCatalogues(s)
	if err != nil || len(both) != 2 || both[0].Format != model.SpecFormatOpenAPI || both[1].Format != model.SpecFormatMCP {
		t.Fatalf("combined listing = %+v (err %v), want the contract then the catalogue", both, err)
	}
}

// TestRESTContractAndMCPCatalogueCoexist: one host, both kinds, every order.
// Proved red by routing PutMCPCatalogue's upsert back into spec_infos (the
// pre-split shared row): "upload then snapshot" lost the contract.
func TestRESTContractAndMCPCatalogueCoexist(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		t.Run("upload then snapshot", func(t *testing.T) {
			s := b.open(t, 0, 0)
			if _, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI); err != nil {
				t.Fatalf("upload: %v", err)
			}
			if err := s.PutMCPCatalogue(coexistSnapshot("2026-09-19T10:05:00Z"), coexistToolsV1); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			assertCoexist(t, s, coexistToolsV1)
			// Every later snapshot is another upsert — the one that used to
			// overwrite the contract on every tools/list.
			if err := s.PutMCPCatalogue(coexistSnapshot("2026-09-19T10:10:00Z"), coexistToolsV2); err != nil {
				t.Fatalf("second snapshot: %v", err)
			}
			assertCoexist(t, s, coexistToolsV2)
		})
		t.Run("snapshot then upload", func(t *testing.T) {
			s := b.open(t, 0, 0)
			if err := s.PutMCPCatalogue(coexistSnapshot("2026-09-19T10:05:00Z"), coexistToolsV1); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			prev, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI)
			if err != nil {
				t.Fatalf("upload: %v", err)
			}
			// The upload displaced nothing: the catalogue is not a contract.
			if prev.Existed {
				t.Errorf("the upload reported replacing %q — it rotated the MCP catalogue into prev_doc", prev.Raw)
			}
			assertCoexist(t, s, coexistToolsV1)
		})
		t.Run("remove the contract keeps the catalogue", func(t *testing.T) {
			s := b.open(t, 0, 0)
			if _, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI); err != nil {
				t.Fatalf("upload: %v", err)
			}
			if err := s.PutMCPCatalogue(coexistSnapshot("2026-09-19T10:05:00Z"), coexistToolsV1); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if existed, err := s.DeleteSpecInfo(coexistIntegration); err != nil || !existed {
				t.Fatalf("delete contract: existed=%v err=%v", existed, err)
			}
			if _, ok, _ := s.GetMCPCatalogueDoc(coexistIntegration); !ok {
				t.Fatal("removing the REST contract removed the host's MCP catalogue too")
			}
		})
		t.Run("survives a restart", func(t *testing.T) {
			s := b.open(t, 0, 0)
			if err := s.PutMCPCatalogue(coexistSnapshot("2026-09-19T10:05:00Z"), coexistToolsV1); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if _, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI); err != nil {
				t.Fatalf("upload: %v", err)
			}
			_ = s.Close()
			assertCoexist(t, b.reopen(t, 0, 0), coexistToolsV1)
		})
	})
}

// TestContractWritersRefuseAnMCPRow is the guard that keeps spec_infos free of
// MCP rows: a writer that still sends one through the contract path is refused
// (ErrRejected, so the exporter drops rather than retries it), and nothing is
// written. PutSpecRecord is the dispatch that sends it to the right table.
func TestContractWritersRefuseAnMCPRow(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := s.PutSpecInfo(coexistSnapshot("t0"), coexistToolsV1); !errors.Is(err, ErrRejected) {
			t.Errorf("PutSpecInfo(mcp) = %v, want ErrRejected", err)
		}
		if _, err := s.PutUploadedSpec(coexistSnapshot("t0"), coexistToolsV1); !errors.Is(err, ErrRejected) {
			t.Errorf("PutUploadedSpec(mcp) = %v, want ErrRejected", err)
		}
		if infos, _ := s.ListSpecInfos(); len(infos) != 0 {
			t.Fatalf("a refused MCP row landed in spec_infos: %+v", infos)
		}
		if err := PutSpecRecord(s, coexistSnapshot("t0"), coexistToolsV1); err != nil {
			t.Fatalf("PutSpecRecord(mcp): %v", err)
		}
		if cats, _ := s.ListMCPCatalogues(); len(cats) != 1 {
			t.Fatalf("PutSpecRecord did not file the MCP row as a catalogue: %+v", cats)
		}
	})
}

// TestOpenMovesMCPRowsOutOfSpecInfos: a database written before the split keeps
// its observed MCP snapshots in spec_infos. Opening it moves them — document,
// metadata, the 2026-09-07 source repair — into mcp_catalogues, leaves the
// contracts where they are, and a second open changes nothing. Proved red by
// skipping moveMCPRowsOutOfSpecInfos at open.
func TestOpenMovesMCPRowsOutOfSpecInfos(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if _, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI); err != nil {
			t.Fatalf("upload: %v", err)
		}
		_ = s.Close()
		// What an older collector left: two observed snapshots in spec_infos,
		// one stored with the pre-2026-09-07 'config' default, one stdio row
		// with its launch line.
		b.rawExec(t, `INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, version, endpoints, loaded_at, doc, source)
			VALUES ('mcp-acme-test', 'provider', 'mcp.acme.test', 'external', 'mcp', 'acme-tools-mcp', '0.3.0', 1, '2026-09-18T09:00:00Z', '{"tools":[]}', 'config')`)
		b.rawExec(t, `INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, endpoints, loaded_at, doc, source, server_command)
			VALUES ('acme-stdio', 'provider', 'acme-stdio-mcp', 'local-process', 'mcp', 'acme-stdio-mcp', 1, '2026-09-18T09:00:00Z', '{"tools":[{"name":"x"}]}', 'observed', '["npx","acme"]')`)

		check := func(s Store) {
			t.Helper()
			infos, err := s.ListSpecInfos()
			if err != nil || len(infos) != 1 || infos[0].Integration != coexistIntegration {
				t.Fatalf("contracts after the move = %+v (err %v), want only the uploaded REST contract", infos, err)
			}
			cats, err := s.ListMCPCatalogues()
			if err != nil || len(cats) != 2 {
				t.Fatalf("catalogues after the move = %+v (err %v), want the two snapshots", cats, err)
			}
			byID := map[string]model.SpecInfo{}
			for _, c := range cats {
				byID[c.Integration] = c
			}
			remote := byID["mcp-acme-test"]
			if remote.Source != model.SpecSourceObserved || remote.Title != "acme-tools-mcp" || remote.Version != "0.3.0" ||
				remote.LoadedAt != "2026-09-18T09:00:00Z" || remote.PeerHost != "mcp.acme.test" || remote.EdgeClass != "external" {
				t.Errorf("moved remote row = %+v, want its metadata intact and the source repaired to observed", remote)
			}
			if stdio := byID["acme-stdio"]; stdio.ServerCommand != `["npx","acme"]` || stdio.EdgeClass != model.EdgeClassLocalProcess {
				t.Errorf("moved stdio row = %+v, want its launch line and edge class", stdio)
			}
			if doc, ok, _ := s.GetMCPCatalogueDoc("acme-stdio"); !ok || string(doc) != `{"tools":[{"name":"x"}]}` {
				t.Errorf("moved stdio document = %q ok=%v", doc, ok)
			}
		}
		s = b.reopen(t, 0, 0)
		check(s)
		_ = s.Close()
		check(b.reopen(t, 0, 0)) // idempotent: the second open moves nothing
	})
}

// TestOpenMoveKeepsTheNewerCatalogue: a rollback to an image from before the
// split writes snapshots into spec_infos again while mcp_catalogues still holds
// what this image wrote. The next open must keep whichever is newer, and never
// leave the stale copy behind in spec_infos.
func TestOpenMoveKeepsTheNewerCatalogue(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		if err := s.PutMCPCatalogue(model.SpecInfo{Integration: "old-wins", PeerHost: "a.test", Format: model.SpecFormatMCP, LoadedAt: "2026-09-19T12:00:00Z"}, []byte(`{"tools":["current"]}`)); err != nil {
			t.Fatal(err)
		}
		if err := s.PutMCPCatalogue(model.SpecInfo{Integration: "new-wins", PeerHost: "b.test", Format: model.SpecFormatMCP, LoadedAt: "2026-09-19T08:00:00Z"}, []byte(`{"tools":["stale"]}`)); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		b.rawExec(t, `INSERT INTO spec_infos (integration, role, peer_host, format, endpoints, loaded_at, doc, source) VALUES
			('old-wins', 'provider', 'a.test', 'mcp', 0, '2026-09-19T09:00:00Z', '{"tools":["rolled-back-older"]}', 'observed'),
			('new-wins', 'provider', 'b.test', 'mcp', 0, '2026-09-19T11:00:00Z', '{"tools":["rolled-back-newer"]}', 'observed')`)
		s = b.reopen(t, 0, 0)
		if doc, _, _ := s.GetMCPCatalogueDoc("old-wins"); string(doc) != `{"tools":["current"]}` {
			t.Errorf("old-wins = %s, want the newer catalogue kept", doc)
		}
		if doc, _, _ := s.GetMCPCatalogueDoc("new-wins"); string(doc) != `{"tools":["rolled-back-newer"]}` {
			t.Errorf("new-wins = %s, want the newer (rolled-back) snapshot adopted", doc)
		}
		if infos, _ := s.ListSpecInfos(); len(infos) != 0 {
			t.Errorf("mcp rows left in spec_infos after the move: %+v", infos)
		}
	})
}

// TestMigrateFromSQLite_CarriesMCPCatalogues: the sqlite → postgres import
// carries both kinds from both shapes of legacy file — a file with the
// catalogue table (a REST contract and an MCP catalogue for one host), and a
// file from before it, whose snapshots are still spec_infos rows and which has
// no mcp_catalogues table at all. Neither lands an MCP row in postgres'
// spec_infos, and neither aborts on the missing table. Proved red twice: by
// importing legacy mcp rows into spec_infos, and by dropping the table probe.
func TestMigrateFromSQLite_CarriesMCPCatalogues(t *testing.T) {
	t.Run("file with the catalogue table", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		path := filepath.Join(t.TempDir(), "flanj.db")
		s, err := OpenSQLite(path, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI); err != nil {
			t.Fatal(err)
		}
		if err := s.PutMCPCatalogue(coexistSnapshot("2026-09-19T10:05:00Z"), coexistToolsV1); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		sum, err := MigrateFromSQLite(pg, path)
		if err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if sum.Contracts != 1 || sum.MCPCatalogues != 1 {
			t.Errorf("summary = %+v, want 1 contract and 1 catalogue", sum)
		}
		assertCoexist(t, pg, coexistToolsV1)
	})
	t.Run("file from before the catalogue table", func(t *testing.T) {
		pg := pgPods(t, 1, 0, 0)[0]
		path := filepath.Join(t.TempDir(), "flanj.db")
		s, err := OpenSQLite(path, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutUploadedSpec(coexistUpload(), coexistOpenAPI); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		b := &testBackend{name: "sqlite", path: path}
		b.rawExec(t, `DROP TABLE mcp_catalogues`)
		b.rawExec(t, `INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, version, endpoints, loaded_at, doc, source)
			VALUES ('mcp-acme-test', 'provider', 'mcp.acme.test', 'external', 'mcp', 'acme-tools-mcp', '0.3.0', 1, '2026-09-18T09:00:00Z', '{"tools":[]}', 'config')`)
		sum, err := MigrateFromSQLite(pg, path)
		if err != nil {
			t.Fatalf("migrate a file without mcp_catalogues: %v", err)
		}
		if sum.Contracts != 1 || sum.MCPCatalogues != 1 {
			t.Errorf("summary = %+v, want 1 contract and 1 catalogue", sum)
		}
		infos, _ := pg.ListSpecInfos()
		if len(infos) != 1 || infos[0].Format != model.SpecFormatOpenAPI {
			t.Fatalf("postgres contracts = %+v, want only the REST contract", infos)
		}
		cats, _ := pg.ListMCPCatalogues()
		if len(cats) != 1 || cats[0].Integration != "mcp-acme-test" || cats[0].Source != model.SpecSourceObserved {
			t.Fatalf("postgres catalogues = %+v, want the legacy snapshot, filed as observed", cats)
		}
		if doc, ok, _ := pg.GetMCPCatalogueDoc("mcp-acme-test"); !ok || string(doc) != `{"tools":[]}` {
			t.Errorf("imported catalogue document = %q ok=%v", doc, ok)
		}
	})
}
