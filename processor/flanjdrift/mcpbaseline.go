package flanjdrift

import (
	"fmt"
	"sync"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
)

// The MCP half of the contract channel.
//
// An MCP server's contract is self-delivering: this processor observes the
// client's own tools/list and keeps the per-edge baseline in memory
// (drift.MCPDetector). That was the whole story on a single pod, where the
// process that observes is the process that persists, and Start re-seeds from
// the co-located store. A tiered FRONT breaks it: the front owns no store, so
// its baseline was whatever that one process had personally witnessed.
// Observed live on the tiered e2e lane, 2026-09-07: front-a sees the
// tools/list, the server renames a tool, a stale client calls the old name
// through front-b — and nothing fires. front-b never saw the list before the
// rename, so it has nothing to diff and nothing to judge the call against;
// restarting a front has the same effect, since its memory was the baseline.
//
// So the baseline comes from the store, over the channel the front already
// polls for uploaded contracts: the store pod serves its spec_infos rows of
// format "mcp" — persisted from every front's forwarded spec_info records —
// and this file offers each one to the detector, which keeps the newer of what
// it holds and what the store holds (drift.MCPDetector.Seed decides; it never
// emits findings). The metadata-first rule of speccache.go applies unchanged:
// rows are compared by loaded_at, and a document is downloaded only when its
// row moved. On a single pod the rows are this process's own, so the seed is
// a no-op after the one read that establishes that; on a shared-postgres
// deployment the same read is how one pod learns what another observed.

// mcpSeedLogMessage is the line a front logs when it adopts a baseline from
// the store. The tiered e2e lane waits on it (a front has no other observable
// surface), so it is a named constant rather than a string in a call.
const mcpSeedLogMessage = "mcp baseline seeded from the store"

// mcpSeeds remembers which store row (integration -> loaded_at) has already
// been offered to the detector, so a steady-state refresh downloads nothing.
// The zero value is ready to use.
type mcpSeeds struct {
	mu   sync.Mutex
	seen map[string]string
}

// mcpSeed is one adopted baseline, for the log line.
type mcpSeed struct {
	integration string
	peerHost    string
	observedAt  string
}

// reconcile offers the detector every MCP row whose loaded_at moved since it
// was last offered, and forgets rows the store no longer lists, so a row that
// comes back is offered again. Returns the baselines adopted, for logging;
// a row the detector declined (same content, or older than what it holds) is
// remembered too, so it is not re-downloaded every tick.
//
// A per-row failure is reported and skipped, never fatal to the others. A row
// whose document will not parse is remembered as offered: it will not parse
// next tick either, and refetching it every ten seconds would be the hot loop
// speccache.go's floor exists to prevent.
func (s *mcpSeeds) reconcile(infos []model.SpecInfo, src specSource, det *drift.MCPDetector) (adopted []mcpSeed, errs []error) {
	if s == nil || det == nil || src == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]string{}
	}

	listed := make(map[string]struct{}, len(infos))
	for _, si := range infos {
		// The same admission rule as the store pod's channel: a provider's
		// observed tools/list, bound to the edge it was observed on.
		if si.Format != model.SpecFormatMCP || si.Role != model.SpecRoleProvider || si.PeerHost == "" {
			continue
		}
		listed[si.Integration] = struct{}{}
		if s.seen[si.Integration] == si.LoadedAt {
			continue
		}
		raw, err := src.specDoc(si.Integration)
		if err != nil {
			errs = append(errs, fmt.Errorf("mcp snapshot %q: %w", si.Integration, err))
			continue // transient: the row stays unoffered and is retried next tick
		}
		if len(raw) == 0 {
			continue // the row lost its document between list and fetch; next tick
		}
		s.seen[si.Integration] = si.LoadedAt
		ok, err := det.Seed(si, raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			adopted = append(adopted, mcpSeed{integration: si.Integration, peerHost: si.PeerHost, observedAt: si.LoadedAt})
		}
	}
	for integration := range s.seen {
		if _, ok := listed[integration]; !ok {
			delete(s.seen, integration)
		}
	}
	return adopted, errs
}
