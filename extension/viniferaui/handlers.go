package viniferaui

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/promote"
	"github.com/vinifera-io/collector/internal/store"
)

// routes builds the UI handler: read API under /api/*, the Connect + thread
// relay (every mutating route goes through guardMutating), the embedded SPA
// elsewhere.
func (e *uiExtension) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", e.handleHealth)
	mux.HandleFunc("/api/edges", e.handleEdges)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"integration":       e.cfg.IntegrationID,
		"window_rows":       rows,
		"window_bytes":      bytes,
		"calls":             calls,
		"findings":          findings,
		"cp_configured":     e.cp != nil,
		"connect_status":    connect,
		"collector_version": collectorVersion,
		// Display names from config: the UI prefills Connect's org field and
		// names the provider on the Flag sheet with these.
		"consumer_display_name": e.cfg.ConsumerDisplayName,
		"provider_display_name": e.cfg.ProviderDisplayName,
	})
}

// edgeWithRPM decorates a discovered edge with its observed request rate:
// calls captured over the trailing 60 seconds, i.e. calls/minute.
type edgeWithRPM struct {
	model.Edge
	RPM float64 `json:"rpm"`
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
	all := make([]edgeWithRPM, 0, len(edges))
	outbound := make([]edgeWithRPM, 0)
	inbound := make([]edgeWithRPM, 0)
	for _, ed := range edges {
		er := edgeWithRPM{Edge: ed, RPM: float64(counts[ed.PeerHost+"|"+ed.Direction])}
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
		if rec, ok := acks[findingSignature(f)]; ok && ackable(f) {
			views[i].Acked = true
			views[i].AckedAt = rec.AckedAt
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
	// Evidence rule (v0.5 §6), enforced SERVER-SIDE — not just by UI absence:
	// local-only kinds (stale_client; DESCRIPTION-only definition changes) are
	// consumer-side or subjective and never leave this collector as a flag.
	if !finding.Flaggable() {
		writeErr(w, http.StatusForbidden, "not_flaggable", msgNotFlaggable)
		return
	}
	if finding.SourceCallID == nil || *finding.SourceCallID == "" {
		writeErr(w, http.StatusBadRequest, "finding_has_no_call", msgFindingNoCall)
		return
	}
	call, ok, err := st.GetCall(*finding.SourceCallID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "call_not_found", msgCallEvicted)
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
		providerName = humanizeIntegration(call.Integration)
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
	// evict-after-promote: unpin + stamp promoted_at so the call re-enters the pool.
	if err := st.MarkPromoted(call.ID); err != nil {
		e.telemetry.Logger.Warn("flag succeeded but mark-promoted failed: " + err.Error())
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
	return "Confirm " + email + " first — we sent \"Confirm your Vinifera contact\"."
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
