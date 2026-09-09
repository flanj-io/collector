package flanjui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/edge"
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
	// maxDocBytes caps an uploaded document: the ONE cap, shared with the
	// store pod's contract endpoint and the front that reads it, so the three
	// ends of the channel agree by construction rather than by comment.
	maxDocBytes = model.MaxContractDocBytes
	// maxEnvelopeBytes bounds the READ of an upload envelope; the ruling on
	// size is the document check in readContractUpload, against maxDocBytes.
	// The envelope is JSON, and JSON escaping grows a text document on the
	// wire (every newline is two bytes, every quote too), so a bound of
	// "document cap plus a little" refused documents that were under the cap:
	// real YAML at 7.8 MiB arrives as an envelope past 8 MiB. Twice the cap
	// holds any document the ruling would accept, plus the host and filename.
	maxEnvelopeBytes = 2*maxDocBytes + (1 << 16)
	// maxSmallBodyBytes bounds every OTHER JSON envelope the localhost API
	// accepts — contract remove, edge name, Connect, flag, finding ack. All of
	// them carry a handful of short fields; 64 KiB is far past any of them and
	// far under anything that would cost memory.
	maxSmallBodyBytes = 1 << 16
	// maxHostLen is the DNS name ceiling.
	maxHostLen = 253
)

// readJSONBody decodes a request envelope into dst, reading at most limit
// bytes. Returns false after writing the refusal: 413 with the given code and
// message when the body is over the limit, 400 invalid_json when it is not
// JSON.
//
// This is the ONE way a route in this package reads a JSON envelope. It was
// written for the upload path and left the four small-envelope routes alone
// (edge name, Connect, flag, finding ack) because 64 KiB is far past what any
// of them carry — but "harmless at those sizes" is not the same as correct: a
// pasted 70 KB flag message hit exactly the trap below and came back "The
// request body is not valid JSON.", which is the wrong-diagnosis class the
// upload fix existed to end. Adding a route means calling this, not writing a
// fifth decoder.
//
// The limit is enforced by http.MaxBytesReader, which FAILS at the limit. It
// used to be io.LimitReader, which stops at the limit and says nothing — the
// decoder then saw a string cut off mid-way, reported an unexpected EOF, and
// every oversized upload came back as "not valid JSON". The 413 branch was
// reachable only for envelopes inside the 64 KiB between the document cap and
// the reader's: a 9 MB document reproduced it through the UI on 2026-09-07
// (launch-week item 6). MaxBytesReader also tells the server to close the
// connection after the reply instead of draining the rest of the upload.
func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any, tooLargeCode, tooLargeMsg string) bool {
	return decodeJSONBody(w, r, limit, dst, tooLargeCode, tooLargeMsg, false)
}

// readOptionalJSONBody is readJSONBody for a route where an absent or empty
// body is a normal request rather than a malformed one — the finding ack, whose
// shipped UI sends `{}` and whose older builds send nothing at all. The size
// refusal is identical; only EOF is tolerated.
func readOptionalJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any, tooLargeCode, tooLargeMsg string) bool {
	return decodeJSONBody(w, r, limit, dst, tooLargeCode, tooLargeMsg, true)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any, tooLargeCode, tooLargeMsg string, emptyOK bool) bool {
	if r.Body == nil {
		if emptyOK {
			return true
		}
		writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
		return false
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(dst)
	if err == nil {
		return true
	}
	// Size first: an oversized body can fail as anything once the reader cuts
	// it off, and the size is the fact worth reporting.
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, tooLargeCode, tooLargeMsg)
		return false
	}
	if emptyOK && errors.Is(err, io.EOF) {
		return true
	}
	writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
	return false
}

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
	PeerHost    string `json:"peer_host"`
	Integration string `json:"integration"`
	Title       string `json:"title,omitempty"`
	Version     string `json:"version,omitempty"`
	Endpoints   int    `json:"endpoints"`
	DocsURL     string `json:"docs_url,omitempty"`
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

	// The row is written; tell the cache before anything else. A REPLACE is the
	// case that needs this most: the host is already covered, so the drift
	// processor's own first-sight kick never fires and every call until the next
	// tick would be scored against the document this upload just superseded.
	e.announceSpecChange()

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
	if !readJSONBody(w, r, maxSmallBodyBytes, &body, "request_too_large", msgRequestTooLarge) {
		return
	}
	integration := strings.TrimSpace(body.Integration)
	if integration == "" {
		writeErr(w, http.StatusBadRequest, "integration_required", msgContractIntegrationRequired)
		return
	}
	// Only uploaded contracts are removable here, and the refusal names the
	// provenance it actually found. A config contract would be re-loaded at the
	// next start, so the operator is sent to the file; an observed MCP snapshot
	// has no file at all — the server delivers it and the next tools/list
	// replaces it — so the config sentence sent them hunting for something that
	// does not exist. One code, two sentences: the class of refusal is the
	// same, the reason is not.
	if existing, found, err := specInfoFor(st, integration); err == nil && found &&
		existing.Source != model.SpecSourceUpload && existing.Source != "" {
		msg := msgContractNotRemovable
		if existing.Source == model.SpecSourceObserved {
			msg = msgContractNotRemovableObserved
		}
		writeErr(w, http.StatusConflict, "not_removable", msg)
		return
	}
	existed, err := st.DeleteSpecInfo(integration)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", msgContractStoreFailed)
		return
	}
	if existed {
		// A removal is a cache HIT on the deleted document, so nothing on the
		// per-call path notices it either — the contract keeps validating
		// traffic after the operator removed it. Announce only a real deletion:
		// removing what was not there changed no contract set.
		e.announceSpecChange()
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
	// Over the envelope bound is a document too large by construction (the
	// envelope is the document plus two short fields), so it gets the same
	// refusal as the document check below rather than a generic one.
	if !readJSONBody(w, r, maxEnvelopeBytes, &req, "document_too_large", msgContractTooLarge) {
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
		// The parser's reason — which line, which key — goes to the log; the
		// response carries the deck's sentence and nothing after it. It used to
		// append the parser's own text ("failed to unmarshal data: json error:
		// … yaml error: …"), which put Go's voice in the operator's UI (QA walk
		// finding NB-2, 2026-09-07). Every error on this surface is one sentence.
		e.telemetry.Logger.Info("contracts: refused an unparseable document for " + host +
			uploadFilenameNote(req.Filename) + ": " + err.Error())
		writeErr(w, http.StatusBadRequest, "unparseable_document", msgContractUnparseable)
		return req, none, false
	}
	return req, summary, true
}

