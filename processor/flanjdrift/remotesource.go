package flanjdrift

import (
	"encoding/json"
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
	return r.get(r.base+"/internal/contracts/doc?integration="+url.QueryEscape(integration),
		fmt.Sprintf("the contract document for %q", integration))
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
		return nil, fmt.Errorf("the store pod holds %s larger than the %d MiB cap",
			what, maxSpecBytes>>20)
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
		return nil, fmt.Errorf("the store pod served %s larger than the %d MiB cap",
			what, maxSpecBytes>>20)
	}
	return raw, nil
}

// maxSpecBytes caps a single contract document, and is the ONE cap
// (model.MaxContractDocBytes): the same number the UI upload refuses to accept
// and the store pod's contract endpoint refuses to serve, so the ends of this
// channel agree by construction rather than by comment.
const maxSpecBytes = model.MaxContractDocBytes
