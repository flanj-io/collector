package flanjui

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
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
	// Edge rename (v1 phase 1) — LOCAL mutation (guarded WITHOUT the CP check:
	// naming an edge works on a disconnected collector; only the opt-in
	// directory suggestion needs a Connected one, and its failure never fails
	// the save).
	mux.HandleFunc("/api/edges/name", e.handleEdgeName)
	mux.HandleFunc("/api/calls", e.handleCalls)
	mux.HandleFunc("/api/findings", e.handleFindings)
	// Local acknowledge (never a relay route — guarded WITHOUT the CP check).
	mux.HandleFunc("/api/findings/{id}/ack", e.handleFindingAck)
	mux.HandleFunc("/api/findings/{id}/unack", e.handleFindingUnack)
	mux.HandleFunc("/api/contracts", e.handleContracts)
	mux.HandleFunc("/api/contracts/spec", e.handleContractSpec)
	mux.HandleFunc("/api/connect", e.handleConnect)
	mux.HandleFunc("/api/flag", e.handleFlag)
	mux.HandleFunc("/api/threads", e.handleThreads)
	mux.HandleFunc("/api/threads/{id}/summary", e.handleThreadSummary)
	mux.HandleFunc("/api/threads/{id}/open", e.handleThreadOpen)
	mux.HandleFunc("/api/threads/{id}/close", e.handleThreadClose)
	mux.HandleFunc("/api/threads/{id}/reopen", e.handleThreadReopen)
	mux.HandleFunc("/api/threads/{id}/replace-link", e.handleThreadReplaceLink)
	mux.Handle("/", e.spaHandler())
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// storeOrError resolves the shared store, writing a 503 and returning nil when
// it cannot be found (e.g. the store extension is not configured).
func (e *uiExtension) storeOrError(w http.ResponseWriter) store.Store {
	st := e.resolveStore()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store extension not available"})
	}
	return st
}

// handleHealth reports the divergence headline inputs: store fill + counts.
func (e *uiExtension) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	rows, bytes, err := st.Stats()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	calls, findings, _ := st.Counts()
	// Connect status from the store only (no CP call on the health poll).
	connect := "disconnected"
	if cs, err := loadConnect(st); err == nil {
		connect = cs.status()
	}
	out := map[string]any{
		"status":            "ok",
		"window_rows":       rows,
		"window_bytes":      bytes,
		"calls":             calls,
		"findings":          findings,
		"cp_configured":     e.cp != nil,
		"connect_status":    connect,
		"collector_version": collectorVersion,
		// Did this collector hold data before the light-default upgrade? The
		// second of the two gates on the one-time theme-flip notice
		// (ux-design-v2 §3.4) — the first is "this browser has no stored theme
		// choice", which only the browser can answer.
		"held_prior_data": heldPriorData(st),
		// consumer_display_name names the INSTALLER, not a discovery — it may
		// render pre-traffic (the pre-traffic honesty rule exempts it).
		"consumer_display_name": e.cfg.ConsumerDisplayName,
	}
	// Pre-traffic honesty (v1 phase 1): `integration` and
	// `provider_display_name` describe a DISCOVERY, so they emit only once at
	// least one external outbound edge (or a finding) exists — never from bare
	// config at zero traffic.
	if hasObservedProvider(st, findings) {
		out["integration"] = e.cfg.IntegrationID
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
// (calls captured over the trailing 60 seconds, i.e. calls/minute) and — v1
// phase 1 — its naming fields: the registrable domain (the naming key), the
// resolved display name and its provenance (`user | config | directory |
// auto`). display_name is empty when the source is auto (the UI humanizes the
// host itself). Names resolve for OUTBOUND rows only; inbound rows carry the
// domain but always source auto (inbound naming is deferred — ruling 6).
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
		er.DisplayName, er.NameSource = names.resolve(er.RegistrableDomain)
	}
	return er
}