// maxLoggedFilenameLen bounds, in runes, the request-supplied filename a log
// line carries.
const maxLoggedFilenameLen = 200

// uploadFilenameNote is the quoted name a log line carries when the operator's
// pick is known. The filename is what the REQUEST said it was — it is never
// echoed back to the browser, only logged beside the parser's reason.
//
// Which makes it attacker-controlled text on its way into a log stream. It used
// to be interpolated raw after a TrimSpace, so a filename containing a newline
// wrote a second line into the log — a forged entry, with whatever severity,
// component and message the sender chose, indistinguishable downstream from one
// this collector emitted. strconv.Quote escapes every control character
// (newline, carriage return, tab, the ANSI escape that repaints a terminal) and
// the quotes themselves, so the whole name stays one field of one line. It is
// truncated too: a megabyte filename is a megabyte log line.
func uploadFilenameNote(filename string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		return ""
	}
	// By runes, so the cut never lands mid-character.
	if r := []rune(name); len(r) > maxLoggedFilenameLen {
		name = string(r[:maxLoggedFilenameLen]) + "…"
	}
	return " " + strconv.Quote(name)
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
		// An uploaded contract binds to a network host by construction — the
		// host is required and normalised. Stated rather than left empty so the
		// card's origin line has a source that never guesses.
		EdgeClass: model.EdgeClassExternal,
		Source:    model.SpecSourceUpload,
		LoadedAt:  time.Now().UTC().Format(time.RFC3339),
		Title:     sum.Title,
		Version:   sum.Version,
		Endpoints: sum.Endpoints,
		DocsURL:   sum.DocsURL,
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
//
// It KEEPS a trailing :port. `flanj.peer.host` is host[:port] and it is the edge
// key (CONTRACTS §2); the spec cache looks it up by exact string. Truncating at
// the colon meant a host:port edge could not be bound AT ALL — the UI locked the
// uploader to `api.acme.test:28080`, the confirm step said so, and the server
// silently bound `api.acme.test` instead: a different edge, whose contract this
// upload would then report as a replace and overwrite.
//
// The scheme's own default port is dropped, because that is the same listener
// under a longer name and the SDK already emits it short.
func normalizeHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	if h == "" {
		return "", fmt.Errorf("%s", msgContractHostRequired)
	}
	// A pasted URL is the overwhelmingly likely mistake, and the host is right
	// there — take it rather than refusing. Its scheme decides which port is
	// redundant, so read it before it is stripped.
	scheme := ""
	if i := strings.Index(h, "://"); i >= 0 {
		scheme, h = h[:i], h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	// A pasted URL can carry userinfo (`user@host`); the host is after the last @.
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	// `https://api.acme.test:443/v1` and `api.acme.test` are one listener, and
	// the SDK already emits the short spelling — converge on it.
	h = edge.StripDefaultPort(h, scheme)

	// From here the host and the port are checked apart: the ceiling and the
	// DNS-shape rules are the HOST's, and the port has rules of its own.
	host, port := edge.SplitHostPort(h)
	host = strings.Trim(host, ".")
	if host == "" {
		return "", fmt.Errorf("%s", msgContractHostRequired)
	}
	if len(host) > maxHostLen {
		return "", fmt.Errorf("%s", msgContractHostTooLong)
	}
	if strings.ContainsAny(host, " \t\r\n,\\\"'<>") {
		return "", fmt.Errorf("%s", msgContractHostInvalid)
	}
	for _, r := range host {
		if r > 127 {
			// Internationalised hosts arrive punycoded on the wire, and the
			// edges this binds to are the wire's hosts. Accepting a unicode
			// spelling would bind to a host no call ever carries.
			return "", fmt.Errorf("%s", msgContractHostPunycode)
		}
	}
	if port == "" {
		return host, nil
	}
	// A port that is not a port would bind a contract to a key no call can ever
	// carry — the silent-no-op this whole function exists to stop.
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strings.TrimLeft(port, "0") != port {
		return "", fmt.Errorf("%s", msgContractHostBadPort)
	}
	return host + ":" + port, nil
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
