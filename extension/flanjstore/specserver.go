package flanjstore

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// The tiered topology's contract channel.
//
// A FRONT collector runs flanjdrift but owns no store, while the store pod owns
// both the store and the UI an operator uploads contracts through. Without a
// read path the upload lands where the front never sees it and REST drift
// detection silently never runs — on exactly the deployments large enough to be
// tiered. This serves that read.
//
// Deliberately NOT on the UI extension. The UI is loopback-only and stays that
// way (non-negotiable #5); this is a separate, read-only, contracts-only
// listener on the cluster interface, a sibling of the `:4318` intra-cluster
// ingest fronts already speak to. It exposes no calls, no findings, no
// settings, and mutates nothing.
const (
	specReadTimeout  = 10 * time.Second
	specWriteTimeout = 30 * time.Second
	// specMaxDoc caps a single document: the ONE cap, shared with the upload
	// path and the front that reads this endpoint (model.MaxContractDocBytes),
	// so the ends of the channel agree by construction rather than by comment.
	specMaxDoc = model.MaxContractDocBytes
)

// startSpecServer binds the contract endpoint when one is configured. A front
// with no store pod to ask is the normal single-pod case, so an unset
// spec_endpoint is silence, not an error.
func (e *storeExtension) startSpecServer() error {
	if e.cfg.SpecEndpoint == "" {
		return nil
	}
	ln, err := net.Listen("tcp", e.cfg.SpecEndpoint)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/contracts", e.handleSpecList)
	mux.HandleFunc("GET /internal/contracts/doc", e.handleSpecDoc)

	e.specSrv = &http.Server{
		Handler:           e.authSpec(mux),
		ReadHeaderTimeout: specReadTimeout,
		WriteTimeout:      specWriteTimeout,
	}
	e.specLn = ln
	go func() {
		if err := e.specSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && e.logger != nil {
			e.logger.Error("contract endpoint stopped", zap.Error(err))
		}
	}()
	if e.logger != nil {
		e.logger.Info("contract endpoint listening (intra-cluster, read-only)",
			zap.String("endpoint", e.cfg.SpecEndpoint),
			zap.Bool("token_required", e.cfg.SpecToken != ""))
	}
	return nil
}

