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
	"sync/atomic"
	"syscall"
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

	doc, finalURL, ferr := e.fetchContractDoc(ctx, target, contractFetchTimeout, edgeAllowsPrivate(st, target))
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

// Where a contract fetch may connect — the destination policy, in one place.
//
// Three classes, decided on the address the connection is ACTUALLY made to:
//
//   - FORBIDDEN, always: link-local (169.254/16 — the cloud metadata service —
//     and fe80::/10), the IPv6 metadata address fd00:ec2::254, unspecified
//     (0.0.0.0, ::) and the rest of 0/8 (which Linux routes to the local host),
//     multicast and broadcast. None is ever a published OpenAPI document, so
//     refusing them costs nothing.
//   - PRIVATE: loopback (127/8, ::1), RFC1918, CGNAT (100.64/10) and IPv6 ULA
//     (fc00::/7). Reachable ONLY when the fetch was addressed to a host this
//     deployment already has an edge for — see destinationPolicy.
//   - PUBLIC: everything else.
//
// Why PRIVATE is conditional rather than forbidden: `internal` is a first-class
// edge class, and an internal provider's spec lives on an internal host —
// refusing RFC1918 outright would refuse the self-hosted case this product is
// built for. Conditioning it on the edge table means a fetch can reach an
// internal address only where the SDK is ALREADY sending traffic, so it opens
// no destination the deployment was not already talking to. A typed URL naming
// an internal admin endpoint the app never calls is refused.
//
// Embedded IPv4 is classified by the address it carries: an IPv4-mapped
// ::ffff:a.b.c.d (net.IP.To4 unwraps it) and a NAT64 64:ff9b::/96 address —
// the second is the one a v4-only check misses, and on a NAT64 network it
// reaches exactly the IPv4 address it embeds.
type destClass int

const (
	destPublic destClass = iota
	destPrivate
	destForbidden
)

var (
	errBlockedTarget = errors.New(msgContractFetchBlockedTarget)
	errPrivateTarget = errors.New(msgContractFetchPrivateTarget)

	nat64Prefix   = mustCIDR("64:ff9b::/96")
	cgnatRange    = mustCIDR("100.64.0.0/10")
	thisNetwork   = mustCIDR("0.0.0.0/8")
	ec2MetaIPv6   = net.ParseIP("fd00:ec2::254")
	ipv4Broadcast = net.IPv4bcast
)

func mustCIDR(c string) *net.IPNet {
	_, n, err := net.ParseCIDR(c)
	if err != nil {
		panic(err)
	}
	return n
}

