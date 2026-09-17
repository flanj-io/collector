package flanjui

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/flanj-io/collector/internal/drift"
)

// Contract PROBE — suggest-and-approve, and nothing else.
//
// The probe asks a peer host this collector ALREADY CALLS whether it publishes
// its OpenAPI document at one of the conventional paths. It returns what it
// found. It binds NOTHING, ever, on any path, and there is no code below that
// writes to the store — that is not an oversight to be tidied up later, it is
// the feature.
//
// Why it cannot auto-bind, stated once so nobody optimises it away: A WRONG
// CONTRACT IS WORSE THAN NO CONTRACT. A host with no contract renders
// `not checked`, which is honest and costs the operator nothing. A host bound
// to the wrong document renders DRIFTED — loudly, on a stranger's real
// provider, to a thread that stranger reads. `competitors.md` records the
// consequence: providers who get flagged wrongly mute the tool, and a muted
// provider is a dead edge in a network product. A probe hit is a GUESS
// (`/openapi.json` on a gateway is routinely somebody else's document, or a
// stale copy, or the gateway's own spec rather than the service's) and a guess
// may be OFFERED but never ACTED ON.
//
// It is also opt-in, which here means what it says: this route runs only when
// an operator presses a control. Nothing schedules it, no start-up path calls
// it, and there is no config key that turns it on — a key would be a standing
// instruction to make requests, which is the thing "opt-in" is supposed to
// prevent, and CONTRACTS §8 is frozen anyway.

// conventionalSpecPaths are the paths a probe tries, in the order a publisher
// is likely to use them. Four, deliberately: this list is EGRESS, every entry
// is a request to somebody else's host, and a longer list scanning for
// something that is not there reads like exactly what it would be.
var conventionalSpecPaths = []string{
	"/openapi.json",
	"/openapi.yaml",
	"/.well-known/openapi",
	"/swagger.json",
}

// probeCandidate is one thing the probe found — OFFERED, never bound.
type probeCandidate struct {
	URL string `json:"url"`
	// Title / Version / Endpoints come from parsing what was served, so a
	// candidate the operator is shown is one that at least IS an OpenAPI
	// document. A 200 that serves an HTML page is not offered at all.
	Title     string `json:"title,omitempty"`
	Version   string `json:"version,omitempty"`
	Endpoints int    `json:"endpoints"`
	// Servers / ServersMatch are the same corroboration the upload confirm step
	// shows. They are the single most useful signal for "is this the right
	// document?", and they belong on the offer, not only after it is taken.
	Servers      []string `json:"servers"`
	ServersMatch bool     `json:"servers_match"`
}

// probeResponse is the whole answer: what was tried, and what came back.
//
// `Tried` is returned even when nothing was found, because "we asked these four
// and none of them answered" is a RESULT the operator can act on, and an empty
// list with no explanation is indistinguishable from a broken control.
type probeResponse struct {
	PeerHost   string           `json:"peer_host"`
	Tried      []string         `json:"tried"`
	Candidates []probeCandidate `json:"candidates"`
}

// handleContractProbe looks for a published spec on a host this collector
// already talks to, and OFFERS what it finds.
//
// Guarded exactly like upload and fetch. The binding step is
// POST /api/contracts/fetch with a candidate's URL — the one bind path, so
// there is no second place a contract can be written and no path where an
// offer becomes a binding without a human in between.
func (e *uiExtension) handleContractProbe(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	var body struct {
		PeerHost string `json:"peer_host"`
	}
	if !readJSONBody(w, r, maxSmallBodyBytes, &body, "request_too_large", msgRequestTooLarge) {
		return
	}
	if strings.TrimSpace(body.PeerHost) == "" {
		writeErr(w, http.StatusBadRequest, "host_required", msgContractProbeHostRequired)
		return
	}
	host, err := normalizeHost(body.PeerHost)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_host", err.Error())
		return
	}
	// THE constraint on this route: the host must be one the SDK already
	// reports traffic to. It is what makes the probe categorically different
	// from a URL fetcher — it can only ever reach hosts this deployment is
	// already calling, so it opens no destination that was not already open.
	// A typo'd host is refused here rather than probed, which also means a
	// fat-fingered domain never receives four requests from a stranger.
	if !hostHasTraffic(st, host) {
		writeErr(w, http.StatusNotFound, "unknown_host", msgContractProbeUnknownHost)
		return
	}

	resp := probeResponse{PeerHost: host, Tried: []string{}, Candidates: []probeCandidate{}}
	for _, p := range conventionalSpecPaths {
		u := &url.URL{Scheme: probeScheme(host), Host: host, Path: p}
		resp.Tried = append(resp.Tried, u.String())
		doc, final, ferr := e.fetchContractDoc(r.Context(), u, contractProbeTimeout)
		if ferr != nil {
			// A miss is the COMMON case and is not an error: most providers
			// publish at none of these paths. Logged at debug volume only —
			// four info lines per press, per host, would be noise.
			continue
		}
		sum, err := drift.DescribeSpec(doc)
		if err != nil {
			// Served something, but not a spec. Not offered: an offer the
			// operator cannot bind is worse than no offer.
			continue
		}
		resp.Candidates = append(resp.Candidates, candidateFrom(final, host, sum))
	}
	e.telemetry.Logger.Info("contracts: probed " + host + " for a published spec; " +
		strconv.Itoa(len(resp.Candidates)) + " of " + strconv.Itoa(len(resp.Tried)) + " conventional paths answered with a document")
	writeJSON(w, http.StatusOK, resp)
}

// probeScheme picks the scheme a probe uses. https, except for a loopback host,
// which is a developer's own machine and is routinely plain http.
//
// It does NOT fall back from https to http on failure. A provider who does not
// serve their spec over TLS is not someone this collector should quietly
// downgrade for — and a fallback would double this route's egress for every
// host that simply does not publish a spec, which is most of them.
func probeScheme(host string) string {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 {
		h = h[:i]
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost") {
		return "http"
	}
	return "https"
}

// candidateFrom describes one hit, with the same servers corroboration the
// confirm step shows.
func candidateFrom(u, host string, sum drift.SpecSummary) probeCandidate {
	c := probeCandidate{
		URL:       u,
		Title:     sum.Title,
		Version:   sum.Version,
		Endpoints: sum.Endpoints,
		Servers:   sum.ServerHosts,
	}
	for _, s := range c.Servers {
		if strings.EqualFold(s, host) {
			c.ServersMatch = true
			break
		}
	}
	if c.Servers == nil {
		c.Servers = []string{}
	}
	return c
}
