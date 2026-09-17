package flanjui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// Contract FETCH — the second way a provider contract enters this collector,
// and the first one that leaves a claim a STRANGER can check.
//
// Ruling R5 (2026-09-16, `architecture.md` §3.4) reversed the standing "no URL
// fetch, ever" position this file's predecessor recorded. The reversal is not a
// convenience argument. An uploaded file's provenance is "somebody at the
// consumer had a file": a provider reading a flagged thread cannot verify it,
// cannot tell a current spec from a year-old one, and cannot distinguish their
// own document from a hand-edited copy. A fetched one says "your own published
// spec at <url>, fetched <when>" — which they can check against what they serve,
// today, themselves. That is an upgrade to the EVIDENCE RULE, and the evidence
// rule is what the whole product rests on.
//
// What did NOT change is the discipline the old position was protecting:
//
//   - A human binds. The fetch previews; it never persists on its own, and
//     neither does the probe. `suggest-and-approve` is the same guardrail
//     applied to fixes, and for the same reason — A WRONG CONTRACT IS WORSE
//     THAN NO CONTRACT. No contract renders `not checked`, honestly. A
//     mismatched one renders DRIFTED, loudly, to a stranger, on their real
//     provider, and providers who get flagged wrongly mute the tool.
//   - A failure is a STATED state. Every refusal below names what happened in
//     one sentence. Nothing here can bind an empty, partial or error document,
//     which is the silent way a fetch turns into a wrong contract.
//   - Nothing re-fetches. The document is read ONCE, at the moment a human
//     approved it. Periodic re-fetch is the control-plane registry (v2) and is
//     deliberately absent — see `docs/CONCEPTS.md`.
//   - The document still never leaves. It is fetched INTO this collector and
//     stored here, exactly as an upload is.
//
// The SSRF concern the old comment raised is real and is answered rather than
// waved away — see fetchTargetError and the probe's edge requirement below.

const (
	// contractFetchTimeout bounds one document fetch end to end.
	contractFetchTimeout = 20 * time.Second
	// contractProbeTimeout bounds ONE probe request. A probe fires several, so
	// this is deliberately short: a host that does not answer a conventional
	// path is the common case, not an error worth waiting on.
	contractProbeTimeout = 4 * time.Second
	// contractFetchMaxRedirects bounds a redirect chain. Publishers redirect
	// (http→https, a CDN, a versioned path), so zero would refuse real specs.
	contractFetchMaxRedirects = 5
	// stagedFetchTTL is how long a fetched document waits for the human to
	// confirm it. Long enough to read a confirm step, short enough that a
	// forgotten tab is not holding a document in memory for an afternoon.
	stagedFetchTTL = 15 * time.Minute
	// maxStagedFetches caps the staging table. The UI opens one fetch at a
	// time; this bounds a script that does not.
	maxStagedFetches = 8
)

// stagedFetch is a fetched document waiting for a human to approve it.
//
// The document is held HERE, server-side, between the preview and the bind —
// it is not handed to the browser and taken back. That is what makes the
// evidence claim true rather than merely plausible: `source: fetched, url: X`
// can only ever describe bytes THIS collector read from X. Round-tripping the
// document through the client would leave the URL a label the client chose,
// attached to bytes the client supplied, on a row whose entire purpose is to be
// checkable by a third party.
//
// It also gets the property the upload path buys with a second parse: what the
// operator confirmed is byte-for-byte what gets bound. A re-fetch on confirm
// would bind a document nobody previewed.
type stagedFetch struct {
	token    string
	url      string
	peerHost string
	doc      []byte
	summary  drift.SpecSummary
	fetched  time.Time
	expires  time.Time
}

// fetchStaging holds the documents awaiting confirmation. In-process and
// unreplicated on purpose: a staged fetch is one operator's half-finished
// action in one browser tab, not durable state. On a shared-postgres deployment
// the confirm lands on whichever pod served the preview (the UI is loopback —
// one browser, one pod), and a pod restart loses the staging, which costs a
// re-fetch and nothing else.
type fetchStaging struct {
	mu    sync.Mutex
	items map[string]*stagedFetch
}