// authSpec enforces the shared token. Compared in constant time, and the token
// itself is never logged or echoed.
func (e *storeExtension) authSpec(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unconditional. An empty configured token used to skip the check
		// entirely, which turned an unset FLANJ_SPEC_TOKEN into an open
		// listener on the cluster interface. Config validation now refuses that
		// combination outright; this is the second lock on the same door, and
		// it fails CLOSED — an empty token matches no request, including one
		// sending a bare `Bearer `.
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if e.cfg.SpecToken == "" || len(got) <= len(prefix) || got[:len(prefix)] != prefix ||
			subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(e.cfg.SpecToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleSpecList returns contract METADATA — never the documents. That is what
// keeps a front's steady-state refresh one small request per tick: it compares
// this list against what it has cached (OpenAPI) or seeded (MCP) and downloads
// only what moved.
func (e *storeExtension) handleSpecList(w http.ResponseWriter, _ *http.Request) {
	st := e.Store()
	if st == nil {
		http.Error(w, "store not ready", http.StatusServiceUnavailable)
		return
	}
	infos, err := st.ListSpecInfos()
	if err != nil {
		e.specError(w, "list contracts", err)
		return
	}
	out := make([]model.SpecInfo, 0, len(infos))
	for _, si := range infos {
		if servableContract(si) {
			out = append(out, si)
		}
	}
	// The listing carries each document's SIZE (model.SpecInfo.DocBytes), so a
	// front recognises an over-cap row here and stops asking for a document it
	// will be refused. This is also the store pod's own full sweep of the
	// condition, and therefore where it is logged — on transition.
	e.reportOverCap(out)
	writeSpecJSON(w, map[string]any{"contracts": out})
}

// Log lines for the document cap. Named constants because the tiered e2e lane
// waits on them: a front and a store pod have no other observable surface for
// a condition that produces no finding and no record.
const (
	msgSpecOverCap        = "contract endpoint: a stored document is past the cap and will not be served"
	msgSpecOverCapCleared = "contract endpoint: the stored document is back under the cap and is served again"
)

// reportOverCap logs the document-cap refusal ON TRANSITION — when a row first
// goes past the cap, and when it comes back under it.
//
// Every listing re-derives the condition from scratch, and a front lists every
// ten seconds. Logging what the sweep found would put six identical Warn lines
// a minute per oversized edge per front into the store pod's log, forever,
// which buries the one line that matters under a thousand copies of itself and
// reads like a storm of new events rather than one unchanged fact. The start
// and the end are the events; `condition.Standing` holds the rest.
//
// Called with the SERVABLE rows only — the ones that actually cross this
// channel. A self contract or an unbound row is withheld from fronts for other
// reasons entirely and its size decides nothing.
func (e *storeExtension) reportOverCap(servable []model.SpecInfo) {
	now := make(map[string]int64)
	hosts := make(map[string]string)
	for _, si := range servable {
		if si.DocBytes > specMaxDoc {
			now[si.Integration] = int64(si.DocBytes)
			hosts[si.Integration] = si.PeerHost
		}
	}
	raised, cleared := e.overCap.Observe(now)
	if e.logger == nil {
		return
	}
	for _, c := range raised {
		e.logger.Warn(msgSpecOverCap,
			zap.String("integration", c.Key),
			zap.String("peer_host", hosts[c.Key]),
			zap.Int64("bytes", c.Detail),
			zap.Int("cap_bytes", specMaxDoc))
	}
	for _, c := range cleared {
		e.logger.Info(msgSpecOverCapCleared,
			zap.String("integration", c.Key),
			zap.Int("cap_bytes", specMaxDoc))
	}
}

// servableContract is the ONE rule for what may cross this hop, applied by the
// list route and the doc route alike. A filter on the index and none on the
// item is not a filter: the doc route used to pass the caller's `integration`
// straight to a bare `SELECT ... WHERE integration=?` over the same table, so
// `?integration=self` returned the organisation's own OpenAPI document — the
// exact thing the list route was written to withhold.
//
// A front validates its dependencies' traffic, so it gets a PROVIDER's
// contract bound to the edge it applies to, in either format the store holds:
// an uploaded OpenAPI document, or an observed MCP tools/list snapshot. The
// self contract is the store pod's own and an unbound contract names no edge;
// neither has business here.
//
// MCP snapshots were withheld until 2026-09-07 on the reasoning that they are
// self-delivering from traffic the front already sees. They are — for the ONE
// front that saw it. Every other front's baseline was whatever that process
// had personally witnessed: a rename observed through front-a raised nothing
// when the stale client called through front-b, and a restarted front forgot
// the baseline entirely. The store holds the org-wide baseline (every front
// forwards its snapshots as spec_info records); this channel is how it gets
// back down. The front's drift processor routes the two formats apart on its
// side (an MCP row never enters its OpenAPI cache).
func servableContract(si model.SpecInfo) bool {
	return si.Role == model.SpecRoleProvider &&
		si.PeerHost != "" &&
		(si.Format == model.SpecFormatOpenAPI || si.Format == model.SpecFormatMCP)
}

// handleSpecDoc returns one raw contract document.
func (e *storeExtension) handleSpecDoc(w http.ResponseWriter, r *http.Request) {
	st := e.Store()
	if st == nil {
		http.Error(w, "store not ready", http.StatusServiceUnavailable)
		return
	}
	integration := r.URL.Query().Get("integration")
	if integration == "" {
		http.Error(w, "integration is required", http.StatusBadRequest)
		return
	}
	// Resolve the metadata FIRST and apply the same admission rule as the list.
	// A withheld contract answers exactly like an absent one — a 403 here would
	// confirm that `self` exists to anyone holding the token.
	infos, err := st.ListSpecInfos()
	if err != nil {
		e.specError(w, "list contracts", err)
		return
	}
	servable := false
	var row model.SpecInfo
	for _, si := range infos {
		if si.Integration == integration && servableContract(si) {
			servable, row = true, si
			break
		}
	}
	if !servable {
		http.Error(w, "no such contract", http.StatusNotFound)
		return
	}
	// Refuse from the METADATA, before the document is read. The listing
	// already measured it, and loading an over-cap document into this pod's
	// heap on every request only to refuse it is the cost the cap exists to
	// avoid — one that grows with the number of fronts asking.
	if row.DocBytes > specMaxDoc {
		e.refuseOverCap(w, row.Integration, row.PeerHost, row.DocBytes)
		return
	}
	raw, _, ok, err := st.GetSpecDoc(integration)
	if err != nil {
		e.specError(w, "read contract", err)
		return
	}
	if !ok {
		http.Error(w, "no such contract", http.StatusNotFound)
		return
	}
	if len(raw) > specMaxDoc {
		// REFUSE — never truncate. A document cut off at the cap goes out as a
		// 200 the front cannot tell from a whole one: it parses as garbage,
		// the front reports a PARSE error for a SIZE problem, and detection on
		// that edge silently stops. Cutting it here also blinded the front's
		// own guard, which can only see an overflow if one is transmitted.
		//
		// This is reachable, and by exactly one writer. The upload path
		// refuses a larger document before it is ever stored, and the self
		// contract never crosses this hop (servableContract). An OBSERVED MCP
		// tools/list has no cap anywhere on its way in — the SDK sends the
		// server's whole tool array verbatim, the drift processor persists
		// whatever parses, and the only bound is the OTLP receiver's 20 MiB
		// request body — so a large enough catalogue lands in a row that has
		// to cross this channel.
		//
		// The row stays LISTED, and since 2026-09-08 it is listed WITH ITS SIZE
		// and with an explicit state on the Contracts card, so "a contract is
		// present" and "this edge is unchecked on every front" stop
		// contradicting each other. Reaching here past the metadata check above
		// means the row was replaced between the list and the read.
		e.refuseOverCap(w, integration, row.PeerHost, len(raw))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(raw)
}

// refuseOverCap answers a document past the cap: 413, and a log line the FIRST
// time this row is refused (or the first time a NEW over-cap document is stored
// for it). A front asks every ten seconds and gets the same named refusal every
// time, which is what the front needs; the store pod's operator needs the event,
// not the repetition, so the line rides condition.Standing — the same set the
// listing sweep clears, so the two routes cannot each log the row once.
func (e *storeExtension) refuseOverCap(w http.ResponseWriter, integration, peerHost string, bytes int) {
	if e.overCap.Raise(integration, int64(bytes)) && e.logger != nil {
		// Int64, matching reportOverCap's field exactly: the two routes write
		// the SAME line, and a reader (or a test) must not have to know which
		// one produced it.
		e.logger.Warn(msgSpecOverCap,
			zap.String("integration", integration),
			zap.String("peer_host", peerHost),
			zap.Int64("bytes", int64(bytes)),
			zap.Int("cap_bytes", specMaxDoc))
	}
	http.Error(w, "contract document larger than the cap", http.StatusRequestEntityTooLarge)
}

// specError logs the cause and tells the caller only that it failed — a peer
// gets a status, not the store's internals.
func (e *storeExtension) specError(w http.ResponseWriter, what string, err error) {
	if e.logger != nil {
		e.logger.Warn("contract endpoint: "+what+" failed", zap.Error(err))
	}
	http.Error(w, what+" failed", http.StatusInternalServerError)
}

func writeSpecJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// stopSpecServer closes the listener at Shutdown.
func (e *storeExtension) stopSpecServer() {
	if e.specSrv != nil {
		_ = e.specSrv.Close()
	}
}

// compile-time assertion: the endpoint only ever needs the read surface.
var _ interface {
	ListSpecInfos() ([]model.SpecInfo, error)
	GetSpecDoc(string) ([]byte, string, bool, error)
} = (store.Store)(nil)
