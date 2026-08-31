package flanjui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// Contract upload — the only way a provider contract enters this collector.
//
// Uploaded contracts STAY HERE. Nothing on this path talks to the control
// plane, and no code path sends a document off the host.
//
// Two shapes, in the order the operator meets them:
//
//	POST /api/contracts/preview  parse and describe. Persists NOTHING.
//	POST /api/contracts/upload   parse again, then persist.
//
// Parsing before persisting is a hard requirement, not a nicety: the drift
// processor's refresh loads whatever is in the store, so an unparseable
// document that reached a row would cost that host detection with no symptom at
// the point the operator could still fix it.
const (
	// maxDocBytes caps an uploaded document. Real OpenAPI documents run to a
	// few megabytes; this matches the ceiling the front<-store channel enforces,
	// so both ends agree on what fits.
	maxDocBytes = 8 << 20 // 8 MiB
	// maxHostLen is the DNS name ceiling.
	maxHostLen = 253
)

type uploadRequest struct {
	// PeerHost is the provider edge this contract validates. MANDATORY: a
	// contract with no host validates nothing forever while its card claims
	// otherwise, which is exactly how the old optional `peer_host` failed.
	PeerHost string `json:"peer_host"`
	// Document is the OpenAPI document itself, JSON or YAML, as text.
	Document string `json:"document"`
	// Filename is what the operator picked, kept only for the error message.
	Filename string `json:"filename"`
}

// contractPreview is what the confirm step renders before anything is written.
type contractPreview struct {
	PeerHost    string   `json:"peer_host"`
	Integration string   `json:"integration"`
	Title       string   `json:"title,omitempty"`
	Version     string   `json:"version,omitempty"`
	Endpoints   int      `json:"endpoints"`
	DocsURL     string   `json:"docs_url,omitempty"`
	// Servers are the `servers:` URLs the document declares. They CORROBORATE
	// the binding, never decide it — proxy, gateway and staging hosts are
	// legitimate and common, so a mismatch warns and never blocks.
	Servers []string `json:"servers"`
	// ServersMatch is true when one of them resolves to the bound host.
	ServersMatch bool `json:"servers_match"`
	// Replaces names the contract this upload would displace, if any.
	Replaces string `json:"replaces,omitempty"`
	// HasTraffic is false when no call to this host has been seen yet, which is
	// a legitimate state (pre-traffic upload) the UI explains rather than warns
	// about.
	HasTraffic bool `json:"has_traffic"`
}

// handleContractPreview parses an uploaded document and describes what binding
// it would produce. Writes nothing.
func (e *uiExtension) handleContractPreview(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	req, sum, ok := e.readContractUpload(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, e.previewFor(st, req.PeerHost, sum))
}