// classifyIP puts one address in its class. Forbidden checks come first: an
// address that is both (say, a mapped link-local) must never be downgraded to
// merely private.
func classifyIP(ip net.IP) destClass {
	if nat64Prefix.Contains(ip) {
		// The embedded IPv4 is the real destination behind a NAT64 gateway.
		return classifyIP(net.IP(ip[12:16]))
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // IPv4-mapped IPv6 is judged as the IPv4 it carries
	}
	switch {
	case ip.IsUnspecified(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast(),
		ip.Equal(ipv4Broadcast), ip.Equal(ec2MetaIPv6), thisNetwork.Contains(ip):
		return destForbidden
	case ip.IsLoopback(), ip.IsPrivate(), cgnatRange.Contains(ip):
		return destPrivate
	}
	return destPublic
}

// hostOnly strips a port and IPv6 brackets from host[:port].
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// checkDestination applies the policy to a host[:port] — a URL's host, or the
// resolved address the dialer is about to connect to.
//
// For a NAME it can only refuse the well-known metadata aliases (so the
// operator gets that specific sentence rather than a dial error). A name is NOT
// made safe here: `evil.test IN A 169.254.169.254` is an ordinary A record that
// passes any string check. The binding check is the dialer's Control hook,
// which calls this with the RESOLVED address — see contractHTTPClient.
func checkDestination(hostport string, allowPrivate bool) error {
	h := hostOnly(hostport)
	ip := net.ParseIP(h)
	if ip == nil {
		lower := strings.ToLower(strings.TrimSuffix(h, "."))
		if lower == "metadata.google.internal" || lower == "metadata" {
			return errBlockedTarget
		}
		return nil
	}
	switch classifyIP(ip) {
	case destForbidden:
		return errBlockedTarget
	case destPrivate:
		if !allowPrivate {
			return errPrivateTarget
		}
	}
	return nil
}

// destinationPolicy is the per-request privilege: may this fetch connect to a
// PRIVATE address? Decided once from the host the request was addressed to,
// and only ever LOWERED — a redirect to any other host drops it for the rest of
// the chain, so an allowed internal edge cannot bounce the fetch onto an
// internal host the deployment never calls. Monotonic on purpose: coming back
// to the original host does not restore it.
//
// A client is built per request (contractHTTPClient), so this is never shared
// between requests, and the transport it guards pools no connection to a host
// the policy did not see.
type destinationPolicy struct {
	allowPrivate atomic.Bool
	origin       string
}

// fetchContractDoc reads one document. Returns the bytes and the URL they
// actually came from after redirects.
//
// The cap is enforced by READING one byte past it and refusing — never by
// truncating. A document cut at the cap still parses, as garbage, so
// truncation would report a PARSE error for a SIZE problem and then bind a
// wrong contract: the exact wrong-diagnosis class model.MaxContractDocBytes
// exists to end at every other boundary (collector#40, #48, #50).
//
// allowPrivate is whether the addressed host is a discovered edge — the caller
// decides it (edgeAllowsPrivate), because only the caller holds the store.
func (e *uiExtension) fetchContractDoc(ctx context.Context, target *url.URL, timeout time.Duration, allowPrivate bool) ([]byte, string, *fetchError) {
	if err := checkDestination(target.Host, allowPrivate); err != nil {
		return nil, "", destinationRefusal(err)
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

	resp, err := contractHTTPClient(timeout, target.Host, allowPrivate).Do(req)
	if err != nil {
		// Destination refusals first: a refused dial can surface wrapped in a
		// timeout-shaped error, and "that host didn't answer" would be the
		// wrong diagnosis for "Flanj refused to ask".
		if errors.Is(err, errBlockedTarget) || errors.Is(err, errPrivateTarget) {
			return nil, "", destinationRefusal(err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", &fetchError{http.StatusGatewayTimeout, "fetch_timeout", msgContractFetchTimeout, err}
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

// destinationRefusal maps a policy error to the route's answer. Two codes,
// because the operator's next move differs: a forbidden target is never
// fetchable, a private one is fetchable once it is a provider the deployment
// actually calls.
func destinationRefusal(err error) *fetchError {
	if errors.Is(err, errPrivateTarget) {
		return &fetchError{http.StatusForbidden, "private_target", msgContractFetchPrivateTarget, err}
	}
	return &fetchError{http.StatusForbidden, "blocked_target", msgContractFetchBlockedTarget, err}
}

// contractHTTPClient builds the client every fetch and probe request goes
// through — one per request, so the destination policy it carries is that
// request's alone.
//
// The guard that MATTERS is on the dialer's Control hook, which fires after
// resolution with the address the connection is about to be made to. Checking
// the URL's host catches only literals: `evil.test IN A 169.254.169.254` passes
// every string check, no rebinding race required. Control closes the name case
// and the resolve-then-resolve-again TOCTOU together, and covers every REDIRECT
// hop for free, because each hop dials.
//
// CheckRedirect bounds the chain, re-checks each hop's host (for the literal
// case's sentence), and LOWERS the policy on a cross-host hop before that hop
// dials.
func contractHTTPClient(timeout time.Duration, origin string, allowPrivate bool) *http.Client {
	pol := &destinationPolicy{origin: strings.ToLower(origin)}
	pol.allowPrivate.Store(allowPrivate)
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return dialGuard(address, pol.allowPrivate.Load())
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
			// No environment proxy. With HTTP_PROXY set the dial goes to the
			// PROXY and the target's address is never seen by Control, which
			// would switch this whole policy off silently.
			Proxy: nil,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= contractFetchMaxRedirects {
				return errors.New("too many redirects")
			}
			if !strings.EqualFold(r.URL.Host, pol.origin) {
				pol.allowPrivate.Store(false)
			}
			return checkDestination(r.URL.Host, pol.allowPrivate.Load())
		},
	}
}

// dialGuard refuses a connection about to be made to an address this
// collector must not request. `address` is what the dialer resolved — an IP
// literal with a port — so a name that resolves to a forbidden or (unallowed)
// private address is refused here even though its spelling passed every
// earlier check. It is checkDestination, so there is exactly one policy.
func dialGuard(address string, allowPrivate bool) error {
	return checkDestination(address, allowPrivate)
}

// edgeAllowsPrivate reports whether a fetch addressed to this URL may connect
// to a private address: only when the URL's own host is a discovered edge.
//
// The URL's host, NOT the peer_host the contract will bind to — otherwise
// "bind to api.acme.test (an edge), fetch http://10.0.0.5/admin" would borrow
// the edge's privilege for a host nobody calls.
func edgeAllowsPrivate(st store.Store, target *url.URL) bool {
	host, err := normalizeHost(target.Scheme + "://" + target.Host)
	if err != nil {
		return false
	}
	return hostHasTraffic(st, host)
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
