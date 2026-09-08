package flanjui

// Edge registration (v1 phase 2 — CONTRACTS §5, POST /api/v1/edges/sync).
//
// Each unique EXTERNAL edge this deployment has discovered is registered to the
// control plane BEFORE any finding exists, so the owner's dashboard can show
// the integration graph from the first Connected install rather than only where
// something has already broken. The payload per edge is minimal and enumerated:
// registrable domain, direction, first seen, last seen (promote.EdgesRequest).
//
// The consent posture, all three parts of it, is what makes this defensible in
// the window before schema-push rides on it:
//
//   - GATED ON CONNECT. Nothing leaves a collector that never Connected — the
//     same skip conditions as the other two legs: a configured control plane
//     and a collector key in the store, or the tick does nothing, silently.
//   - EXTERNAL ONLY. Internal edges never leave. Two walls hold that: this file
//     asks the store for external edges (ListEdges(true)), and
//     promote.BuildEdgeRegistrations drops anything not classified external
//     regardless of what it was handed. The second wall is the one with the
//     wire-bytes test on it.
//   - DISCLOSED AND SWITCHABLE. The Connect panel says what registration sends
//     before you Connect, and `edge_sync: false` (CONTRACTS §8) turns the leg
//     off without touching the findings sync.
//
// Registration is IDEMPOTENT by design: the CP upserts on (collector,
// registrable_domain, direction), so every tick sends the current edge set
// whole and a re-send updates `last_seen` in place rather than duplicating.
// There is no local "already registered" bookkeeping to drift out of sync with
// the CP's rows.

import (
	"context"
	"fmt"

	"github.com/flanj-io/collector/internal/promote"
)

// syncEdgesOnce is one registration tick: list the EXTERNAL edges, build the
// enumerated payload, POST it with the collector key. Every skip is silent by
// design — an unconnected collector, or one that has discovered nothing, has
// nothing to say.
func (e *uiExtension) syncEdgesOnce(ctx context.Context) {
	if e.cp == nil {
		return
	}
	st := e.resolveStore()
	if st == nil {
		return
	}
	cs, err := loadConnect(st)
	if err != nil || cs.CollectorKey == "" {
		return
	}
	// externalOnly=true: the first of the two walls. The second is inside
	// BuildEdgeRegistrations, which re-checks the class on every row.
	edges, err := st.ListEdges(true)
	if err != nil || len(edges) == 0 {
		return
	}
	rows := promote.BuildEdgeRegistrations(edges)
	if len(rows) == 0 {
		return
	}
	resp, status, err := e.keyedClient(cs).PostEdges(ctx, promote.EdgesRequest{Edges: rows})
	if err != nil {
		// Status + count only — never a domain, never the response body, never
		// the bearer. A log line that named the edges would put the dependency
		// graph in the pod logs, which is the thing this slice is careful about.
		e.telemetry.Logger.Debug(fmt.Sprintf("edge sync: registration of %d edges failed with status %d — retrying next tick", len(rows), status))
		return
	}
	e.telemetry.Logger.Debug(fmt.Sprintf("edge sync: %d edges registered, %d stored (status %d)", len(rows), resp.Stored, status))
}
