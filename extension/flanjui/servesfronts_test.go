package flanjui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/component"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// contractServerStub is a store extension that answers store.ContractServer.
type contractServerStub struct{ serves bool }

func (c contractServerStub) ServesContracts() bool                     { return c.serves }
func (contractServerStub) Start(context.Context, component.Host) error { return nil }
func (contractServerStub) Shutdown(context.Context) error              { return nil }

var _ store.ContractServer = contractServerStub{}

func (r *testRig) withContractServer(t *testing.T, serves bool) {
	t.Helper()
	r.ext.host = extHost{exts: map[component.ID]component.Component{
		component.MustNewID("flanjstore"): contractServerStub{serves: serves},
	}}
}

// TestHealthReportsWhetherThisPodServesFronts: the Contracts card cannot say
// anything about the 8 MiB document cap without knowing whether this pod is the
// store pod of a tiered deployment.
//
// The cap belongs to that hop and to no other. A single pod's drift processor
// reads the same rows in-process with no cap at all, so an over-cap document
// there is bound and validating — and a card calling it "too large to serve"
// would be warning about something that works, which is the class of claim this
// whole surface exists to stop making.
func TestHealthReportsWhetherThisPodServesFronts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		serves bool
	}{
		{"tiered store pod", true},
		{"single pod", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.withContractServer(t, tc.serves)
			r.ui = httptest.NewServer(r.ext.routes())
			t.Cleanup(r.ui.Close)

			_, out, raw := r.do(t, http.MethodGet, "/api/health", nil)
			if out["serves_fronts"] != tc.serves {
				t.Fatalf("serves_fronts = %v, want %v: %s", out["serves_fronts"], tc.serves, raw)
			}
		})
	}
}

// TestHealthServesFrontsWithNoStoreExtension: a host that carries no store
// extension, or one on an image predating the interface, answers false — the
// pre-tiered default, and the one that claims nothing.
func TestHealthServesFrontsWithNoStoreExtension(t *testing.T) {
	r := newRig(t)
	r.ui = httptest.NewServer(r.ext.routes())
	t.Cleanup(r.ui.Close)
	_, out, raw := r.do(t, http.MethodGet, "/api/health", nil)
	if out["serves_fronts"] != false {
		t.Fatalf("serves_fronts = %v with no store extension, want false: %s", out["serves_fronts"], raw)
	}
}

// TestContractsCarryTheDocumentSize: GET /api/contracts reports how big each
// stored document is, so the card can name the overage instead of leaving the
// operator to guess why an edge with a contract validates nothing.
func TestContractsCarryTheDocumentSize(t *testing.T) {
	r := newRig(t)
	r.start(t)
	doc := []byte(`{"tools":[{"name":"pad","description":"` + strings.Repeat("p", model.MaxContractDocBytes) + `"}]}`)
	if err := r.st.PutSpecInfo(model.SpecInfo{
		Integration: "acme-tools",
		Role:        model.SpecRoleProvider,
		PeerHost:    "mcp.acme.test",
		Format:      model.SpecFormatMCP,
		Source:      model.SpecSourceObserved,
		LoadedAt:    "2026-09-08T10:00:00Z",
	}, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, out, raw := r.do(t, http.MethodGet, "/api/contracts", nil)
	rows, _ := out["contracts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("contracts = %s, want the one seeded row", raw)
	}
	row, _ := rows[0].(map[string]any)
	got, ok := row["doc_bytes"].(float64)
	if !ok {
		t.Fatalf("the row carries no doc_bytes: %s", raw)
	}
	if int(got) != len(doc) {
		t.Errorf("doc_bytes = %d, want %d", int(got), len(doc))
	}
	if int(got) <= model.MaxContractDocBytes {
		t.Errorf("doc_bytes = %d does not read as over the %d cap", int(got), model.MaxContractDocBytes)
	}
}
