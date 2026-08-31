package flanjdrift

import (
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// Provider contracts are UPLOADED in the UI and live in the store — never in
// config (CONTRACTS §8 dropped `spec_path`/`spec_v2_path`/`peer_host`). So the
// processor cannot load a document once at construction any more: it keeps a
// cache of PARSED documents keyed by peer host and refreshes it in the
// background.
//
// The per-call path is a map read and nothing else. Parsing an OpenAPI document
// is expensive and the store is a database — neither belongs on the hot path.
const (
	// specRefresh bounds how often the cache re-reads its source. An upload
	// therefore starts validating within one tick, which is what the UI
	// promises ("Validating from now on").
	specRefresh = 60 * time.Second
	// specRefreshFloor is the minimum spacing between refreshes, so a burst of
	// traffic to uncovered hosts cannot turn first-sight kicks into a hot loop.
	specRefreshFloor = 5 * time.Second
)

// specSource supplies provider contracts at runtime. Two implementations: the
// co-located store handle (single pod, and every pod of a shared-postgres
// deployment), and — for a front collector of the tiered topology, which has no
// store of its own — the store pod over HTTP.
type specSource interface {
	// listSpecs returns contract METADATA only. Cheap by construction: the
	// refresh compares it against what is cached and downloads nothing when
	// nothing changed.
	listSpecs() ([]model.SpecInfo, error)
	// specDoc returns one raw contract document.
	specDoc(integration string) ([]byte, error)
}

// storeSpecSource reads the co-located store directly.
type storeSpecSource struct{ st store.Store }

func (s storeSpecSource) listSpecs() ([]model.SpecInfo, error) { return s.st.ListSpecInfos() }

func (s storeSpecSource) specDoc(integration string) ([]byte, error) {
	raw, _, ok, err := s.st.GetSpecDoc(integration)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return raw, nil
}

// cachedSpec is one parsed provider contract, plus the change token that lets a
// refresh skip re-parsing it.
type cachedSpec struct {
	doc         *openapi3.T
	integration string
	version     string
	// loadedAt is the store's own stamp for this row. It changes on every
	// upload, so comparing it is how the refresh decides to re-download.
	loadedAt string
	// rawBytes is the document's size, summed into the cache's memory report.
	rawBytes int
}

// specCache holds the parsed provider contracts, keyed by the peer host each is
// bound to. Uploads bind to exactly one host (mandatory — an unbound contract
// validates nothing forever while the card claims otherwise), which is what
// makes this map the whole lookup.
type specCache struct {
	mu     sync.RWMutex
	byHost map[string]cachedSpec
}

func newSpecCache() *specCache { return &specCache{byHost: map[string]cachedSpec{}} }

// lookup returns the contract bound to peerHost, if one is cached.
//
// The read methods tolerate a nil cache and report "no contracts". A processor
// with no contract source is a legitimate configuration — a spec-less front
// still stamps call ids, which is what makes front->store retries idempotent —
// so the absence of contracts must degrade to pass-through, never to a panic
// on the hot path.
func (c *specCache) lookup(peerHost string) (*openapi3.T, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byHost[peerHost]
	if !ok {
		return nil, false
	}
	return e.doc, true
}

// empty reports whether no contract is cached — the pass-through fast path.
func (c *specCache) empty() bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byHost) == 0
}

// stats reports what the cache is holding, for the front's memory line: how
// many documents and how many bytes of source they were parsed from. Parsed
// documents cost a multiple of this, so it is an index rather than a measure —
// which is what a "getting close to the cap" signal needs.
func (c *specCache) stats() (docs int, rawBytes int) {
	if c == nil {
		return 0, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, e := range c.byHost {
		rawBytes += e.rawBytes
	}
	return len(c.byHost), rawBytes
}

// refresh reconciles the cache against the source: it downloads and parses only
// the contracts whose stored document actually changed, and drops the ones that
// are gone. A per-document failure is logged by the caller and skipped — one bad
// row must never cost the other contracts their detection.
//
// Returns the hosts whose contract changed, for logging.
func (c *specCache) refresh(src specSource) (changed []string, errs []error) {
	infos, err := src.listSpecs()
	if err != nil {
		return nil, []error{err}
	}

	// The rows this cache is for: uploaded (or otherwise stored) OpenAPI
	// contracts for PROVIDERS, each bound to a host. Self contracts stay
	// config-loaded in v1, and MCP snapshots are self-delivering and handled by
	// the MCP detector, so neither belongs here.
	want := make(map[string]model.SpecInfo, len(infos))
	for _, si := range infos {
		if si.Role != model.SpecRoleProvider || si.Format != model.SpecFormatOpenAPI || si.PeerHost == "" {
			continue
		}
		want[si.PeerHost] = si
	}

	c.mu.RLock()
	stale := make([]model.SpecInfo, 0, len(want))
	for host, si := range want {
		if cur, ok := c.byHost[host]; !ok || cur.loadedAt != si.LoadedAt || cur.integration != si.Integration {
			stale = append(stale, si)
		}
	}
	c.mu.RUnlock()

	// Download and parse outside the lock — the hot path keeps reading the old
	// documents while a replacement is being prepared.
	fresh := make(map[string]cachedSpec, len(stale))
	for _, si := range stale {
		raw, err := src.specDoc(si.Integration)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(raw) == 0 {
			continue // the row lost its document between list and fetch; next tick
		}
		doc, err := drift.LoadSpecData(raw)
		if err != nil {
			// A document that cannot be parsed must never have been stored —
			// the upload path rejects before persisting. Reaching here means a
			// row predates that rule or was written by something else; skip it
			// rather than failing the whole refresh.
			errs = append(errs, err)
			continue
		}
		fresh[si.PeerHost] = cachedSpec{
			doc:         doc,
			integration: si.Integration,
			version:     si.Version,
			loadedAt:    si.LoadedAt,
			rawBytes:    len(raw),
		}
	}

	c.mu.Lock()
	for host, e := range fresh {
		c.byHost[host] = e
		changed = append(changed, host)
	}
	for host := range c.byHost {
		if _, ok := want[host]; !ok {
			delete(c.byHost, host)
			changed = append(changed, host)
		}
	}
	c.mu.Unlock()

	return changed, errs
}
