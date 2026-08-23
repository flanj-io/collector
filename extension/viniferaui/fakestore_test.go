package viniferaui

import (
	"sync"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/store"
)

// fakeStore is an in-memory store.Store for handler tests: findings, calls and
// the settings KV behave; everything else is inert.
type fakeStore struct {
	mu       sync.Mutex
	calls    map[string]model.RedactedCall
	findings map[string]model.Finding
	settings map[string]string
	promoted []string
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
func (f *fakeStore) ListEdges(bool) ([]model.Edge, error)               { return nil, nil }
func (f *fakeStore) EdgeCallCountsSince(string) (map[string]int, error) { return map[string]int{}, nil }
func (f *fakeStore) PutSpecInfo(model.SpecInfo, []byte) error           { return nil }
func (f *fakeStore) ListSpecInfos() ([]model.SpecInfo, error)           { return nil, nil }
func (f *fakeStore) GetSpecDoc(string) ([]byte, string, bool, error)    { return nil, "", false, nil }
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