func (s *fetchStaging) put(f *stagedFetch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]*stagedFetch)
	}
	s.sweepLocked(time.Now())
	// Over the cap, drop the oldest rather than refusing: the newest staged
	// fetch is the one the operator is looking at.
	for len(s.items) >= maxStagedFetches {
		var oldest *stagedFetch
		for _, it := range s.items {
			if oldest == nil || it.fetched.Before(oldest.fetched) {
				oldest = it
			}
		}
		if oldest == nil {
			break
		}
		delete(s.items, oldest.token)
	}
	s.items[f.token] = f
}

// take returns a staged fetch and REMOVES it: a token binds at most once, so a
// double-submitted confirm cannot write the row twice.
func (s *fetchStaging) take(token string) (*stagedFetch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	it, ok := s.items[token]
	if !ok {
		return nil, false
	}
	delete(s.items, token)
	return it, true
}

func (s *fetchStaging) sweepLocked(now time.Time) {
	for tok, it := range s.items {
		if now.After(it.expires) {
			delete(s.items, tok)
		}
	}
}

// fetchRequest is the ONE route's envelope, in both of its shapes.
//
// One route, two steps, because they are two halves of one action and the
// operator meets them in one panel: `{url, peer_host}` fetches and describes,
// `{token}` binds what was described. Splitting them across two routes would
// put the preview on a route whose name says bind.
type fetchRequest struct {
	// URL is the document to fetch. Step one.
	URL string `json:"url"`
	// PeerHost is the provider edge the contract would bind to. Optional on
	// step one — an omitted host is taken from the URL, which is the case the
	// probe produces and the overwhelmingly common one an operator types.
	PeerHost string `json:"peer_host"`
	// Token is the staged fetch to bind. Step two. Present means BIND.
	Token string `json:"token"`
}

// fetchPreviewResponse is step one's answer: everything the confirm step
// renders, plus the handle that binds it.
type fetchPreviewResponse struct {
	// Token is what step two sends back. Absent means nothing was staged.
	Token string `json:"token"`
	// SourceURL is the URL this collector actually read, after redirects — not
	// the one that was typed. A redirect chain that lands somewhere else is a
	// fact the operator must see BEFORE binding, because it is what the
	// evidence line will claim afterwards.
	SourceURL string `json:"source_url"`
	// RequestedURL is what was typed, echoed back only when a redirect moved it.
	RequestedURL string `json:"requested_url,omitempty"`
	FetchedAt    string `json:"fetched_at"`
	// Preview is the SAME shape the upload path's preview returns, built by the
	// same code — the confirm step is one component for both sources.
	Preview contractPreview `json:"preview"`
}

// handleContractFetch fetches a contract from a URL and, on a second call with
// the token it returns, binds it.
//
// Guarded exactly like upload: POST, `X-Flanj-UI: 1`, JSON, no foreign Origin.
// A browser page on another origin cannot reach it, which is what keeps a
// user-typed-URL fetcher inside the customer network from being a lever
// anything but the operator can pull.
func (e *uiExtension) handleContractFetch(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	var req fetchRequest
	if !readJSONBody(w, r, maxSmallBodyBytes, &req, "request_too_large", msgRequestTooLarge) {
		return
	}
	if strings.TrimSpace(req.Token) != "" {
		e.bindStagedFetch(w, st, strings.TrimSpace(req.Token))
		return
	}
	e.previewFetch(w, r.Context(), st, req)
}

