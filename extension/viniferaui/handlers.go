package viniferaui

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/vinifera-io/collector/internal/promote"
	"github.com/vinifera-io/collector/internal/store"
)

// routes builds the UI handler: read API under /api/*, the embedded SPA elsewhere.
func (e *uiExtension) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", e.handleHealth)
	mux.HandleFunc("/api/edges", e.handleEdges)
	mux.HandleFunc("/api/calls", e.handleCalls)
	mux.HandleFunc("/api/findings", e.handleFindings)
	mux.HandleFunc("/api/flag", e.handleFlag)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"integration":       e.cfg.IntegrationID,
		"window_rows":       rows,
		"window_bytes":      bytes,
		"calls":             calls,
		"findings":          findings,
		"cp_configured":     e.cp != nil,
		"collector_version": collectorVersion,
	})
}

// handleEdges returns the discovered EXTERNAL edges (inbound + outbound). Internal
// same-team edges are classified out of surfacing and never returned here.
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
	outbound := make([]any, 0)
	inbound := make([]any, 0)
	for _, ed := range edges {
		if ed.Direction == "server" {
			inbound = append(inbound, ed)
		} else {
			outbound = append(outbound, ed)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"edges":    edges,
		"outbound": outbound,
		"inbound":  inbound,
	})
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
	writeJSON(w, http.StatusOK, map[string]any{"findings": findings})
}

// flagRequestBody is the UI -> collector flag payload (not the CP contract body,
// which the collector assembles from the stored call + finding).
type flagRequestBody struct {
	FindingID           string `json:"finding_id"`
	InviteeEmail        string `json:"invitee_email"`
	ConsumerDisplayName string `json:"consumer_display_name"`
	ProviderDisplayName string `json:"provider_display_name"`
	Message             string `json:"message"`
}

// humanizeIntegration turns an integration id into a human display name
// ("acme-payments" -> "Acme Payments"). Shared humanize rule; delegates to the
// canonical implementation in internal/promote.
func humanizeIntegration(id string) string { return promote.HumanizeIntegration(id) }

// handleFlag promotes the failing call + finding to the control plane, then marks
// the call promoted (evict-after-promote) on success.
func (e *uiExtension) handleFlag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if e.cp == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "control plane not configured (set cp_base_url + cp_deploy_token)"})
		return
	}
	var body flagRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.FindingID == "" || body.InviteeEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "finding_id and invitee_email are required"})
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}

	finding, ok, err := st.GetFinding(body.FindingID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "finding not found"})
		return
	}
	if finding.SourceCallID == nil || *finding.SourceCallID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "finding has no source call to flag (version-diff findings are informational)"})
		return
	}
	call, ok, err := st.GetCall(*finding.SourceCallID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "source call not found (may have been evicted)"})
		return
	}

	consumerName := body.ConsumerDisplayName
	if consumerName == "" {
		consumerName = e.cfg.ConsumerDisplayName
	}
	// Provider name names the flagged side on the peek/thread. Prefer the UI
	// override, then the configured provider_display_name, then the humanized
	// integration id (CONTRACTS §5/§8).
	providerName := body.ProviderDisplayName
	if providerName == "" {
		providerName = e.cfg.ProviderDisplayName
	}
	if providerName == "" {
		providerName = humanizeIntegration(e.cfg.IntegrationID)
	}
	req := promote.Build(promote.Input{
		ConsumerDisplayName: consumerName,
		ProviderDisplayName: providerName,
		InviteeEmail:        body.InviteeEmail,
		Message:             body.Message,
		Call:                call,
		Finding:             finding,
	})

	resp, code, err := e.cp.Post(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "control-plane flag failed: " + err.Error()})
		return
	}
	// evict-after-promote: unpin + stamp promoted_at so the call re-enters the pool.
	if err := st.MarkPromoted(call.ID); err != nil {
		e.telemetry.Logger.Warn("flag succeeded but mark-promoted failed: " + err.Error())
	}
	writeJSON(w, code, resp)
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
