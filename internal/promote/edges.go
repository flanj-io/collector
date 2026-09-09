package promote

// Edge registration (CONTRACTS §5, POST /api/v1/edges/sync — v1 phase 2): the
// collector periodically registers the EXTERNAL edges it has discovered, so the
// owner's control-plane dashboard can show the integration graph before any
// finding exists. The payload per edge is minimal and ENUMERATED: registrable
// domain, direction, first seen, last seen. Nothing else.
//
// Two invariants live in this file and are pinned by wire-bytes tests
// (edges_test.go):
//
//  1. INTERNAL EDGES NEVER LEAVE. The ledger invariant — "only external edges
//     are surfaced" — extends across the wire. BuildEdgeRegistrations drops
//     every non-external edge, including the SDK's `local-process` class (a
//     child process is not a network edge). This is the second wall: the caller
//     already asks the store for external edges only.
//  2. THE PEER HOST NEVER LEAVES. Only the registrable domain (eTLD+1, computed
//     LOCALLY via the public-suffix list) goes on the wire. `api.acme.test` and
//     `api-eu.acme.test` register as one row, `acme.test` — the org behind the
//     domain is the fact worth registering; which subdomain it answers on is
//     this deployment's business. IP-literal peers register the literal, which
//     is what RegistrableDomain returns for them.
//
// There are NO volume or rpm aggregates here, deliberately (spec §3 Step 2):
// call counts and drift counts stay local until something needs them.

import (
	"context"
	"net/http"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
)

// EdgesSyncMaxItems is the control plane's per-request cap (CONTRACTS §5):
// more than 200 edges in one POST is a 400.
const EdgesSyncMaxItems = 200

// Per-field length caps of the CP's sync DTO (the public CONTRACTS.md §5 caps;
// they mirror the MaxLength decorators in the CP's sync-edge.dto.ts). The CP
// validates the batch as a unit, so ONE oversized value would 400 the whole
// POST on every tick forever — BuildEdgeRegistrations truncates instead.
const (
	capRegistrableDomain = 253 // the DNS name limit; an IP literal is far shorter
	capDirection         = 16
	capEdgeTimestamp     = 64
)

// EdgeRegistration is one row of the POST /api/v1/edges/sync request — the
// complete allow-list. Deliberately NOT model.Edge: `peer_host` identifies a
// specific listener inside the deployment, and `call_count` / `drift_count` are
// volume aggregates that nothing needs yet. A new model.Edge field stays local
// until it is deliberately added here.
type EdgeRegistration struct {
	RegistrableDomain string `json:"registrable_domain"`
	// Direction is the EDGE ORIENTATION vocabulary — "outbound" | "inbound" —
	// not the call-direction words (client/server) the OTLP convention uses.
	// The CP's row is about the relationship, so it reads in the relationship's
	// terms: outbound = this org is the consumer, inbound = the provider.
	Direction string `json:"direction"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// EdgesRequest is the POST /api/v1/edges/sync body. An empty list is a valid
// no-op on the CP, but callers normally skip the POST entirely.
type EdgesRequest struct {
	Edges []EdgeRegistration `json:"edges"`
}

// EdgesResponse is the 200 answer: how many rows the CP received and how many
// it stored (idempotent upsert by (collector, domain, direction) — a repeat
// post updates in place).
type EdgesResponse struct {
	Received int `json:"received"`
	Stored   int `json:"stored"`
}

// BuildEdgeRegistrations maps stored edges onto the wire allow-list.
//
// External only, and FOLDED BY DOMAIN: several hosts under one registrable
// domain are one registration, because that is what the payload is about. The
// fold takes the EARLIEST first_seen and the LATEST last_seen across the hosts
// it merges — the relationship began when its first host was seen and is as
// fresh as its most recent one. Timestamps are ISO-8601 UTC, which compares
// lexically, so the min/max are plain string comparisons (the same rule the
// store's window queries rely on).
//
// Defensive normalization for rows written by older collectors: an empty
// first_seen falls back to last_seen and vice versa; an edge with neither, or
// with a host the public-suffix list reduces to nothing, is dropped rather than
// registered as a blank domain.
//
// At most EdgesSyncMaxItems rows are returned, in the order the caller's rows
// arrived. That order is the store's — ListEdges sorts `direction ASC, last_seen
// DESC` — so a deployment over the cap registers its MOST RECENTLY ACTIVE edges,
// which is the right subset to keep: a relationship nothing has touched in the
// window is the one that can wait for the next tick. This function is
// deterministic for a given input; it inherits its stability from that ordering
// rather than imposing one, and does not re-sort (a second sort here would
// silently disagree with the store's and make the truncation point harder to
// reason about, not easier).
func BuildEdgeRegistrations(edges []model.Edge) []EdgeRegistration {
	byKey := make(map[string]*EdgeRegistration, len(edges))
	order := make([]string, 0, len(edges))
	for _, e := range edges {
		// Wall 1: internal (and `local-process`) never leaves. The caller asks
		// the store for external edges; this is the wall that holds when a
		// future caller forgets to.
		if !edge.IsExternal(e.Class) {
			continue
		}
		// Wall 2: the host is reduced to its registrable domain HERE, so the
		// only spelling that can reach the wire is the reduced one.
		domain := edge.RegistrableDomain(e.PeerHost)
		if domain == "" {
			continue
		}
		first, last := e.FirstSeen, e.LastSeen
		if first == "" {
			first = last
		}
		if last == "" {
			last = first
		}
		if first == "" {
			// A row with no timestamps at all says nothing about a
			// relationship; registering it would put empty strings on the wire
			// and fail the CP's non-empty validation for the whole batch.
			continue
		}
		direction := edge.Orientation(e.Direction)
		key := domain + "|" + direction
		cur, ok := byKey[key]
		if !ok {
			byKey[key] = &EdgeRegistration{
				RegistrableDomain: truncateToCap(domain, capRegistrableDomain),
				Direction:         truncateToCap(direction, capDirection),
				FirstSeen:         truncateToCap(first, capEdgeTimestamp),
				LastSeen:          truncateToCap(last, capEdgeTimestamp),
			}
			order = append(order, key)
			continue
		}
		if first < cur.FirstSeen {
			cur.FirstSeen = truncateToCap(first, capEdgeTimestamp)
		}
		if last > cur.LastSeen {
			cur.LastSeen = truncateToCap(last, capEdgeTimestamp)
		}
	}
	// Stable order: earliest-registered relationship first (ties broken by the
	// order the store handed rows over, which is itself deterministic). This is
	// what makes the >200 case send the same subset every tick.
	out := make([]EdgeRegistration, 0, len(order))
	for _, key := range order {
		if len(out) == EdgesSyncMaxItems {
			break
		}
		out = append(out, *byKey[key])
	}
	return out
}

// PostEdges sends one edge-registration batch: POST /api/v1/edges/sync, Bearer
// collector key, the usual version headers. Single attempt, no retry — the
// caller's next tick is the retry. 200 { received, stored }.
func (c *Client) PostEdges(ctx context.Context, req EdgesRequest) (EdgesResponse, int, error) {
	var out EdgesResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/edges/sync", c.bearer(), req, &out, http.StatusOK)
	return out, status, err
}
