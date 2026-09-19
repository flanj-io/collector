package flanjui

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/promote"
	"github.com/flanj-io/collector/internal/redact"
	"github.com/flanj-io/collector/internal/store"
)

// routes builds the UI handler: read API under /api/*, the Connect + thread
// relay (every mutating route goes through guardMutating), the embedded SPA
// elsewhere.
func (e *uiExtension) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", e.handleHealth)
	mux.HandleFunc("/api/edges", e.handleEdges)
	// Edge rename — LOCAL mutation (guarded WITHOUT the CP check:
	// naming an edge works on a disconnected collector; only the opt-in
	// directory suggestion needs a Connected one, and its failure never fails
	// the save).
	mux.HandleFunc("/api/edges/name", e.handleEdgeName)
	// The sheet's "Open to" prefill (thread-domain-gate): is this host's
	// registrable domain a CLAIMED directory entry? Read-only, local table only.
	mux.HandleFunc("/api/directory/hint", e.handleDirectoryHint)
	mux.HandleFunc("/api/calls", e.handleCalls)
	// One stored call by id — what a finding's source_call_id names. The list
	// above is the newest 200, and a deduplicated finding keeps its FIRST
	// call as evidence, so on a busy collector that call is often not in it.
	mux.HandleFunc("/api/calls/{id}", e.handleCall)
	mux.HandleFunc("/api/findings", e.handleFindings)
	// Local acknowledge (never a relay route — guarded WITHOUT the CP check).
	mux.HandleFunc("/api/findings/{id}/ack", e.handleFindingAck)
	mux.HandleFunc("/api/findings/{id}/unack", e.handleFindingUnack)
	mux.HandleFunc("/api/contracts", e.handleContracts)
	mux.HandleFunc("/api/contracts/spec", e.handleContractSpec)
	mux.HandleFunc("/api/contracts/preview", e.handleContractPreview)
	mux.HandleFunc("/api/contracts/upload", e.handleContractUpload)
	// Fetch is one route with two steps (preview, then bind on the token it
	// returned); probe only ever OFFERS, and binding an offer goes back through
	// fetch. So there are exactly two ways a contract is written — upload and
	// fetch — and both put a human between the document and the row.
	mux.HandleFunc("/api/contracts/fetch", e.handleContractFetch)
	mux.HandleFunc("/api/contracts/probe", e.handleContractProbe)
	mux.HandleFunc("/api/contracts/remove", e.handleContractRemove)
	mux.HandleFunc("/api/connect", e.handleConnect)
	mux.HandleFunc("/api/flag", e.handleFlag)
	// Start a thread from an edge row — a MESSAGE-ONLY thread. A
	// relay route like /api/flag, and behind the same Connect gate.
	mux.HandleFunc("/api/edges/thread", e.handleEdgeThread)
	mux.HandleFunc("/api/threads", e.handleThreads)
	mux.HandleFunc("/api/threads/{id}/summary", e.handleThreadSummary)
	mux.HandleFunc("/api/threads/{id}/open", e.handleThreadOpen)
	mux.HandleFunc("/api/threads/{id}/close", e.handleThreadClose)
	mux.HandleFunc("/api/threads/{id}/reopen", e.handleThreadReopen)
	mux.HandleFunc("/api/threads/{id}/replace-link", e.handleThreadReplaceLink)
	// The agent-facing drift read surface (mcp.go): an MCP server on the SAME
	// loopback listener, under the same posture, exposing the same finding rows
	// the SPA above renders. Read-only — no mutating route has an MCP tool.
	mux.Handle(mcpPath, e.mcpHandler())
	mux.Handle(mcpPath+"/", e.mcpHandler())
	mux.Handle("/", e.spaHandler())
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// storeOrError resolves the shared store, writing a 503 and returning nil when
// it cannot be found (e.g. the store extension is not configured). Same status,
// same code, same sentence as a store call that fails once resolved
// (storeErr): to the operator both are "the local store isn't there", and two
// envelopes for one condition is how a UI ends up rendering two different
// stories about the same outage.
func (e *uiExtension) storeOrError(w http.ResponseWriter) store.Store {
	st := e.resolveStore()
	if st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store_error", msgStoreUnavailable)
	}
	return st
}

// storeErr answers a READ route whose store call failed.
//
// The raw error goes to the log and nowhere else. A pgx connection error is the
// DSN in prose — `failed to connect to user=flanj database=flanj … lookup
// postgres …` — and the read routes used to hand that verbatim to the browser
// as `{"error": "<the whole thing>"}`, on unauthenticated localhost GETs, while
// every mutating route answered the one fixed sentence. The operator learns
// nothing from the DSN they cannot read off their own config; a log line is
// where it belongs.
//
// 503, not 500: the store is a dependency that is down, not a bug in the
// request — and it is the status storeOrError already answered for the same
// condition.
func (e *uiExtension) storeErr(w http.ResponseWriter, op string, err error) {
	e.telemetry.Logger.Warn("read api: " + op + ": " + err.Error())
	writeErr(w, http.StatusServiceUnavailable, "store_error", msgStoreUnavailable)
}

