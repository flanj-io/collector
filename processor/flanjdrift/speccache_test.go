package flanjdrift

import (
	"errors"
	"os"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// specV1 is the vendored contract fixture — the same document the golden
// live-vs-spec case validates against.
func specV1(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../contracts/spec-v1.yaml")
	if err != nil {
		t.Fatalf("read spec-v1: %v", err)
	}
	return b
}

// specV2 is the vendored SUCCESSOR contract: it declares `amount` as a string,
// which is what the golden call actually carries. So the same call drifts
// against v1 and conforms against v2 — the verdict says which document is live.
func specV2(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../contracts/spec-v2.yaml")
	if err != nil {
		t.Fatalf("read spec-v2: %v", err)
	}
	return b
}

// fakeSource is a specSource whose rows the test controls, counting document
// fetches so the "only re-download what changed" contract is testable rather
// than assumed.
type fakeSource struct {
	infos    []model.SpecInfo
	docs     map[string][]byte
	fetches  map[string]int
	listErr  error
	docErrOn string
}

func newFakeSource() *fakeSource {
	return &fakeSource{docs: map[string][]byte{}, fetches: map[string]int{}}
}

func (f *fakeSource) listSpecs() ([]model.SpecInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.infos, nil
}

func (f *fakeSource) specDoc(integration string) ([]byte, error) {
	f.fetches[integration]++
	if f.docErrOn == integration {
		return nil, errors.New("boom")
	}
	return f.docs[integration], nil
}

// put registers one provider contract bound to a host.
func (f *fakeSource) put(integration, host, loadedAt string, doc []byte) {
	f.infos = append(f.infos, model.SpecInfo{
		Integration: integration,
		Role:        model.SpecRoleProvider,
		Format:      model.SpecFormatOpenAPI,
		PeerHost:    host,
		LoadedAt:    loadedAt,
	})
	f.docs[integration] = doc
}

// TestSpecCacheBindsByHost: an uploaded contract is looked up by the host it was
// bound to, and only that host. Binding is mandatory at upload precisely so this
// map is the whole lookup — an unbound contract would validate nothing forever
// while its card claimed otherwise.
func TestSpecCacheBindsByHost(t *testing.T) {
	src := newFakeSource()
	src.put("api-acme-test", "api.acme.test", "2026-08-31T10:00:00Z", specV1(t))

	c := newSpecCache()
	changed, errs := c.refresh(src)
	if len(errs) != 0 {
		t.Fatalf("refresh errors: %v", errs)
	}
	if len(changed) != 1 || changed[0] != "api.acme.test" {
		t.Fatalf("changed = %v, want [api.acme.test]", changed)
	}

	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("bound host has no contract")
	}
	if _, ok := c.lookup("api.globex.test"); ok {
		t.Error("an unrelated host resolved a contract — binding is not scoping")
	}
	if c.empty() {
		t.Error("cache reports empty with a contract loaded")
	}
	if docs, raw := c.stats(); docs != 1 || raw != len(specV1(t)) {
		t.Errorf("stats = (%d docs, %d bytes), want (1, %d)", docs, raw, len(specV1(t)))
	}
}

// TestSpecCacheIgnoresNonProviderRows: the cache is for uploaded REST contracts
// bound to a provider host. Self contracts stay config-loaded, MCP snapshots are
// self-delivering and belong to the MCP detector, and an unbound row cannot be
// keyed by host at all. None may leak into the OpenAPI lookup.
func TestSpecCacheIgnoresNonProviderRows(t *testing.T) {
	doc := specV1(t)
	src := newFakeSource()
	src.infos = []model.SpecInfo{
		{Integration: "self", Role: model.SpecRoleSelf, Format: model.SpecFormatOpenAPI, PeerHost: "api.self.test", LoadedAt: "t"},
		{Integration: "mcp-acme", Role: model.SpecRoleProvider, Format: model.SpecFormatMCP, PeerHost: "mcp.acme.test", LoadedAt: "t"},
		{Integration: "unbound", Role: model.SpecRoleProvider, Format: model.SpecFormatOpenAPI, PeerHost: "", LoadedAt: "t"},
	}
	src.docs["self"] = doc
	src.docs["mcp-acme"] = doc
	src.docs["unbound"] = doc

	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("refresh errors: %v", errs)
	}
	if !c.empty() {
		t.Fatalf("cache took %d rows it should have skipped", func() int { n, _ := c.stats(); return n }())
	}
	if len(src.fetches) != 0 {
		t.Errorf("fetched documents for skipped rows: %v", src.fetches)
	}
}

