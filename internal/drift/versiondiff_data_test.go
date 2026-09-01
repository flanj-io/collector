package drift

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectVersionDiffDataMatchesThePathForm: the upload-replace path must
// produce exactly the findings the config path used to. `spec_v2_path` is gone
// (CONTRACTS §8), so this byte-based entry point is the only remaining producer
// of the version-diff finding — if it diverged, a shipped detector would have
// quietly changed behaviour on the way to a UI feature.
func TestDetectVersionDiffDataMatchesThePathForm(t *testing.T) {
	dir := contractsDir()
	v1Path := filepath.Join(dir, "spec-v1.yaml")
	v2Path := filepath.Join(dir, "spec-v2.yaml")

	fromPaths, err := DetectVersionDiff(v1Path, v2Path, "acme-payments")
	if err != nil {
		t.Fatalf("path form: %v", err)
	}
	if len(fromPaths) == 0 {
		t.Fatal("the fixtures produce no version-diff findings — this test proves nothing")
	}

	v1, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile(v2Path)
	if err != nil {
		t.Fatal(err)
	}
	fromData, err := DetectVersionDiffData(v1, v2, "acme-payments")
	if err != nil {
		t.Fatalf("data form: %v", err)
	}

	if len(fromData) != len(fromPaths) {
		t.Fatalf("data form produced %d findings, path form %d", len(fromData), len(fromPaths))
	}
	// Signature is the dedup key, so matching signatures is what guarantees the
	// two forms collapse into the same findings in the store.
	want := map[string]bool{}
	for _, f := range fromPaths {
		want[f.Signature] = true
	}
	for _, f := range fromData {
		if !want[f.Signature] {
			t.Errorf("data form produced an unmatched finding: %s (%s)", f.Signature, f.Rule)
		}
		if f.Severity != "breaking" {
			t.Errorf("finding %s severity = %q, want breaking", f.Rule, f.Severity)
		}
		if f.SourceCallID != nil {
			t.Errorf("finding %s has a source call — a version diff is call-less by construction", f.Rule)
		}
	}
}

// TestDetectVersionDiffDataReadsJSON: uploads accept JSON or YAML, so the diff
// must too. A YAML parser reads JSON, which is why both are written with one
// extension.
func TestDetectVersionDiffDataReadsJSON(t *testing.T) {
	v1 := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1.0.0"},"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	v2 := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"2.0.0"},"paths":{}}`)

	findings, err := DetectVersionDiffData(v1, v2, "acme")
	if err != nil {
		t.Fatalf("json documents: %v", err)
	}
	if len(findings) == 0 {
		t.Error("removing every path produced no breaking finding")
	}
}