// previewFetch is step one: fetch, parse, describe, stage. Persists NOTHING.
func (e *uiExtension) previewFetch(w http.ResponseWriter, ctx context.Context, st store.Store, req fetchRequest) {
	target, err := parseFetchURL(req.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}

	// The host the contract binds to. An explicit one wins — a spec published
	// on a docs host binds to the API host, and those differ often enough that
	// refusing the mismatch would refuse the normal case. With none given, the
	// URL's host is the honest default and the confirm step shows it.
	hostSource := req.PeerHost
	if strings.TrimSpace(hostSource) == "" {
		hostSource = target.Host
	}
	host, err := normalizeHost(hostSource)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_host", err.Error())
		return
	}

	doc, finalURL, ferr := e.fetchContractDoc(ctx, target, contractFetchTimeout)
	if ferr != nil {
		// A fetch failure is a STATED state and never a bind. Every branch of
		// fetchError carries its own sentence naming what happened — a 404, a
		// TLS failure and a document over the cap are three different things to
		// do next, and "couldn't fetch" is none of them.
		e.telemetry.Logger.Info("contracts: fetch refused for " + target.Redacted() + ": " + ferr.Error())
		writeErr(w, ferr.status, ferr.code, ferr.message)
		return
	}

	summary, err := drift.DescribeSpec(doc)
	if err != nil {
		// The parser's own words go to the log, never to the browser — the same
		// rule the upload path follows. A URL that serves an HTML error page
		// with a 200 lands here, which is why the sentence names the URL.
		e.telemetry.Logger.Info("contracts: fetched document from " + finalURL + " did not parse: " + err.Error())
		writeErr(w, http.StatusBadRequest, "unparseable_document", msgContractFetchUnparseable)
		return
	}

	now := time.Now().UTC()
	staged := &stagedFetch{
		token:    newFetchToken(),
		url:      finalURL,
		peerHost: host,
		doc:      doc,
		summary:  summary,
		fetched:  now,
		expires:  now.Add(stagedFetchTTL),
	}
	e.staging.put(staged)

	resp := fetchPreviewResponse{
		Token:     staged.token,
		SourceURL: finalURL,
		FetchedAt: now.Format(time.RFC3339),
		Preview:   e.previewFor(st, host, summary),
	}
	if finalURL != target.String() {
		resp.RequestedURL = target.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// bindStagedFetch is step two: persist the document a human just approved.
//
// It writes the bytes THIS collector read, from the URL it records — never
// anything the client sent. The token is the only thing that crosses.
func (e *uiExtension) bindStagedFetch(w http.ResponseWriter, st store.Store, token string) {
	staged, ok := e.staging.take(token)
	if !ok {
		// Expired, already bound, or never existed — one refusal, because the
		// operator's next move is identical in all three: fetch it again. The
		// sentence says so instead of naming a token they never saw.
		writeErr(w, http.StatusConflict, "fetch_expired", msgContractFetchExpired)
		return
	}

	integration := integrationForHost(staged.peerHost)
	// The same ownership refusal the upload path applies, and it must apply
	// here too: a fetch that silently replaced the org's own self contract, or
	// a differently-spelled host that slugs the same, is the wrong-contract
	// failure this whole phase exists to prevent.
	if existing, found, err := specInfoFor(st, integration); err == nil && found {
		if !isOperatorBound(existing.Source) || existing.PeerHost != staged.peerHost {
			writeErr(w, http.StatusConflict, "integration_conflict",
				fmt.Sprintf("A contract already uses the name %q on this collector. Remove it before binding a new one to %s.",
					integration, staged.peerHost))
			return
		}
	}

	info := specInfoFromDoc(staged.summary, integration, staged.peerHost)
	info.Source = model.SpecSourceFetched
	info.SourceURL = staged.url
	// LoadedAt IS the fetch time for a fetched row — the moment the bytes were
	// read, not the moment the operator got round to confirming. The card, the
	// finding and the thread all render "fetched <when>" off this one field, so
	// it must mean what they say it means.
	info.LoadedAt = staged.fetched.Format(time.RFC3339)

	prev, err := st.PutUploadedSpec(info, staged.doc)
	if err != nil {
		e.telemetry.Logger.Warn("contracts: storing a fetched contract failed: " + err.Error())
		writeErr(w, http.StatusInternalServerError, "store_failed", msgContractStoreFailed)
		return
	}
	// Tell the spec cache before anything else — a REPLACE needs it most; see
	// the upload path's note.
	e.announceSpecChange()

	out := map[string]any{
		"contract": info,
		"replaced": prev.Existed,
	}
	if prev.Existed {
		out["replaced_version"] = prev.Version
		out["breaking_changes"] = e.diffOnReplace(st, integration, prev.Raw, staged.doc)
	}
	writeJSON(w, http.StatusOK, out)
}

// isOperatorBound reports whether a contract row was put there by a human
// acting in the UI — uploaded or fetched. Those two are interchangeable for
// ownership: either may replace the other, because a human bound both and the
// card offers Replace on both. A config or observed row is neither, and
// replacing one through this path would overwrite something no operator chose.
func isOperatorBound(source string) bool {
	return source == model.SpecSourceUpload || source == model.SpecSourceFetched
}

/* ── The fetch itself ──────────────────────────────────────────────────── */

// fetchError is a refusal with a sentence. Every failure on the fetch path has
// one, because "a fetch failure is a stated state, never a silent empty bind"
// is the rule — and a stated state that says "something went wrong" states
// nothing.
type fetchError struct {
	status  int
	code    string
	message string
	err     error
}

func (f *fetchError) Error() string {
	if f.err != nil {
		return f.code + ": " + f.err.Error()
	}
	return f.code
}

// parseFetchURL accepts what an operator pastes and refuses what cannot be a
// published spec URL. Errors are the message the UI shows.
func parseFetchURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New(msgContractFetchURLRequired)
	}
	// A bare host or host/path is what a hurried paste looks like. https is the
	// only default worth guessing: a spec served over plain http would be
	// fetched in the clear, and guessing that on the operator's behalf is not
	// this function's call to make silently.
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, errors.New(msgContractFetchURLInvalid)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New(msgContractFetchSchemeUnsupported)
	}
	if u.Host == "" {
		return nil, errors.New(msgContractFetchURLInvalid)
	}
	// Credentials in the URL are dropped rather than sent. A spec behind basic
	// auth is not a PUBLISHED spec, and the whole evidence claim is that the
	// provider serves this document to anyone who asks — including the provider
	// themselves, checking the thread. Silently authenticating would make the
	// claim false in the one case it matters.
	if u.User != nil {
		return nil, errors.New(msgContractFetchNoCredentials)
	}
	u.Fragment = ""
	return u, nil
}

