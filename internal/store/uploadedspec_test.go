package store

import (
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

func uploadedSpec(integration, host, version, loadedAt string) model.SpecInfo {
	return model.SpecInfo{
		Integration: integration,
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatOpenAPI,
		PeerHost:    host,
		Version:     version,
		LoadedAt:    loadedAt,
		Source:      model.SpecSourceUpload,
	}
}

// TestUploadedSpecFirstUploadHasNoPrevious: a first upload displaces nothing,
// and the caller must be able to tell that apart from a replace so it does not
// render "replaced" with an empty version beside it.
func TestUploadedSpecFirstUploadHasNoPrevious(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		prev, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "1.0.0", "t1"), []byte("doc-v1"))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if prev.Existed {
			t.Errorf("first upload reported a previous contract: %+v", prev)
		}

		raw, _, ok, err := st.GetSpecDoc("acme")
		if err != nil || !ok {
			t.Fatalf("GetSpecDoc: ok=%v err=%v", ok, err)
		}
		if string(raw) != "doc-v1" {
			t.Errorf("stored doc = %q, want doc-v1", raw)
		}
	})
}

// TestUploadedSpecReplaceKeepsExactlyOnePrevious is the N=2 rule: the document
// an upload displaces is returned (so the version diff can run) and kept (so
// the card can read "replaced v1.0.0" and the diff UI can be switched on later
// rather than migrated to). A THIRD upload keeps the second, not the first —
// one previous, no archive.
func TestUploadedSpecReplaceKeepsExactlyOnePrevious(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "1.0.0", "t1"), []byte("doc-v1")); err != nil {
			t.Fatal(err)
		}
		prev, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "2.0.0", "t2"), []byte("doc-v2"))
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if !prev.Existed {
			t.Fatal("a replace reported no previous contract")
		}
		if string(prev.Raw) != "doc-v1" || prev.Version != "1.0.0" || prev.LoadedAt != "t1" {
			t.Errorf("previous = %+v, want the v1 document", prev)
		}

		infos, err := st.ListSpecInfos()
		if err != nil || len(infos) != 1 {
			t.Fatalf("ListSpecInfos = %d rows, err=%v — a replace must not create a second row", len(infos), err)
		}
		if infos[0].Version != "2.0.0" || infos[0].PrevVersion != "1.0.0" {
			t.Errorf("row = %+v, want version 2.0.0 replacing 1.0.0", infos[0])
		}
		if infos[0].Source != model.SpecSourceUpload {
			t.Errorf("source = %q, want %q — the card's provenance word tracks it", infos[0].Source, model.SpecSourceUpload)
		}

		// A third upload rotates again: the second is kept, the first is gone.
		prev, err = st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "3.0.0", "t3"), []byte("doc-v3"))
		if err != nil {
			t.Fatal(err)
		}
		if string(prev.Raw) != "doc-v2" {
			t.Errorf("previous = %q, want doc-v2 — exactly one previous is kept", prev.Raw)
		}
	})
}

// TestUploadedSpecPreviousIsReadableLater: the diff UI is deferred, so the
// stored previous document has no reader yet. Storing it is what keeps that a
// switch rather than a migration, and this is the read that proves it survived.
func TestUploadedSpecPreviousIsReadableLater(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "1.0.0", "t1"), []byte("doc-v1")); err != nil {
			t.Fatal(err)
		}
		reader, ok := st.(interface {
			GetSpecPrevDoc(string) ([]byte, bool, error)
		})
		if !ok {
			t.Fatal("backend cannot read back a previous document")
		}

		if _, found, err := reader.GetSpecPrevDoc("acme"); err != nil || found {
			t.Errorf("a first upload left a previous document: found=%v err=%v", found, err)
		}

		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "2.0.0", "t2"), []byte("doc-v2")); err != nil {
			t.Fatal(err)
		}
		raw, found, err := reader.GetSpecPrevDoc("acme")
		if err != nil || !found {
			t.Fatalf("previous document not readable: found=%v err=%v", found, err)
		}
		if string(raw) != "doc-v1" {
			t.Errorf("previous = %q, want doc-v1", raw)
		}
	})
}

