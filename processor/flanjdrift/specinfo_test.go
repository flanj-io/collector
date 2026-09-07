package flanjdrift

import (
	"testing"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
)

// TestSpecInfoFor_NamesItsProvenance: the self contract's metadata says where
// it came from, explicitly. The store's column default is "config" too, which
// is exactly how the observed MCP path's unset Source went unnoticed
// (2026-09-07) — no writer gets to lean on that accident.
func TestSpecInfoFor_NamesItsProvenance(t *testing.T) {
	doc, err := drift.LoadSpecFile("../../contracts/spec-v1.yaml")
	if err != nil {
		t.Fatalf("load spec-v1: %v", err)
	}
	info := specInfoFor(doc, model.SpecRoleSelf, "self", "")
	if info.Source != model.SpecSourceConfig {
		t.Errorf("source = %q, want %q", info.Source, model.SpecSourceConfig)
	}
	if info.Role != model.SpecRoleSelf || info.Format != model.SpecFormatOpenAPI ||
		info.Integration != "self" || info.Endpoints == 0 || info.LoadedAt == "" {
		t.Errorf("spec info = %+v", info)
	}
}
