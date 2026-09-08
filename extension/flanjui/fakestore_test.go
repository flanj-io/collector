package flanjui

import (
	"sync"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// fakeStore is an in-memory store.Store for handler tests: findings, calls and
// the settings KV behave; everything else is inert.
type fakeStore struct {
	mu       sync.Mutex
	calls    map[string]model.RedactedCall
	findings map[string]model.Finding
	settings map[string]string
	promoted []string
	// edges + specInfos back the v1p1 naming surface: ListEdges serves the
	// seeded rows (externalOnly filters on class) and ListSpecInfos serves the
	// seeded spec rows (config→edge linkage for the boot migration).
	edges     []model.Edge
	specInfos []model.SpecInfo
	specDocs  map[string][]byte
	// afterPut, when set, runs (unlocked) right after a PutSetting write —
	// tests use it to simulate a concurrent writer clobbering the key.
	afterPut func(key, value string)
}

var _ store.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		calls:    map[string]model.RedactedCall{},
		findings: map[string]model.Finding{},
		settings: map[string]string{},
		specDocs: map[string][]byte{},
	}
}

func (f *fakeStore) InsertCall(c model.RedactedCall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[c.ID] = c
	return nil
}
func (f *fakeStore) InsertFinding(x model.Finding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings[x.ID] = x
	return nil
}
func (f *fakeStore) MarkPromoted(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promoted = append(f.promoted, id)
	return nil
}
func (f *fakeStore) GetCall(id string) (model.RedactedCall, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	return c, ok, nil
}
func (f *fakeStore) GetFinding(id string) (model.Finding, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.findings[id]
	return x, ok, nil
}
func (f *fakeStore) ListCalls(limit int) ([]model.RedactedCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.RedactedCall, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c)
	}
	return out, nil
}
func (f *fakeStore) ListFindings(limit int) ([]model.Finding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Finding, 0, len(f.findings))
	for _, x := range f.findings {
		out = append(out, x)
	}
	return out, nil
}
func (f *fakeStore) CallPeerHosts(ids []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, id := range ids {
		// Absent calls and calls with no host stay ABSENT from the map — the
		// real backends answer that way and the read API's fallback depends on
		// it.
		if c, ok := f.calls[id]; ok && c.PeerHost != "" {
			out[id] = c.PeerHost
		}
	}
	return out, nil
}
func (f *fakeStore) ListEdges(externalOnly bool) ([]model.Edge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Edge, 0, len(f.edges))
	for _, e := range f.edges {
		if externalOnly && e.Class != "external" {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
func (f *fakeStore) EdgeCallCountsSince(string) (map[string]int, error) { return map[string]int{}, nil }
func (f *fakeStore) PutSpecInfo(si model.SpecInfo, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.specDocs == nil {
		f.specDocs = map[string][]byte{}
	}
	if len(raw) > 0 {
		f.specDocs[si.Integration] = raw
	}
	f.specInfos = append(f.specInfos, si)
	return nil
}

// ListSpecInfos mirrors the real backends, which MEASURE the stored document at
// list time (store.base.ListSpecInfos). A fake that reported no size would make
// every over-cap row here look like an ordinary one.
func (f *fakeStore) ListSpecInfos() ([]model.SpecInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]model.SpecInfo(nil), f.specInfos...)
	for i := range out {
		out[i].DocBytes = len(f.specDocs[out[i].Integration])
	}
	return out, nil
}
func (f *fakeStore) GetSpecDoc(integration string) ([]byte, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc, ok := f.specDocs[integration]
	if !ok {
		return nil, "", false, nil
	}
	return doc, model.SpecFormatOpenAPI, true, nil
}

// PutUploadedSpec mirrors the real backends' N=2 rotation: the document an
// upload displaces is returned and kept as the single previous.
func (f *fakeStore) PutUploadedSpec(si model.SpecInfo, raw []byte) (store.UploadedSpecPrevious, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.specDocs == nil {
		f.specDocs = map[string][]byte{}
	}
	var prev store.UploadedSpecPrevious
	for i, cur := range f.specInfos {
		if cur.Integration != si.Integration {
			continue
		}
		prev = store.UploadedSpecPrevious{
			Existed:  true,
			Raw:      f.specDocs[si.Integration],
			Version:  cur.Version,
			LoadedAt: cur.LoadedAt,
		}
		si.PrevVersion = cur.Version
		si.PrevLoadedAt = cur.LoadedAt
		f.specInfos[i] = si
		f.specDocs[si.Integration] = raw
		return prev, nil
	}
	f.specInfos = append(f.specInfos, si)
	f.specDocs[si.Integration] = raw
	return prev, nil
}

func (f *fakeStore) DeleteSpecInfo(integration string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, cur := range f.specInfos {
		if cur.Integration != integration {
			continue
		}
		f.specInfos = append(f.specInfos[:i], f.specInfos[i+1:]...)
		delete(f.specDocs, integration)
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) Stats() (int, int64, error)                         { return len(f.calls), 0, nil }
func (f *fakeStore) Counts() (int, int, error)                          { return len(f.calls), len(f.findings), nil }
func (f *fakeStore) GetSetting(key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.settings[key]
	return v, ok, nil
}
func (f *fakeStore) PutSetting(key, value string) error {
	f.mu.Lock()
	f.settings[key] = value
	f.mu.Unlock()
	if f.afterPut != nil {
		f.afterPut(key, value)
	}
	return nil
}
func (f *fakeStore) Close() error { return nil }

// settingsSnapshot is a copy of the whole KV — tests compare two of them to
// prove a read path wrote nothing.
func (f *fakeStore) settingsSnapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.settings))
	for k, v := range f.settings {
		out[k] = v
	}
	return out
}