// handleHealth reports the divergence headline inputs: store fill + counts.
func (e *uiExtension) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	rows, bytes, err := st.Stats()
	if err != nil {
		e.storeErr(w, "store stats", err)
		return
	}
	calls, findings, _ := st.Counts()
	// Connect status from the store only (no CP call on the health poll).
	connect := "disconnected"
	collectorName := ""
	if cs, err := loadConnect(st); err == nil {
		connect = cs.status()
		collectorName = cs.CollectorName
	}
	out := map[string]any{
		"status":         "ok",
		"window_rows":    rows,
		"window_bytes":   bytes,
		"calls":          calls,
		"findings":       findings,
		"cp_configured":  e.cp != nil,
		"connect_status": connect,
		// The deployment's NAME (2026-09-14) — what the Overview headline names
		// and what the dashboard lists. Empty until Connect; it replaced the
		// static `integration` slug, which named a REST integration from config.
		"collector_name":    collectorName,
		"collector_version": collectorVersion,
		// Did this collector hold data before the light-default upgrade? The
		// second of the two gates on the one-time theme-flip notice
		// — the first is "this browser has no stored theme
		// choice", which only the browser can answer.
		"held_prior_data": heldPriorData(st),
		// No organization name here (2026-09-19): the configured
		// consumer_display_name is deprecated and ignored, and the workspace's
		// display name — the only org name this collector shows — rides
		// GET /api/connect, once the control plane has told us.
		// Does this pod serve its contracts to FRONT collectors? The Contracts
		// card needs it to say anything about the 8 MiB document cap, which
		// applies to that hop and to no other: on a single pod the same
		// oversized document is read in-process, bound, and validating.
		"serves_fronts": e.servesFronts(),
	}
	// Pre-traffic honesty: `provider_display_name` describes a
	// DISCOVERY, so it emits only once at least one external outbound edge (or
	// a finding) exists — never from bare config at zero traffic. (The
	// `integration` slug that rode beside it is gone with the config key.)
	if hasObservedProvider(st, findings) {
		out["provider_display_name"] = e.cfg.ProviderDisplayName
	}
	writeJSON(w, http.StatusOK, out)
}

// hasObservedProvider reports whether this collector has anything real to
// attach a provider identity to: ≥1 external outbound edge, or ≥1 finding.
func hasObservedProvider(st store.Store, findings int) bool {
	if findings > 0 {
		return true
	}
	edges, err := st.ListEdges(true)
	if err != nil {
		return false
	}
	for _, ed := range edges {
		if ed.Direction == edge.DirectionClient {
			return true
		}
	}
	return false
}

// edgeWithRPM decorates a discovered edge with its observed request rate
// (calls captured over the trailing 60 seconds, i.e. calls/minute) and its
// naming fields: the registrable domain (the naming key), the
// resolved display name and its provenance (`user | config | directory |
// auto`). display_name is empty when the source is auto (the UI humanizes the
// host itself). Names resolve for OUTBOUND rows only; inbound rows carry the
// domain but always source auto (inbound naming is deferred).
type edgeWithRPM struct {
	model.Edge
	RPM               float64 `json:"rpm"`
	RegistrableDomain string  `json:"registrable_domain"`
	DisplayName       string  `json:"display_name"`
	NameSource        string  `json:"name_source"`
}

// decorateEdge builds the API row for one discovered edge.
func decorateEdge(ed model.Edge, rpm float64, names nameResolver) edgeWithRPM {
	er := edgeWithRPM{Edge: ed, RPM: rpm, NameSource: nameSourceAuto}
	er.RegistrableDomain = edge.RegistrableDomain(ed.PeerHost)
	if ed.Direction == edge.DirectionClient {
		er.DisplayName, er.NameSource = names.resolve(ed.PeerHost, er.RegistrableDomain)
	}
	return er
}

// edgeRows builds the decorated external-edge rows GET /api/edges returns, in
// store order. It is the ONE builder for that shape: the agent MCP surface
// (mcp.go) reads edges through this function too, so "the agent and the human
// see the same truth" is structural rather than a promise two call sites make
// separately. The failing store op is named for storeErr.
func (e *uiExtension) edgeRows(st store.Store) ([]edgeWithRPM, string, error) {
	edges, err := st.ListEdges(true) // externalOnly
	if err != nil {
		return nil, "list edges", err
	}
	since := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05Z")
	counts, err := st.EdgeCallCountsSince(since)
	if err != nil {
		return nil, "edge call counts", err
	}
	// One resolution context per REQUEST — never a lookup per edge, and never
	// a CP call from here (the directory tier reads only the KV + baked seed).
	names := e.newNameResolver(st)
	all := make([]edgeWithRPM, 0, len(edges))
	for _, ed := range edges {
		all = append(all, decorateEdge(ed, float64(counts[ed.PeerHost+"|"+ed.Direction]), names))
	}
	return all, "", nil
}

