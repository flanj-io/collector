package flanjdrift

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanj-io/collector/internal/model"
)

// remoteSpecSource is the tiered topology's answer to a structural problem: a
// FRONT collector runs the drift processor but has no store, while the store
// pod holds both the store and the UI an operator uploads through. Without a
// read path the upload lands somewhere the front never sees, and detection
// silently never runs for exactly the deployments large enough to be tiered.
//
// So the front asks. Read-only, contracts only, over the store pod's
// intra-cluster listener (flanjstore `spec_endpoint`) — never the UI's, which
// stays loopback.
//
// Deliberately shaped like the deferred CP-side per-domain fetch
// (architecture.md §6, Addendum 4 ruling 3): "here are the hosts I see, send me
// their contracts". When that lands it is a second implementation of this
// interface, not a second channel.
type remoteSpecSource struct {
	base   string
	token  string
	client *http.Client
}

func newRemoteSpecSource(endpoint, token string) *remoteSpecSource {
	base := strings.TrimSuffix(strings.TrimSpace(endpoint), "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &remoteSpecSource{
		base:  base,
		token: strings.TrimSpace(token),
		// Bounded: a slow store pod must never stall the refresh loop into the
		// next tick. The cache keeps serving what it already has meanwhile.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// listSpecs fetches contract metadata — no documents, so a steady-state refresh
// on a fifty-provider front is one small request a minute.
func (r *remoteSpecSource) listSpecs() ([]model.SpecInfo, error) {
	body, err := r.get(r.base+"/internal/contracts", "the contract list")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Contracts []model.SpecInfo `json:"contracts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("store pod contract list: %w", err)
	}
	return payload.Contracts, nil
}

// specDoc fetches one raw contract document.
func (r *remoteSpecSource) specDoc(integration string) ([]byte, error) {
	raw, err := r.get(r.base+"/internal/contracts/doc?integration="+url.QueryEscape(integration),
		fmt.Sprintf("the contract document for %q", integration))
	if errors.Is(err, errPastTheCap) {
		// Re-shape the refusal as the typed condition the reconcilers key on,
		// so it can be reported ONCE rather than every ten seconds. The size is
		// not in the answer — a 413 carries a status, not a measurement — so it
		// stays zero here; the LISTING is where a front normally learns it
		// (overCap below), and this path is the fallback for a store pod on an
		// image that predates doc_bytes.
		return nil, &overCapError{integration: integration}
	}
	return raw, err
}

// overCap reports a row the store pod will refuse to serve, from the LISTING —
// no request, no refusal, no round trip.
//
// This is the front's half of the same fact the store pod logs. Before the
// listing carried a size, the only way to learn it was to ask for the document
// and be turned down, which meant one pointless request per oversized edge per
// front every ten seconds, forever, and a Warn line on both pods for each.
//
// A row whose size is not reported (DocBytes 0 — an older store pod) is NOT
// treated as over the cap: the transfer-time refusals stay in place and answer
// for it.
func (r *remoteSpecSource) overCap(si model.SpecInfo) *overCapError {
	if si.DocBytes <= maxSpecBytes {
		return nil
	}
	return &overCapError{integration: si.Integration, peerHost: si.PeerHost, bytes: si.DocBytes}
}

// get fetches u. `what` names the thing being fetched, so a refusal reads as a
// sentence about a contract rather than about a URL.
func (r *remoteSpecSource) get(u, what string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("store pod unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // the contract went away between list and fetch
	}
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		// The store pod's own refusal for a document past the cap
		// (extension/flanjstore/specserver.go). Translated here rather than
		// left as a bare status, so a front's log names the cause without
		// anyone having to go read the store pod's — the fix is on neither
		// pod anyway, it is the catalogue that is too big.
		return nil, fmt.Errorf("the store pod holds %s larger than the %d MiB cap: %w",
			what, maxSpecBytes>>20, errPastTheCap)
	}
	if resp.StatusCode != http.StatusOK {
		// Never echo the body: it is a peer's response, and the token is in
		// this request. The status is what an operator needs.
		return nil, fmt.Errorf("store pod returned %s", resp.Status)
	}
	// Capped so a misconfigured endpoint (pointed at something that is not a
	// store pod) cannot read an unbounded body into a front's memory — and
	// DETECTED, not truncated. One byte past the cap is read on purpose: a
	// buffer that fills it is a body the cap refuses.
	//
	// io.LimitReader alone stops at the cap and says nothing, so an oversized
	// document arrived cut off mid-content, kin-openapi (or ParseToolsList)
	// refused it, and the front logged a PARSE failure for what is a SIZE
	// problem — then silently detected no drift on that edge for as long as
	// the document stayed big. Same wrong-diagnosis class as the upload path's
	// own LimitReader (#40) and the localhost API's request bodies (#48); this
	// was the last one, on the outbound side.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSpecBytes+1))
	if err != nil {
		return nil, fmt.Errorf("store pod: reading %s failed: %w", what, err)
	}
	if len(raw) > maxSpecBytes {
		// Nothing partial is returned: a truncated document must never reach a
		// parser, which is the whole point of noticing here. The caller
		// (speccache.reconcile / mcpSeeds.reconcile) reports this and skips the
		// row, so whatever it already holds for that edge keeps validating.
		return nil, fmt.Errorf("the store pod served %s larger than the %d MiB cap: %w",
			what, maxSpecBytes>>20, errPastTheCap)
	}
	return raw, nil
}

// errPastTheCap marks a refusal for SIZE, wrapped into the sentence a front
// logs so the reason survives the message. It exists to separate this condition
// from every other reason a fetch can fail: a size refusal is PERMANENT for as
// long as the document stays big, so it is reported on transition, while a
// timeout or a 503 is transient and reports every time.
var errPastTheCap = errors.New("past the contract document cap")

// overCapError is one row the contract channel refuses for size. It carries the
// row's identity so the front can log the condition once and clear it once, and
// the size when it is known — the listing reports it, the store pod's 413 does
// not.
type overCapError struct {
	integration string
	peerHost    string
	// bytes is the stored document's size, or 0 when only a refusal said so.
	bytes int
}

func (e *overCapError) Error() string {
	if e.bytes > 0 {
		return fmt.Sprintf("the store pod holds the contract document for %q at %d bytes, past the %d MiB cap",
			e.integration, e.bytes, maxSpecBytes>>20)
	}
	return fmt.Sprintf("the store pod holds the contract document for %q larger than the %d MiB cap",
		e.integration, maxSpecBytes>>20)
}

func (e *overCapError) Unwrap() error { return errPastTheCap }

// asOverCap reports whether err is the document-cap condition, and for which
// row. Every other error the refresh loop can produce reports on every tick.
func asOverCap(err error) (*overCapError, bool) {
	var oc *overCapError
	if errors.As(err, &oc) {
		return oc, true
	}
	return nil, false
}

// maxSpecBytes caps a single contract document, and is the ONE cap
// (model.MaxContractDocBytes): the same number the UI upload refuses to accept
// and the store pod's contract endpoint refuses to serve, so the ends of this
// channel agree by construction rather than by comment.
const maxSpecBytes = model.MaxContractDocBytes
