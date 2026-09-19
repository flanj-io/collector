package store

import (
	"errors"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// observedMCP is an MCP server's self-delivered tools/list, as the drift
// processor persists it: the id is derived from the host, like an uploaded
// contract's, so a server at api.acme.test/mcp lands on the same id as the
// REST contract bound to api.acme.test.
func observedMCP(integration, host, loadedAt string) model.SpecInfo {
	return model.SpecInfo{
		Integration: integration,
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatMCP,
		PeerHost:    host,
		EdgeClass:   model.EdgeClassExternal,
		Source:      model.SpecSourceObserved,
		LoadedAt:    loadedAt,
	}
}

// TestAnMCPCatalogueNeverOverwritesABoundContract: an uploaded REST contract and
// an MCP server on the same host share one host-derived id. Before the hold,
// every tools/list upserted the whole row, so the OpenAPI document, its format
// and its provenance were replaced by a tool list, and that host's REST calls
// stopped being validated. The row must stay the contract someone bound, and
// the writer must be told why nothing changed.
func TestAnMCPCatalogueNeverOverwritesABoundContract(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if _, err := st.PutUploadedSpec(uploadedSpec("api-acme-test", "api.acme.test", "1.0.0", "t1"), []byte("openapi-doc")); err != nil {
			t.Fatalf("upload: %v", err)
		}
		err := st.PutSpecInfo(observedMCP("api-acme-test", "api.acme.test", "t2"), []byte("tools-doc"))
		if !errors.Is(err, ErrSpecInfoHeld) {
			t.Fatalf("PutSpecInfo(mcp over uploaded) = %v, want ErrSpecInfoHeld", err)
		}
		if errors.Is(err, ErrRejected) {
			t.Error("a held row is an expected outcome, not a rejection: a store pod would drop the rest of the batch")
		}

		raw, _, ok, gerr := st.GetSpecDoc("api-acme-test")
		if gerr != nil || !ok {
			t.Fatalf("GetSpecDoc: ok=%v err=%v", ok, gerr)
		}
		if string(raw) != "openapi-doc" {
			t.Errorf("stored doc = %q, want the uploaded contract", raw)
		}
		infos, _ := st.ListSpecInfos()
		if len(infos) != 1 || infos[0].Format != model.SpecFormatOpenAPI || infos[0].Source != model.SpecSourceUpload {
			t.Errorf("row = %+v, want the uploaded OpenAPI contract untouched", infos)
		}
	})
}

// TestAnMCPCatalogueStillUpdatesItsOwnRow: the hold is only across formats. A
// server's newer tools/list must keep replacing its own row, or catalogue
// changes would stop reaching the contract card.
func TestAnMCPCatalogueStillUpdatesItsOwnRow(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if err := st.PutSpecInfo(observedMCP("mcp-acme-test", "mcp.acme.test", "t1"), []byte("tools-v1")); err != nil {
			t.Fatalf("first snapshot: %v", err)
		}
		if err := st.PutSpecInfo(observedMCP("mcp-acme-test", "mcp.acme.test", "t2"), []byte("tools-v2")); err != nil {
			t.Fatalf("newer snapshot: %v", err)
		}
		raw, _, _, _ := st.GetSpecDoc("mcp-acme-test")
		if string(raw) != "tools-v2" {
			t.Errorf("stored doc = %q, want the newer catalogue", raw)
		}
	})
}

// TestAnUploadTakesOverAnObservedMCPRowAsAFirstBind: the other order. The MCP
// server was seen first, so its catalogue holds the id, and an observed row
// cannot be removed. The upload must be able to take the id, and as a FIRST
// bind: the tool list is not a previous version of this contract, and keeping
// it as prev_doc would feed a tools/list into the OpenAPI version diff.
func TestAnUploadTakesOverAnObservedMCPRowAsAFirstBind(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if err := st.PutSpecInfo(observedMCP("api-acme-test", "api.acme.test", "t1"), []byte("tools-doc")); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		prev, err := st.PutUploadedSpec(uploadedSpec("api-acme-test", "api.acme.test", "1.0.0", "t2"), []byte("openapi-doc"))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if prev.Existed || len(prev.Raw) != 0 {
			t.Errorf("the MCP catalogue was kept as the contract's previous version: %+v", prev)
		}
		infos, _ := st.ListSpecInfos()
		if len(infos) != 1 || infos[0].Format != model.SpecFormatOpenAPI || infos[0].PrevVersion != "" {
			t.Errorf("row = %+v, want the OpenAPI contract with no previous version", infos)
		}
		// And from here on the server's catalogue is held against it.
		if err := st.PutSpecInfo(observedMCP("api-acme-test", "api.acme.test", "t3"), []byte("tools-doc-2")); !errors.Is(err, ErrSpecInfoHeld) {
			t.Errorf("a later snapshot = %v, want ErrSpecInfoHeld", err)
		}
	})
}