// fetchTargetError refuses a destination this collector must not request.
//
// This is the answer to the SSRF objection the old "no URL fetch, ever" comment
// raised, and it is deliberately NARROW. Private and internal addresses are
// ALLOWED: `internal` is a first-class edge class here, an internal provider's
// spec lives on an internal host, and refusing RFC1918 would refuse the
// self-hosted case this product is built for.
//
// What is refused is the cloud instance-metadata service — 169.254.169.254 and
// the link-local range it sits in, plus the IPv6 equivalents. That is the
// specific pivot the old comment named, it is never a published OpenAPI
// document, and refusing it costs nothing real.
//
// This is a guard, not a boundary. The boundary is guardLocalMutating: a
// foreign page cannot reach this route at all.
func fetchTargetError(host string) error {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i+1:], "]") {
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
	}
	h = strings.Trim(h, "[]")
	ip := net.ParseIP(h)
	if ip == nil {
		// A NAME. Not resolved here on purpose: resolving to check and then
		// letting the transport resolve again is a TOCTOU that buys nothing,
		// and the metadata service is reached by its address in every exploit
		// that matters. The names that alias it are refused below.
		lower := strings.ToLower(h)
		if lower == "metadata.google.internal" || lower == "metadata" {
			return errors.New(msgContractFetchBlockedTarget)
		}
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New(msgContractFetchBlockedTarget)
	}
	// fd00:ec2::254 — the IPv6 instance metadata address.
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return errors.New(msgContractFetchBlockedTarget)
	}
	return nil
}