// TestSpecCacheRefetchesOnlyWhatChanged is the property that makes a 60s ticker
// affordable on a fifty-provider front: the refresh compares metadata and
// downloads nothing when nothing moved.
func TestSpecCacheRefetchesOnlyWhatChanged(t *testing.T) {
	doc := specV1(t)
	src := newFakeSource()
	src.put("acme", "api.acme.test", "v1", doc)
	src.put("globex", "api.globex.test", "v1", doc)

	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("first refresh: %v", errs)
	}
	if src.fetches["acme"] != 1 || src.fetches["globex"] != 1 {
		t.Fatalf("first refresh fetches = %v, want one each", src.fetches)
	}

	// Nothing changed: a second refresh must download nothing at all.
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("second refresh: %v", errs)
	}
	if src.fetches["acme"] != 1 || src.fetches["globex"] != 1 {
		t.Errorf("unchanged refresh re-downloaded: %v", src.fetches)
	}

	// One contract is replaced (the store restamps loaded_at): only it refetches.
	src.infos[1].LoadedAt = "v2"
	changed, errs := c.refresh(src)
	if len(errs) != 0 {
		t.Fatalf("third refresh: %v", errs)
	}
	if src.fetches["acme"] != 1 {
		t.Errorf("untouched contract refetched: %v", src.fetches)
	}
	if src.fetches["globex"] != 2 {
		t.Errorf("replaced contract not refetched: %v", src.fetches)
	}
	if len(changed) != 1 || changed[0] != "api.globex.test" {
		t.Errorf("changed = %v, want [api.globex.test]", changed)
	}
}

// TestSpecCacheDropsRemoved: Remove ships with upload, so the cache must forget
// a contract the store no longer has — otherwise a mis-bound upload keeps
// validating after the operator deleted it.
func TestSpecCacheDropsRemoved(t *testing.T) {
	src := newFakeSource()
	src.put("acme", "api.acme.test", "v1", specV1(t))

	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Fatal("contract not cached")
	}

	src.infos = nil
	changed, _ := c.refresh(src)
	if _, ok := c.lookup("api.acme.test"); ok {
		t.Error("removed contract still validating")
	}
	if len(changed) != 1 || changed[0] != "api.acme.test" {
		t.Errorf("changed = %v, want [api.acme.test]", changed)
	}
	if !c.empty() {
		t.Error("cache not empty after the only contract was removed")
	}
}

// TestSpecCacheOneBadDocumentDoesNotBlindTheRest: a row that cannot be fetched
// or parsed costs its own host detection and nothing else. The upload path
// rejects unparseable documents before persisting, so this is the belt to that
// braces — but a single bad row taking down every other provider's detection
// would be a far worse failure than the one it came from.
func TestSpecCacheOneBadDocumentDoesNotBlindTheRest(t *testing.T) {
	src := newFakeSource()
	src.put("acme", "api.acme.test", "v1", specV1(t))
	src.put("broken", "api.broken.test", "v1", []byte("this is not an openapi document"))
	src.put("gone", "api.gone.test", "v1", nil)
	src.docErrOn = "gone"

	c := newSpecCache()
	_, errs := c.refresh(src)
	if len(errs) != 2 {
		t.Fatalf("errors = %d (%v), want 2 (one unparseable, one fetch failure)", len(errs), errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("a healthy contract was lost to an unhealthy sibling")
	}
	if _, ok := c.lookup("api.broken.test"); ok {
		t.Error("an unparseable document was cached")
	}
	if _, ok := c.lookup("api.gone.test"); ok {
		t.Error("an unfetchable document was cached")
	}
}

// TestSpecCacheListFailureKeepsServing: the store pod being briefly unreachable
// must not blind a front. The cache keeps serving what it has.
func TestSpecCacheListFailureKeepsServing(t *testing.T) {
	src := newFakeSource()
	src.put("acme", "api.acme.test", "v1", specV1(t))

	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}

	src.listErr = errors.New("store pod unreachable")
	changed, errs := c.refresh(src)
	if len(errs) != 1 {
		t.Errorf("errors = %v, want the list failure surfaced", errs)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want nothing touched on a failed refresh", changed)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("a failed refresh dropped a working contract — a blip must not stop detection")
	}
}

// TestSpecCacheNilIsPassThrough: a processor with no contract source degrades to
// capture-and-stamp, never to a panic on the hot path.
func TestSpecCacheNilIsPassThrough(t *testing.T) {
	var c *specCache
	if _, ok := c.lookup("api.acme.test"); ok {
		t.Error("nil cache resolved a contract")
	}
	if !c.empty() {
		t.Error("nil cache is not empty")
	}
	if docs, raw := c.stats(); docs != 0 || raw != 0 {
		t.Errorf("nil cache stats = (%d, %d), want (0, 0)", docs, raw)
	}
}