// handleContractUpload parses, then persists, then — when this replaced a bound
// contract — diffs the two documents for breaking changes.
func (e *uiExtension) handleContractUpload(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	req, sum, ok := e.readContractUpload(w, r)
	if !ok {
		return
	}

	integration := integrationForHost(req.PeerHost)
	// Refuse to write over a row this upload does not own. Slugs are a pure
	// function of the host, so a clash is either a different host that slugs
	// the same or — the one that matters — a CONFIG contract (the self spec)
	// wearing that id. Silently replacing the org's own contract with a
	// vendor's would be a bad way to find out.
	if existing, found, err := specInfoFor(st, integration); err == nil && found {
		if existing.Source != model.SpecSourceUpload || existing.PeerHost != req.PeerHost {
			writeErr(w, http.StatusConflict, "integration_conflict",
				fmt.Sprintf("A contract already uses the name %q on this collector. Remove it before binding a new one to %s.",
					integration, req.PeerHost))
			return
		}
	}

	info := specInfoFromDoc(sum, integration, req.PeerHost)
	prev, err := st.PutUploadedSpec(info, []byte(req.Document))
	if err != nil {
		e.telemetry.Logger.Warn("contracts: storing an uploaded contract failed: " + err.Error())
		writeErr(w, http.StatusInternalServerError, "store_failed", msgContractStoreFailed)
		return
	}

	out := map[string]any{
		"contract": info,
		"replaced": prev.Existed,
	}
	if prev.Existed {
		out["replaced_version"] = prev.Version
		// The version diff. It used to be computed at construction from
		// `spec_v2_path`; with contracts uploaded, a replace is the only moment
		// two versions of one contract exist, so this is where it lives now.
		// Best effort: a diff that fails must never cost the operator the
		// upload they just made.
		out["breaking_changes"] = e.diffOnReplace(st, integration, prev.Raw, []byte(req.Document))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleContractRemove deletes a contract. Remove ships WITH upload: a contract
// bound to the wrong host and no way to undo it is worse than no contract.
func (e *uiExtension) handleContractRemove(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	var body struct {
		Integration string `json:"integration"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
		return
	}
	integration := strings.TrimSpace(body.Integration)
	if integration == "" {
		writeErr(w, http.StatusBadRequest, "integration_required", msgContractIntegrationRequired)
		return
	}
	// Only uploaded contracts are removable here. The self contract comes from
	// config, so deleting its row would just be undone at the next start —
	// saying so beats a button that appears to work.
	if existing, found, err := specInfoFor(st, integration); err == nil && found &&
		existing.Source != model.SpecSourceUpload && existing.Source != "" {
		writeErr(w, http.StatusConflict, "not_removable", msgContractNotRemovable)
		return
	}
	existed, err := st.DeleteSpecInfo(integration)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", msgContractStoreFailed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": existed})
}

// readContractUpload validates the request and parses the document. Every
// refusal from here is one the operator can act on, and none of them persist.
func (e *uiExtension) readContractUpload(w http.ResponseWriter, r *http.Request) (uploadRequest, drift.SpecSummary, bool) {
	var (
		req  uploadRequest
		none drift.SpecSummary
	)
	if err := json.NewDecoder(io.LimitReader(r.Body, maxDocBytes+(1<<16))).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
		return req, none, false
	}

	host, err := normalizeHost(req.PeerHost)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_host", err.Error())
		return req, none, false
	}
	req.PeerHost = host

	if strings.TrimSpace(req.Document) == "" {
		writeErr(w, http.StatusBadRequest, "document_required", msgContractDocumentRequired)
		return req, none, false
	}
	if len(req.Document) > maxDocBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "document_too_large", msgContractTooLarge)
		return req, none, false
	}

	summary, err := drift.DescribeSpec([]byte(req.Document))
	if err != nil {
		// The consult's string, with the parser's own message appended — the
		// operator needs to know WHICH line is wrong, not just that one is.
		writeErr(w, http.StatusBadRequest, "unparseable_document",
			"Couldn't read that as an OpenAPI document. "+err.Error())
		return req, none, false
	}
	return req, summary, true
}

// previewFor describes the binding an upload would produce.
func (e *uiExtension) previewFor(st store.Store, host string, sum drift.SpecSummary) contractPreview {
	integration := integrationForHost(host)
	p := contractPreview{
		PeerHost:    host,
		Integration: integration,
		Title:       sum.Title,
		Version:     sum.Version,
		Endpoints:   sum.Endpoints,
		DocsURL:     sum.DocsURL,
		Servers:     sum.ServerHosts,
	}
	for _, s := range p.Servers {
		if strings.EqualFold(s, host) {
			p.ServersMatch = true
			break
		}
	}
	if existing, found, err := specInfoFor(st, integration); err == nil && found {
		p.Replaces = existing.Version
	}
	p.HasTraffic = hostHasTraffic(st, host)
	return p
}

// diffOnReplace computes the breaking changes between the document an upload
// displaced and the one that replaced it, and records them. Returns how many.
func (e *uiExtension) diffOnReplace(st store.Store, integration string, prev, current []byte) int {
	if len(prev) == 0 {
		return 0
	}
	findings, err := drift.DetectVersionDiffData(prev, current, integration)
	if err != nil {
		e.telemetry.Logger.Warn("contracts: version diff on replace failed for " + integration + ": " + err.Error())
		return 0
	}
	stored := 0
	for _, f := range findings {
		if err := st.InsertFinding(f); err != nil {
			e.telemetry.Logger.Warn("contracts: storing a version-diff finding failed: " + err.Error())
			continue
		}
		stored++
	}
	return stored
}

// specInfoFromDoc builds the row an upload writes.
func specInfoFromDoc(sum drift.SpecSummary, integration, host string) model.SpecInfo {
	return model.SpecInfo{
		Integration: integration,
		Role:        model.SpecRoleProvider,
		PeerHost:    host,
		Format:      model.SpecFormatOpenAPI,
		Source:      model.SpecSourceUpload,
		LoadedAt:    time.Now().UTC().Format(time.RFC3339),
		Title:       sum.Title,
		Version:     sum.Version,
		Endpoints:   sum.Endpoints,
		DocsURL:     sum.DocsURL,
	}
}

// specInfoFor finds one contract row by its integration id.
func specInfoFor(st store.Store, integration string) (model.SpecInfo, bool, error) {
	infos, err := st.ListSpecInfos()
	if err != nil {
		return model.SpecInfo{}, false, err
	}
	for _, si := range infos {
		if si.Integration == integration {
			return si, true, nil
		}
	}
	return model.SpecInfo{}, false, nil
}

// hostHasTraffic reports whether any edge for this host has been discovered.
// Pre-traffic upload is legitimate — on a fresh install there are no edges at
// all — so this drives an explanation, never a refusal.
func hostHasTraffic(st store.Store, host string) bool {
	edges, err := st.ListEdges(false)
	if err != nil {
		return false
	}
	for _, ed := range edges {
		if strings.EqualFold(ed.PeerHost, host) {
			return true
		}
	}
	return false
}

// normalizeHost accepts what an operator is likely to paste and refuses what
// cannot be a peer host. Errors are the message the UI shows, so each one says
// what to type instead.
func normalizeHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	if h == "" {
		return "", fmt.Errorf("%s", msgContractHostRequired)
	}
	// A pasted URL is the overwhelmingly likely mistake, and the host is right
	// there — take it rather than refusing.
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	h = strings.Trim(h, ".")
	if h == "" {
		return "", fmt.Errorf("%s", msgContractHostRequired)
	}
	if len(h) > maxHostLen {
		return "", fmt.Errorf("%s", msgContractHostTooLong)
	}
	if strings.ContainsAny(h, " \t\r\n,\\\"'<>") {
		return "", fmt.Errorf("%s", msgContractHostInvalid)
	}
	for _, r := range h {
		if r > 127 {
			// Internationalised hosts arrive punycoded on the wire, and the
			// edges this binds to are the wire's hosts. Accepting a unicode
			// spelling would bind to a host no call ever carries.
			return "", fmt.Errorf("%s", msgContractHostPunycode)
		}
	}
	return h, nil
}

// integrationForHost derives the contract's id from the host it binds to. The
// operator is never asked for one — the consult's rule — and because it is a
// pure function of the host, two uploads for one host always address one row.
func integrationForHost(host string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, host)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