// handleEdges returns the discovered EXTERNAL edges (inbound + outbound), each
// carrying an observed RPM over the trailing minute. Internal same-team edges
// are classified out of surfacing and never returned here.
func (e *uiExtension) handleEdges(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	edges, err := st.ListEdges(true) // externalOnly
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	since := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05Z")
	counts, err := st.EdgeCallCountsSince(since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// One resolution context per REQUEST — never a lookup per edge, and never
	// a CP call from here (the directory tier reads only the KV + baked seed).
	names := e.newNameResolver(st)
	all := make([]edgeWithRPM, 0, len(edges))
	outbound := make([]edgeWithRPM, 0)
	inbound := make([]edgeWithRPM, 0)
	for _, ed := range edges {
		er := decorateEdge(ed, float64(counts[ed.PeerHost+"|"+ed.Direction]), names)
		all = append(all, er)
		if ed.Direction == "server" {
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
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
// has loaded — what the Contract tab renders per provider.
func (e *uiExtension) handleContracts(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	infos, err := st.ListSpecInfos()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contracts": infos})
}

// handleContractSpec serves the raw contract document for one integration
// (?integration=...), exactly as the drift processor loaded it.
func (e *uiExtension) handleContractSpec(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	integration := r.URL.Query().Get("integration")
	raw, _, ok, err := st.GetSpecDoc(integration)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no contract loaded for integration"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": calls})
}

// findingView decorates a stored finding with its LOCAL ack state for the UI —
// a read-API join only. model.Finding itself never gains the field (it mirrors
// the frozen schema and is what promotes to the CP; the ack never leaves).
type findingView struct {
	model.Finding
	Acked   bool   `json:"acked,omitempty"`
	AckedAt string `json:"acked_at,omitempty"`
	// AckedEvidenceVersion is the evidence hash the ack covers (the AFTER
	// snapshot hash on a definition_change; absent otherwise). Surfaced so the
	// SPA can apply the SAME match rule client-side — a second, independent
	// check that a new change can never inherit an old acknowledgement
	// (ux-design-v2 §2.8 / §7 risk 2).
	AckedEvidenceVersion string `json:"acked_evidence_version,omitempty"`
}

func (e *uiExtension) handleFindings(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	findings, err := st.ListFindings(200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	acks, err := loadAckSet(st)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]findingView, len(findings))
	for i, f := range findings {
		views[i] = findingView{Finding: f}
		// ackMatches is what makes the acknowledged band's promise true: a
		// definition_change whose after-snapshot hash has moved on is NOT
		// covered by the old record and comes back un-acknowledged.
		if rec, ok := acks[findingSignature(f)]; ok && ackable(f) && ackMatches(f, rec) {
			views[i].Acked = true
			views[i].AckedAt = rec.AckedAt
			views[i].AckedEvidenceVersion = rec.EvidenceVersion
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"findings": views})
}

// flagRequestBody is the UI -> collector flag payload (not the CP contract body,
// which the collector assembles from the stored call + finding). v0.1a: no
// email — the consumer copies the Thread link; an `invitee_email` from an old
// UI build is accepted and ignored.
type flagRequestBody struct {
	FindingID           string `json:"finding_id"`
	ProviderDisplayName string `json:"provider_display_name"`
	Message             string `json:"message"`
}

// humanizeIntegration turns an integration id into a human display name
// ("acme-payments" -> "Acme Payments"). Shared humanize rule; delegates to the
// canonical implementation in internal/promote.
func humanizeIntegration(id string) string { return promote.HumanizeIntegration(id) }

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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
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

	// Connect gate: a key AND a confirmed contact. The gate is "a confirmed
	// contact exists" (`confirmed_contact_email` from `me`), not "the latest
	// contact is confirmed" — while a NEW email is pending the previously
	// confirmed one keeps Create thread available (CONTRACTS-CP §5.3). With
	// none confirmed yet the CP is re-asked right now (bypassing the me-cache)
	// so Create thread works the moment the confirmation click lands.
	cs, err := loadConnect(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	if cs.CollectorKey == "" {
		writeErr(w, http.StatusPreconditionFailed, "not_connected", msgNotConnected)
		return
	}
	if !cs.hasConfirmedContact() {
		cs, _ = e.refreshConnect(r.Context(), st, cs, true)
		if !cs.hasConfirmedContact() {
			writeErr(w, http.StatusPreconditionFailed, "contact_unconfirmed", contactUnconfirmedMessage(cs.ContactEmail))
			return
		}
	}
	cli := e.keyedClient(cs)

	finding, ok, err := st.GetFinding(body.FindingID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "finding_not_found", msgFindingNotFound)
		return
	}
	// Evidence rule (v0.5 §6, amended qfix2-2026-08-26), enforced SERVER-SIDE —
	// not just by UI absence: stale_client is consumer-side and never leaves
	// this collector as a flag. It is the only local-only kind.
	if !finding.Flaggable() {
		writeErr(w, http.StatusForbidden, "not_flaggable", msgNotFlaggable)
		return
	}
	// CALL-LESS flagging (qfix2-2026-08-26, ux-design-v2 §2.7.5): a
	// definition_change has no failing call by nature — the evidence is the
	// provider's own tools/list, before and after — so 400 finding_has_no_call
	// is lifted for it. Every other kind still needs its call: an
	// output_mismatch without one has nothing to show, and a flag control that
	// 400s is worse than no control at all.
	callLess := finding.Kind == model.KindDefinitionChange
	var call *model.RedactedCall
	if finding.SourceCallID != nil && *finding.SourceCallID != "" {
		c, ok, err := st.GetCall(*finding.SourceCallID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
		if !ok && !callLess {
			writeErr(w, http.StatusNotFound, "call_not_found", msgCallEvicted)
			return
		}
		if ok {
			call = &c
		}
	} else if !callLess {
		writeErr(w, http.StatusBadRequest, "finding_has_no_call", msgFindingNoCall)
		return
	}

	// The consumer org is the Connected one (config is the fallback for display
	// only). Provider name: UI override, then provider_display_name, then the
	// humanized integration id (CONTRACTS §5/§8).
	consumerName := cs.ConsumerDisplayName
	if consumerName == "" {
		consumerName = e.cfg.ConsumerDisplayName
	}
	providerName := body.ProviderDisplayName
	if providerName == "" {
		providerName = e.cfg.ProviderDisplayName
	}
	if providerName == "" {
		// A call-less finding names its own integration.
		integration := finding.Integration
		if call != nil {
			integration = call.Integration
		}
		providerName = humanizeIntegration(integration)
	}
	if providerName == "" {
		providerName = humanizeIntegration(e.cfg.IntegrationID)
	}
	req := promote.Build(promote.Input{
		ConsumerDisplayName: consumerName,
		ProviderDisplayName: providerName,
		Message:             body.Message,
		Call:                call,
		Finding:             finding,
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

// contactUnconfirmedMessage is the deck's 412 line, naming the pending address.
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
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); err != nil && r.URL.Path != "/" {
			// Unknown path with no matching asset -> SPA entrypoint.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
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
