package flanjstore

import (
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// TestAHeldCatalogueIsNotARejection: on the tiered hop a front forwards its MCP
// catalogue as a spec_info record, and the store pod holds it against a
// contract bound to the same host (store.ErrSpecInfoHeld). That is the store
// working, not a poison record. Surfacing it as an error would drop the batch
// (ErrRejected is permanent) or retry it forever. The test drives consumeLogs
// itself: through the public ConsumeLogs a sending queue accepts the batch and
// returns nil whatever the write does, which is how a first version of this
// test stayed green against the defect.
func TestAHeldCatalogueIsNotARejection(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.PutUploadedSpec(model.SpecInfo{
		Integration: "api-acme-test", Role: model.SpecRoleProvider, Format: model.SpecFormatOpenAPI,
		PeerHost: "api.acme.test", Source: model.SpecSourceUpload, LoadedAt: "t1",
	}, []byte("openapi-doc")); err != nil {
		t.Fatal(err)
	}

	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	if err := otlpattr.SpecInfoToRecord(lr, model.SpecInfo{
		Integration: "api-acme-test", Role: model.SpecRoleProvider, Format: model.SpecFormatMCP,
		PeerHost: "api.acme.test", Source: model.SpecSourceObserved, LoadedAt: "t2",
	}, []byte("tools-doc")); err != nil {
		t.Fatal(err)
	}

	e := &storeExporter{logger: zap.NewNop(), st: st}
	if err := e.consumeLogs(context.Background(), ld); err != nil {
		t.Fatalf("consumeLogs = %v, want nil: a held catalogue is an expected outcome", err)
	}
	raw, _, _, _ := st.GetSpecDoc("api-acme-test")
	if string(raw) != "openapi-doc" {
		t.Errorf("stored doc = %q, want the uploaded contract", raw)
	}
}