// TestDeleteSpecInfo: Remove ships with upload, because a contract bound to the
// wrong host with no undo is worse than no contract at all. Deleting a contract
// that was never there is not an error — it is the state the caller wanted.
func TestDeleteSpecInfo(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "1.0.0", "t1"), []byte("doc-v1")); err != nil {
			t.Fatal(err)
		}
		existed, err := st.DeleteSpecInfo("acme")
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if !existed {
			t.Error("deleting an existing contract reported it absent")
		}
		if _, _, ok, _ := st.GetSpecDoc("acme"); ok {
			t.Error("the document survived its contract")
		}
		infos, _ := st.ListSpecInfos()
		if len(infos) != 0 {
			t.Errorf("ListSpecInfos = %d rows after delete, want 0", len(infos))
		}

		existed, err = st.DeleteSpecInfo("never-there")
		if err != nil {
			t.Errorf("deleting an absent contract errored: %v", err)
		}
		if existed {
			t.Error("deleting an absent contract reported it present")
		}
	})
}

// TestUploadedSpecSurvivesReopen: contracts are the one thing in this store
// that is NOT a rolling window. An operator who uploaded fifty of them must not
// re-upload after a restart.
func TestUploadedSpecSurvivesReopen(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "1.0.0", "t1"), []byte("doc-v1")); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "2.0.0", "t2"), []byte("doc-v2")); err != nil {
			t.Fatal(err)
		}
		st.Close()

		st = b.reopen(t, 0, 0)
		defer st.Close()

		infos, err := st.ListSpecInfos()
		if err != nil || len(infos) != 1 {
			t.Fatalf("after reopen: %d rows, err=%v", len(infos), err)
		}
		if infos[0].Version != "2.0.0" || infos[0].PrevVersion != "1.0.0" || infos[0].Source != model.SpecSourceUpload {
			t.Errorf("row after reopen = %+v, want v2.0.0 (upload) replacing v1.0.0", infos[0])
		}
		raw, _, ok, _ := st.GetSpecDoc("acme")
		if !ok || string(raw) != "doc-v2" {
			t.Errorf("doc after reopen = %q ok=%v, want doc-v2", raw, ok)
		}
	})
}

// TestPutSpecInfoLeavesUploadProvenanceAlone: the config/self/MCP write path is
// a plain upsert and must not claim an upload's provenance, nor silently drop
// the previous document an upload is keeping.
func TestPutSpecInfoStillWorksAlongsideUploads(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		st := b.open(t, 0, 0)
		defer st.Close()

		if err := st.PutSpecInfo(model.SpecInfo{
			Integration: "self", Role: model.SpecRoleSelf, Format: model.SpecFormatOpenAPI,
			LoadedAt: "t1", Source: model.SpecSourceConfig,
		}, []byte("self-doc")); err != nil {
			t.Fatalf("put self spec: %v", err)
		}
		if _, err := st.PutUploadedSpec(uploadedSpec("acme", "api.acme.test", "1.0.0", "t1"), []byte("doc-v1")); err != nil {
			t.Fatal(err)
		}

		infos, err := st.ListSpecInfos()
		if err != nil || len(infos) != 2 {
			t.Fatalf("ListSpecInfos = %d rows, err=%v, want the self contract and the upload", len(infos), err)
		}
		bySource := map[string]string{}
		for _, si := range infos {
			bySource[si.Integration] = si.Source
		}
		if bySource["self"] != model.SpecSourceConfig {
			t.Errorf("self contract source = %q, want %q", bySource["self"], model.SpecSourceConfig)
		}
		if bySource["acme"] != model.SpecSourceUpload {
			t.Errorf("uploaded contract source = %q, want %q", bySource["acme"], model.SpecSourceUpload)
		}
	})
}