// handleEdges returns the discovered EXTERNAL edges (inbound + outbound), each
// carrying an observed RPM over the trailing minute. Internal same-team edges
// are classified out of surfacing and never returned here.
func (e *uiExtension) handleEdges(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	all, op, err := e.edgeRows(st)
	if err != nil {
		e.storeErr(w, op, err)
		return
	}
	outbound := make([]edgeWithRPM, 0)
	inbound := make([]edgeWithRPM, 0)
	for _, er := range all {
		if er.Direction == "server" {
			inbound = append(inbound, er)
		} else {
			outbound = append(outbound, er)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"edges":    all,
		"outbound": outbound,
		"inbound":  inbound,
	})
}

// edgeNameRequestBody is POST /api/edges/name: rename ({host, name}) or clear
// ({host, name: ""}) an OUTBOUND edge, keyed by the host's registrable domain.
// suggest=true additionally sends the mapping to the Flanj directory — the
// per-mapping OPT-IN (default false; the UI checkbox is unchecked by default).
type edgeNameRequestBody struct {
	Host    string `json:"host"`
	Name    string `json:"name"`
	Suggest bool   `json:"suggest"`
}

// edgeNameMaxLen caps a display name (in runes) before persist.
const edgeNameMaxLen = 80

// handleEdgeName saves, or clears, the `user` name for the outbound edge whose
// host maps to a registrable domain. The value passes the SAME redaction floor
// Connect display names pass before persist. When suggest is set AND the
// collector is Connected, the mapping is POSTed to the CP directory — and a
// submission failure NEVER fails the save: the answer is still 200 with
// suggested=false and the distinct copy string. Nothing is ever sent when
// suggest is false.
func (e *uiExtension) handleEdgeName(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	var body edgeNameRequestBody
	if !readJSONBody(w, r, maxSmallBodyBytes, &body, "request_too_large", msgRequestTooLarge) {
		return
	}
	body.Host = strings.TrimSpace(body.Host)
	if body.Host == "" {
		writeErr(w, http.StatusBadRequest, "missing_fields", msgEdgeHostRequired)
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	domain := edge.RegistrableDomain(body.Host)
	// Outbound rows only: the host must map to a discovered external OUTBOUND
	// edge (inbound naming is deferred; internal edges never surface at all).
	edges, err := st.ListEdges(true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	var target *model.Edge
	for i := range edges {
		if edges[i].Direction == edge.DirectionClient && edge.RegistrableDomain(edges[i].PeerHost) == domain {
			target = &edges[i]
			break
		}
	}
	if domain == "" || target == nil {
		writeErr(w, http.StatusNotFound, "edge_not_found", msgEdgeNotFound)
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		// Clear: tombstone + index removal; the row returns to the next tier.
		if err := deleteEdgeName(st, domain); err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
		names := e.newNameResolver(st)
		writeJSON(w, http.StatusOK, map[string]any{
			"saved":     true,
			"suggested": false,
			"edge":      decorateEdge(*target, 0, names),
		})
		return
	}
	// The same redaction floor Connect display names pass (connect.go), plus a
	// length cap — the value persists and may (opt-in) leave the collector.
	name = strings.TrimSpace(redact.New().Redact(name).Text)
	if utf8.RuneCountInString(name) > edgeNameMaxLen {
		writeErr(w, http.StatusBadRequest, "name_too_long", msgNameTooLong)
		return
	}
	if name == "" {
		// The redaction floor consumed the whole value — refuse rather than
		// persist an empty `user` record (an explicit clear posts name: "").
		writeErr(w, http.StatusBadRequest, "name_empty", msgNameEmpty)
		return
	}
	if err := putEdgeName(st, domain, name, nameSourceUser); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}

	// The OPT-IN suggestion — only ever on suggest=true, only when Connected,
	// and never fatal to the save that already landed.
	suggested := false
	suggestMsg := ""
	if body.Suggest {
		cs, csErr := loadConnect(st)
		if csErr == nil && cs.CollectorKey != "" && e.cp != nil {
			_, sErr := e.keyedClient(cs).SubmitDirectoryName(r.Context(), promote.DirectorySubmissionRequest{Domain: domain, Name: name})
			switch {
			case sErr == nil:
				suggested = true
			default:
				// A CP 400 is the directory's name normalizer REFUSING the
				// suggestion — a definitive answer, not a transport failure:
				// relay the CP's one-sentence reason. Everything else keeps
				// the it-stays-local copy.
				if ce := promote.AsCPError(sErr); ce != nil && ce.Status == http.StatusBadRequest && ce.Message != "" {
					suggestMsg = msgNameSuggestRefused(ce.Message)
				} else {
					suggestMsg = msgNameSavedSuggestFailed
				}
			}
		} else {
			suggestMsg = msgNameSavedSuggestFailed
		}
	}
	names := e.newNameResolver(st)
	out := map[string]any{
		"saved":     true,
		"suggested": suggested,
		"edge":      decorateEdge(*target, 0, names),
	}
	if suggestMsg != "" {
		out["message"] = suggestMsg
	}
	writeJSON(w, http.StatusOK, out)
}

// handleContracts returns the provider contracts (specs) the drift processor
// has loaded — what the Contract tab renders per provider: the REST contracts
// and the self row, then the observed MCP catalogues (format "mcp"). A host
// with both is two rows sharing an integration, told apart by format.
func (e *uiExtension) handleContracts(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	infos, err := store.ListContractsAndCatalogues(st)
	if err != nil {
		e.storeErr(w, "list contracts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contracts": infos})
}

// handleContractSpec serves the raw document for one row
// (?integration=…&format=openapi|mcp), exactly as the drift processor loaded
// it. The format picks the row: a REST contract and an MCP catalogue for one
// host share an integration. The UI always sends it; a request without one
// gets the REST contract when the host has one and the MCP catalogue
// otherwise — what that request meant before the two could coexist.
func (e *uiExtension) handleContractSpec(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	integration := r.URL.Query().Get("integration")
	format := r.URL.Query().Get("format")
	if format != "" && format != model.SpecFormatOpenAPI && format != model.SpecFormatMCP {
		writeErr(w, http.StatusBadRequest, "format_invalid", msgContractFormatInvalid)
		return
	}
	var (
		raw []byte
		ok  bool
		err error
	)
	if format == "" {
		if raw, _, ok, err = st.GetSpecDoc(integration); err == nil && !ok {
			raw, ok, err = st.GetMCPCatalogueDoc(integration)
		}
	} else {
		raw, ok, err = store.GetDoc(st, integration, format)
	}
	if err != nil {
		e.storeErr(w, "get contract document", err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "contract_not_found", msgContractNotLoaded)
		return
	}
	ct := "application/yaml"
	if len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '{' {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (e *uiExtension) handleCalls(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	calls, err := st.ListCalls(200)
	if err != nil {
		e.storeErr(w, "list calls", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": calls})
}

// handleCall serves one stored call by id; 404 when the store holds none.
func (e *uiExtension) handleCall(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	call, ok, err := st.GetCall(r.PathValue("id"))
	if err != nil {
		e.storeErr(w, "get call", err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "call_not_found", msgCallNotFound)
		return
	}
	writeJSON(w, http.StatusOK, call)
}

// findingView decorates a stored finding for the UI with its LOCAL ack state
// and the host of its source call — read-API joins only. model.Finding itself
// gains neither (it mirrors the frozen schema and is what promotes to the CP;
// the ack never leaves).
type findingView struct {
	model.Finding
	Acked   bool   `json:"acked,omitempty"`
	AckedAt string `json:"acked_at,omitempty"`
	// AckedEvidenceVersion is the evidence hash the ack covers (the AFTER
	// snapshot hash on a definition_change; absent otherwise). Surfaced so the
	// SPA can apply the SAME match rule client-side — a second, independent
	// check that a new change can never inherit an old acknowledgement.
	AckedEvidenceVersion string `json:"acked_evidence_version,omitempty"`
	// PeerHost is the provider host this finding is ABOUT: the peer host of its
	// pinned source call, resolved here rather than in the browser.
	//
	// The Contracts tab pairs a finding with the contract card for its host,
	// because host is the only thing the two genuinely share — an uploaded
	// contract's integration id is derived from the host it binds to while a
	// finding's integration comes from the call, stamped by the SDK. The SPA
	// used to resolve that host by looking the source call up in GET /api/calls,
	// which returns the 200 newest rows: source_call_id is frozen at the FIRST
	// occurrence, so once that call aged out of the page the finding detached
	// from its own contract and rendered a second card claiming "No contract for
	// this provider" — under a BREAKING verdict only that contract could have
	// produced. The store still holds the call (a finding pins it), so the join
	// belongs here, where the whole store is in reach.
	//
	// Empty when the finding is call-less (version-diff, MCP definition_change)
	// or its call really is gone; the SPA falls back to integration then.
	// model.Finding itself never gains the field — it mirrors the frozen schema
	// and is what promotes to the CP.
	PeerHost string `json:"peer_host,omitempty"`
}

// findingPeerHosts resolves the peer host of every finding's source call in one
// store lookup — never one per finding, on a route the SPA polls.
func findingPeerHosts(st store.Store, findings []model.Finding) (map[string]string, error) {
	ids := make([]string, 0, len(findings))
	for _, f := range findings {
		if f.SourceCallID != nil && *f.SourceCallID != "" {
			ids = append(ids, *f.SourceCallID)
		}
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	return st.CallPeerHosts(ids)
}

// findingRows builds the decorated finding rows GET /api/findings returns.
// Like edgeRows it is the ONE builder for that shape — the agent MCP surface
// reads findings through it, so a change to the ack join or the peer-host join
// cannot land on the human surface and miss the agent one. The failing store op
// is named for storeErr.
func (e *uiExtension) findingRows(st store.Store) ([]findingView, string, error) {
	findings, err := st.ListFindings(200)
	if err != nil {
		return nil, "list findings", err
	}
	acks, err := loadAckSet(st)
	if err != nil {
		return nil, "load acknowledgements", err
	}
	hosts, err := findingPeerHosts(st, findings)
	if err != nil {
		return nil, "resolve finding hosts", err
	}
	views := make([]findingView, len(findings))
	for i, f := range findings {
		views[i] = findingView{Finding: f}
		if f.SourceCallID != nil {
			views[i].PeerHost = hosts[*f.SourceCallID]
		}
		// ackMatches is what makes the acknowledged band's promise true: a
		// definition_change whose after-snapshot hash has moved on is NOT
		// covered by the old record and comes back un-acknowledged.
		if rec, ok := acks[findingSignature(f)]; ok && ackable(f) && ackMatches(f, rec) {
			views[i].Acked = true
			views[i].AckedAt = rec.AckedAt
			views[i].AckedEvidenceVersion = rec.EvidenceVersion
		}
	}
	return views, "", nil
}

func (e *uiExtension) handleFindings(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	views, op, err := e.findingRows(st)
	if err != nil {
		e.storeErr(w, op, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"findings": views})
}

// flagRequestBody is the UI -> collector flag payload (not the CP contract body,
// which the collector assembles from the stored call + finding). v0.1a: no
// email — the consumer copies the Thread link; an `invitee_email` from an old
// UI build is accepted and ignored.
type flagRequestBody struct {
	FindingID string `json:"finding_id"`
	// ProviderDisplayName is DEPRECATED (2026-09-19): accepted and IGNORED. It
	// was an older UI's per-flag override of the provider name; the control
	// plane now names the provider itself (verified domain ownership, else a
	// verified directory name, else the domain), so this collector no longer
	// forwards any provider name on a flag. The field stays decodable so an
	// older UI build keeps working.
	ProviderDisplayName string `json:"provider_display_name"`
	Message             string `json:"message"`
	// AllowedDomains is the sheet's "Open to" choice, REQUIRED: a list of email
	// domains, or JSON null for "Anyone with the link". Kept raw so an absent
	// field and an explicit null stay distinguishable — see allowedDomainsOf.
	AllowedDomains json.RawMessage `json:"allowed_domains"`
	// AllowedEmails is the other half of the choice (three modes, 2026-09-15):
	// exact addresses. At least one of the two keys must be present.
	AllowedEmails json.RawMessage `json:"allowed_emails"`
}

// humanizeIntegration turns an integration id into a human display name
// ("acme-payments" -> "Acme Payments"). Shared humanize rule; delegates to the
// canonical implementation in internal/promote.
func humanizeIntegration(id string) string { return promote.HumanizeIntegration(id) }

// requireConnectedForThread is the Connect gate every thread-CREATING route
// shares: a key AND a confirmed contact. The gate is "a confirmed contact
// exists" (`confirmed_contact_email` from `me`), not "the latest contact is
// confirmed" — while a NEW email is pending the previously confirmed one keeps
// Create thread available. With none confirmed yet the CP is
// re-asked right now (bypassing the me-cache) so Create thread works the moment
// the confirmation click lands.
//
// One implementation, so the flag sheet and the edge "Start a thread" sheet
// answer with the SAME 412s: the sheet unlocks on one code path, and a gate that
// drifted between the two doors would be a gate on one of them.
func (e *uiExtension) requireConnectedForThread(w http.ResponseWriter, r *http.Request, st store.Store) (connectState, *promote.Client, bool) {
	cs, err := loadConnect(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return cs, nil, false
	}
	if cs.CollectorKey == "" {
		writeErr(w, http.StatusPreconditionFailed, "not_connected", msgNotConnected)
		return cs, nil, false
	}
	if !cs.hasConfirmedContact() {
		cs, _ = e.refreshConnect(r.Context(), st, cs, true)
		if !cs.hasConfirmedContact() {
			writeErr(w, http.StatusPreconditionFailed, "contact_unconfirmed", contactUnconfirmedMessage(cs.ContactEmail))
			return cs, nil, false
		}
	}
	return cs, e.keyedClient(cs), true
}

// handleFlag = Create thread: requires a Connected collector with a confirmed
// contact (412 not_connected | contact_unconfirmed otherwise — viewing local data
// never does), promotes the redacted failing call + finding to the control plane
// with the collector key, persists the thread record (so the finding chip
// survives reloads) and marks the call promoted (evict-after-promote).
func (e *uiExtension) handleFlag(w http.ResponseWriter, r *http.Request) {
	if !e.guardMutating(w, r) {
		return
	}
	var body flagRequestBody
	// The flag message is free text the operator types into a textarea with no
	// length limit, so this is the one small envelope a person can overflow
	// without meaning to — by pasting a log. It must say "too large", not "not
	// valid JSON".
	if !readJSONBody(w, r, maxSmallBodyBytes, &body, "request_too_large", msgRequestTooLarge) {
		return
	}
	if body.FindingID == "" {
		writeErr(w, http.StatusBadRequest, "missing_fields", msgFindingRequired)
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}

	cs, cli, ok := e.requireConnectedForThread(w, r, st)
	if !ok {
		return
	}

	finding, ok, err := st.GetFinding(body.FindingID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "finding_not_found", msgFindingNotFound)
		return
	}
	// Evidence rule (amended qfix2-2026-08-26), enforced SERVER-SIDE —
	// not just by UI absence: stale_client is consumer-side and never leaves
	// this collector as a flag. It is the only local-only kind. A second
	// refusal on another axis (2026-09-17): an info finding, of
	// any kind, stays local too. Same code, each with a sentence true of it.
	if !finding.Flaggable() {
		msg := msgNotFlaggable
		if finding.Kind != model.KindStaleClient {
			msg = msgInfoNotFlaggable
		}
		writeErr(w, http.StatusForbidden, "not_flaggable", msg)
		return
	}
	// CALL-LESS flagging. qfix2-2026-08-26 lifted
	// 400 finding_has_no_call for a definition_change — no failing call exists
	// by nature; the evidence is the provider's own tools/list, before and after.
	// v1p4-2026-09-08 widens it to EVERY
	// kind, because the reason it was narrow was never good: a version-diff has
	// no source call by construction either, and answering its Flag control with
	// a 400 is worse than having no control. `call` is optional on the wire
	// whenever the request carries a message, and this relay always sends one —
	// the sheet prefills it and promote.Build substitutes a default for an
	// emptied textarea. So a finding that simply HAS no source call now flags.
	//
	// An EVICTED call is a different thing and still refuses: the sheet showed
	// the operator an "Evidence (1)" line for that call, and quietly turning
	// their flag into a call-less one would create a thread they did not mean to
	// create. Only a definition_change is exempt (it never had a call to lose).
	var call *model.RedactedCall
	if finding.SourceCallID != nil && *finding.SourceCallID != "" {
		c, ok, err := st.GetCall(*finding.SourceCallID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
		if !ok && finding.Kind != model.KindDefinitionChange {
			writeErr(w, http.StatusNotFound, "call_not_found", msgCallEvicted)
			return
		}
		if ok {
			call = &c
		}
	}

	// The org name on the wire is the workspace's display name as this
	// collector last read it from the control plane, or nothing (the field is
	// omitted) — never the deprecated config key. The control plane names the
	// sender from the workspace either way.
	consumerName := cs.WorkspaceDisplayName
	// providerName is LOCAL ONLY (2026-09-19) — it never rides the flag
	// (promote.FlagRequest carries no provider_display_name field at all; the
	// control plane names the provider itself, from verified domain ownership,
	// else a verified directory name, else the domain). It only seeds this
	// collector's OWN thread record (rec.Provider below) as a placeholder for
	// the Threads tab until the control plane's resolved name arrives on the
	// next list poll. `body.ProviderDisplayName` (an older UI's per-flag
	// override) and `e.cfg.ProviderDisplayName` (the deprecated config key) are
	// both accepted for compatibility and otherwise IGNORED — neither
	// influences this placeholder or anything sent onward.
	integration := finding.Integration
	if call != nil {
		integration = call.Integration
	}
	providerName := humanizeIntegration(integration)
	// Who may open the thread — the last local check before anything leaves.
	// Refused HERE so the operator reads it in the sheet; the CP refuses the
	// same shapes independently.
	allowedDomains, allowedEmails, refuseCode, refuseMsg := openToOf(body.AllowedDomains, body.AllowedEmails)
	if refuseCode != "" {
		writeErr(w, http.StatusBadRequest, refuseCode, refuseMsg)
		return
	}
	req := promote.Build(promote.Input{
		ConsumerDisplayName: consumerName,
		Message:             body.Message,
		Call:                call,
		Finding:             finding,
		AllowedDomains:      allowedDomains,
		AllowedEmails:       allowedEmails,
	})

	resp, code, err := cli.Post(r.Context(), req)
	if err != nil {
		if ce := promote.AsCPError(err); ce != nil && ce.Status == http.StatusPreconditionFailed {
			// The CP disagrees with our cached state — fold it back in.
			if ce.Code == "contact_unconfirmed" {
				cs.ContactStatus, cs.ConfirmedAt, cs.ConfirmedContactEmail = contactPending, "", ""
				_ = saveConnect(st, cs)
				writeErr(w, http.StatusPreconditionFailed, ce.Code, contactUnconfirmedMessage(cs.ContactEmail))
				return
			}
			writeErr(w, http.StatusPreconditionFailed, "not_connected", msgNotConnected)
			return
		}
		writeCPError(w, err, msgCPUnreachableFlag)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec, existed, _ := loadThread(st, finding.ID)
	if !existed {
		rec = threadRecord{FindingID: finding.ID, CreatedAt: now}
	}
	rec.ThreadID = resp.ThreadID
	rec.ThreadPublicID = resp.ThreadPublicID
	rec.Endpoint = finding.Endpoint
	rec.Provider = providerName
	rec.Integration = finding.Integration
	if resp.ThreadURL != "" {
		rec.ThreadURL = resp.ThreadURL
	}
	rec.UpdatedAt = now
	if err := saveThread(st, rec); err != nil {
		e.telemetry.Logger.Warn("flag succeeded but persisting the thread record failed: " + err.Error())
	}
	// evict-after-promote: unpin + stamp promoted_at so the call re-enters the
	// pool. A call-less flag has nothing to unpin.
	if call != nil {
		if err := st.MarkPromoted(call.ID); err != nil {
			e.telemetry.Logger.Warn("flag succeeded but mark-promoted failed: " + err.Error())
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, code, map[string]any{
		"thread_id":        resp.ThreadID,
		"thread_public_id": resp.ThreadPublicID,
		"thread_url":       resp.ThreadURL,
		"state":            resp.State,
		"status":           resp.Status,
		"finding_id":       finding.ID,
	})
}

// edgeThreadRequestBody is the UI -> collector payload for "Start a thread" on
// an edge row. No finding id: an edge is a registrable domain, not
// a drift, so there is nothing local to attach.
//
// RequestID is minted by the SHEET, once, when it opens — it is what makes the
// idempotency key stable across a retry after a failed create. The collector
// cannot mint it here: this handler is stateless, so a retry would land on a
// fresh key and a second thread. Two DIFFERENT questions about one edge are two
// threads, which is why the key is not derived from the host.
type edgeThreadRequestBody struct {
	Host      string `json:"host"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	// AllowedDomains: the sheet's "Open to" choice, REQUIRED — see flagRequestBody.
	AllowedDomains json.RawMessage `json:"allowed_domains"`
	AllowedEmails  json.RawMessage `json:"allowed_emails"`
}

// handleEdgeThread = Start a thread from an edge row (CONTRACTS §5):
// a MESSAGE-ONLY thread. Same Connect gate as a flag (the same 412s, from the
// same helper), same relay, same thread record — the only difference is what is
// on the wire: no call, no finding, `provider_host` naming the edge so the
// thread page can resolve a verified name for its provider slot.
//
// The message is REQUIRED here, before the round-trip: it is the entire artifact.
// The CP enforces the same rule (400 finding_has_no_call), but a sheet that shows
// its own refusal beats one that relays a control-plane error for a field the
// operator can see.
func (e *uiExtension) handleEdgeThread(w http.ResponseWriter, r *http.Request) {
	if !e.guardMutating(w, r) {
		return
	}
	var body edgeThreadRequestBody
	// Same cap and same "too large" refusal as the flag message: a person can
	// overflow this textarea by pasting a log without meaning to.
	if !readJSONBody(w, r, maxSmallBodyBytes, &body, "request_too_large", msgRequestTooLarge) {
		return
	}
	body.Host = strings.TrimSpace(body.Host)
	if body.Host == "" {
		writeErr(w, http.StatusBadRequest, "missing_fields", msgEdgeHostRequired)
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeErr(w, http.StatusBadRequest, "missing_fields", msgEdgeThreadMessageRequired)
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	cs, cli, ok := e.requireConnectedForThread(w, r, st)
	if !ok {
		return
	}

	// The host must resolve to a discovered external OUTBOUND edge — the same
	// rule the rename route enforces, for the same reason: inbound `peer_host`
	// is a forgeable XFF first hop and is never identity, and an
	// internal edge never crosses the org boundary at all.
	edges, err := st.ListEdges(true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	domain := edge.RegistrableDomain(body.Host)
	var target *model.Edge
	for i := range edges {
		if edges[i].Direction == edge.DirectionClient && edge.RegistrableDomain(edges[i].PeerHost) == domain {
			target = &edges[i]
			break
		}
	}
	if domain == "" || target == nil {
		writeErr(w, http.StatusNotFound, "edge_not_found", msgEdgeNotFound)
		return
	}

	consumerName := cs.WorkspaceDisplayName // the same rule as the flag path
	// No provider name is sent (2026-09-19, see promote.FlagRequest): the
	// control plane resolves the provider side itself off `ProviderHost` below
	// (verified domain ownership, else a verified directory name, else the
	// domain), the same way it does for a call-evidenced flag. This route used
	// to send the edge's locally-resolved name (user > contract > directory >
	// auto) alongside the host; the host alone is now sufficient and is never a
	// guess.
	//
	// Who may open the thread — the same rule, in the same place, as the flag path.
	allowedDomains, allowedEmails, refuseCode, refuseMsg := openToOf(body.AllowedDomains, body.AllowedEmails)
	if refuseCode != "" {
		writeErr(w, http.StatusBadRequest, refuseCode, refuseMsg)
		return
	}
	req := promote.BuildQuestion(promote.QuestionInput{
		IdempotencyKey:      edgeThreadIdempotencyKey(body.RequestID, target.PeerHost),
		ConsumerDisplayName: consumerName,
		ProviderHost:        target.PeerHost,
		Message:             body.Message,
		AllowedDomains:      allowedDomains,
		AllowedEmails:       allowedEmails,
	})

	resp, code, err := cli.Post(r.Context(), req)
	if err != nil {
		if ce := promote.AsCPError(err); ce != nil && ce.Status == http.StatusPreconditionFailed {
			// The CP disagrees with our cached state — fold it back in, exactly
			// as the flag path does.
			if ce.Code == "contact_unconfirmed" {
				cs.ContactStatus, cs.ConfirmedAt, cs.ConfirmedContactEmail = contactPending, "", ""
				_ = saveConnect(st, cs)
				writeErr(w, http.StatusPreconditionFailed, ce.Code, contactUnconfirmedMessage(cs.ContactEmail))
				return
			}
			writeErr(w, http.StatusPreconditionFailed, "not_connected", msgNotConnected)
			return
		}
		writeCPError(w, err, msgCPUnreachableFlag)
		return
	}
	// There is no finding to key a thread.finding.<id> record on, so the link is
	// parked under thread.link.<thread_id> — the existing key for a thread this
	// collector holds no finding record for. One blind PutSetting, no
	// read-modify-write, and the Threads tab still lists the row (the LIST comes
	// from the CP) with a working Copy thread link.
	if resp.ThreadURL != "" {
		if err := st.PutSetting(settingThreadLinkPrefix+resp.ThreadID, resp.ThreadURL); err != nil {
			e.telemetry.Logger.Warn("thread created but persisting its link failed: " + err.Error())
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, code, map[string]any{
		"thread_id":        resp.ThreadID,
		"thread_public_id": resp.ThreadPublicID,
		"thread_url":       resp.ThreadURL,
		"state":            resp.State,
		"status":           resp.Status,
	})
}

// edgeThreadIdempotencyKey namespaces the sheet's request id so a question can
// never collide with a flag's `flag_<finding_id>` key. A missing, blank or
// over-long request id (an older UI build, or a hand-made request) falls back to
// a fresh random one: the caller loses retry-safety, which is theirs to lose, but
// two questions never silently become ONE thread — which is exactly what a
// host-derived key would do to a second question about the same edge.
func edgeThreadIdempotencyKey(requestID, host string) string {
	id := strings.TrimSpace(requestID)
	if id == "" || utf8.RuneCountInString(id) > 64 {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			// crypto/rand failing is not a reason to drop the operator's
			// question; a time-based key still never collides with a live one.
			id = "t" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
		} else {
			id = hex.EncodeToString(b[:])
		}
	}
	return "edge_" + host + "_" + id
}

// handleDirectoryHint answers the sheet's "Open to" prefill question for one
// host: its registrable domain, and whether the local directory table holds a
// CLAIMED entry for it (D5 domain proof — someone at that domain proved they
// control it, which is what makes prefilling it as the share domain honest; a
// curated name is a Flanj-reviewed label and proves nothing about a mailbox).
// A pure read of the seed + the last pulled table — the directory is never
// queried per request, and nothing about this collector leaves.
func (e *uiExtension) handleDirectoryHint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only.")
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	domain := edge.RegistrableDomain(host)
	if host == "" || domain == "" {
		writeErr(w, http.StatusBadRequest, "missing_fields", msgEdgeHostRequired)
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	entry, ok := loadDirectory(st)[domain]
	out := map[string]any{"host": host, "domain": domain, "name": nil, "tier": nil, "claimed": false}
	if ok {
		out["name"] = entry.Name
		out["tier"] = entry.Tier
		out["claimed"] = entry.Tier == "claimed"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// contactUnconfirmedMessage is the fixed 412 line, naming the pending address.
func contactUnconfirmedMessage(email string) string {
	if email == "" {
		return msgContactUnconfirmed
	}
	return "Confirm " + email + " first — we sent \"Confirm your Flanj contact\"."
}

// spaHandler serves the embedded Vue SPA, falling back to index.html so client
// routes resolve.
func (e *uiExtension) spaHandler() http.Handler {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI assets missing", http.StatusInternalServerError)
		})
	}
	return spaHandlerFS(sub)
}

// spaHandlerFS is spaHandler over any filesystem, so the fallback rule is
// testable without a built bundle. A path that exists is served as the file
// it is (index.html, the hashed assets, favicon.svg). A path that does not
// exist falls back to the SPA entrypoint ONLY when it looks like a client
// route: a request whose last segment carries a file extension — /favicon.ico
// from a browser that ignores <link rel="icon">, a stale hashed asset, a
// missing image — gets a plain 404. Answering those with index.html at 200
// handed the browser an HTML document as its tab icon and hid every broken
// asset reference behind a green status.
func spaHandlerFS(sub fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); err != nil && r.URL.Path != "/" {
			if looksLikeFile(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			// Unknown extension-less path -> SPA entrypoint.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// looksLikeFile reports whether the request path's last segment has a file
// extension — the shape of an asset request rather than a client route.
func looksLikeFile(p string) bool {
	return path.Ext(path.Base(p)) != ""
}

func trimLeadingSlash(p string) string {
	if p == "/" || p == "" {
		return "index.html"
	}
	if p[0] == '/' {
		return p[1:]
	}
	return p
}
