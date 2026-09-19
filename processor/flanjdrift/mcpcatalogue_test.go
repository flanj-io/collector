package flanjdrift

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// The 2026-09-19 split, on the drift processor's side: one host serving BOTH a
// REST API with an uploaded contract and an MCP server with an observed
// tools/list. The listing carries two rows with one integration; the OpenAPI
// cache must load the contract and the MCP detector must seed from the
// catalogue — each asking for its own document by format.

const bothHost = "api.acme.test"

// TestBothKindsOnOneHostReachTheirOwnReader, over the co-located store (single
// pod, and every pod of a shared-postgres deployment). Proved red by making
// storeSpecSource.specDoc ignore the format (every read became a contract
// read): the MCP detector got the OpenAPI document and seeded nothing.
func TestBothKindsOnOneHostReachTheirOwnReader(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	integration := "api-acme-test"
	if _, err := st.PutUploadedSpec(model.SpecInfo{
		Integration: integration, Role: model.SpecRoleProvider, PeerHost: bothHost,
		Format: model.SpecFormatOpenAPI, LoadedAt: "2026-09-19T10:00:00Z",
	}, specV1(t)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMCPCatalogue(model.SpecInfo{
		Integration: integration, Role: model.SpecRoleProvider, PeerHost: bothHost, EdgeClass: model.EdgeClassExternal,
		Format: model.SpecFormatMCP, LoadedAt: "2026-09-19T10:01:00Z",
	}, []byte(mcpSnapshotJSON)); err != nil {
		t.Fatal(err)
	}
	assertBothReaders(t, storeSpecSource{st: st})
}

// TestBothKindsOnOneHostCrossTheTieredChannel: the same over the front's remote
// source against a store pod that serves each document ONLY for its format (the
// current store pod's rule). The front must send the format on every doc
// request. Proved red by dropping `&format=` from remoteSpecSource.specDoc.
func TestBothKindsOnOneHostCrossTheTieredChannel(t *testing.T) {
	openapi := specV1(t)
	var (
		mu      sync.Mutex
		formats []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/contracts":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"contracts":[
				{"integration":"api-acme-test","role":"provider","peer_host":"api.acme.test","format":"openapi","loaded_at":"2026-09-19T10:00:00Z"},
				{"integration":"api-acme-test","role":"provider","peer_host":"api.acme.test","edge_class":"external","format":"mcp","loaded_at":"2026-09-19T10:01:00Z"}]}`))
		case "/internal/contracts/doc":
			f := r.URL.Query().Get("format")
			mu.Lock()
			formats = append(formats, f)
			mu.Unlock()
			switch f {
			case model.SpecFormatOpenAPI:
				_, _ = w.Write(openapi)
			case model.SpecFormatMCP:
				_, _ = w.Write([]byte(mcpSnapshotJSON))
			default:
				http.Error(w, "no such contract", http.StatusNotFound)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	assertBothReaders(t, newRemoteSpecSource(srv.URL, ""))
	mu.Lock()
	defer mu.Unlock()
	if len(formats) != 2 {
		t.Errorf("doc requests carried formats %q, want one openapi and one mcp", formats)
	}
}

func assertBothReaders(t *testing.T, src specSource) {
	t.Helper()
	infos, err := src.listSpecs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cache := newSpecCache()
	if _, errs := cache.reconcile(infos, src); len(errs) != 0 {
		t.Fatalf("spec cache reconcile: %v", errs)
	}
	if doc, ok := cache.lookup(bothHost); !ok || doc == nil || doc.Info == nil || doc.Info.Title == "" {
		t.Fatalf("the OpenAPI cache has no contract for %s", bothHost)
	}
	det := drift.NewMCPDetector()
	seeds := &mcpSeeds{}
	adopted, _, errs := seeds.reconcile(infos, src, det)
	if len(errs) != 0 {
		t.Fatalf("mcp seed reconcile: %v", errs)
	}
	if len(adopted) != 1 || !det.HasBaseline(bothHost, "client") {
		t.Fatalf("the MCP detector was not seeded from the catalogue (adopted %+v)", adopted)
	}
}