// fetchContractDoc reads one document. Returns the bytes and the URL they
// actually came from after redirects.
//
// The cap is enforced by READING one byte past it and refusing — never by
// truncating. A document cut at the cap still parses, as garbage, so
// truncation would report a PARSE error for a SIZE problem and then bind a
// wrong contract: the exact wrong-diagnosis class model.MaxContractDocBytes
// exists to end at every other boundary (collector#40, #48, #50).
func (e *uiExtension) fetchContractDoc(ctx context.Context, target *url.URL, timeout time.Duration) ([]byte, string, *fetchError) {
	if err := fetchTargetError(target.Host); err != nil {
		return nil, "", &fetchError{http.StatusForbidden, "blocked_target", err.Error(), err}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", &fetchError{http.StatusBadRequest, "invalid_url", msgContractFetchURLInvalid, err}
	}
	// Announce ourselves. A provider reading their access log should be able to
	// tell what asked for their spec, and this is the one request this product
	// makes to a host it is not otherwise talking to.
	req.Header.Set("User-Agent", "flanj-collector (contract fetch)")
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml, text/plain;q=0.8, */*;q=0.5")

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= contractFetchMaxRedirects {
				return errors.New("too many redirects")
			}
			// Every hop is re-checked: a redirect to the metadata service is
			// exactly how a target-check that runs only on the typed URL is
			// defeated.
			return fetchTargetError(r.URL.Host)
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", &fetchError{http.StatusGatewayTimeout, "fetch_timeout", msgContractFetchTimeout, err}
		}
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Err != nil && strings.Contains(uerr.Err.Error(), msgContractFetchBlockedTarget) {
			return nil, "", &fetchError{http.StatusForbidden, "blocked_target", msgContractFetchBlockedTarget, err}
		}
		return nil, "", &fetchError{http.StatusBadGateway, "fetch_failed", msgContractFetchUnreachable, err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	final := resp.Request.URL.String()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status is IN the sentence. "Couldn't fetch" sends an operator
		// looking at their network; "answered 404" sends them to the URL, and
		// "answered 401" tells them the spec is not published, which is the one
		// answer that means this whole path is the wrong one for that provider.
		return nil, final, &fetchError{
			http.StatusBadGateway, "fetch_status",
			fmt.Sprintf(msgContractFetchStatusFmt, resp.StatusCode), nil,
		}
	}

	// Content-Length is a HINT and is refused early when it is over the cap —
	// it saves reading 200 MB to learn what the header said. It is never
	// trusted in the other direction: the read below is capped regardless.
	if resp.ContentLength > int64(maxDocBytes) {
		return nil, final, &fetchError{http.StatusRequestEntityTooLarge, "document_too_large", msgContractTooLarge, nil}
	}

	doc, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxDocBytes)+1))
	if err != nil {
		return nil, final, &fetchError{http.StatusBadGateway, "fetch_failed", msgContractFetchUnreachable, err}
	}
	if len(doc) > maxDocBytes {
		return nil, final, &fetchError{http.StatusRequestEntityTooLarge, "document_too_large", msgContractTooLarge, nil}
	}
	if len(strings.TrimSpace(string(doc))) == 0 {
		// An empty 200 is the silent-empty-bind case by name. It parses to
		// nothing, binds nothing, and would leave a card claiming a contract
		// over a host nothing validates.
		return nil, final, &fetchError{http.StatusBadGateway, "empty_document", msgContractFetchEmpty, nil}
	}
	return doc, final, nil
}

// newFetchToken mints a staging handle. It is a handle, not a secret: the route
// it is used on is already guarded, and nothing about it authorises anything a
// caller could not do by fetching again. Random anyway, so two tabs staging at
// the same nanosecond cannot address one another's document.
func newFetchToken() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}
