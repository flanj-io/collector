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
	body, err := r.get(r.base + "/internal/contracts")
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
	return r.get(r.base + "/internal/contracts/doc?integration=" + url.QueryEscape(integration))
}

func (r *remoteSpecSource) get(u string) ([]byte, error) {
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
	if resp.StatusCode != http.StatusOK {
		// Never echo the body: it is a peer's response, and the token is in
		// this request. The status is what an operator needs.
		return nil, fmt.Errorf("store pod returned %s", resp.Status)
	}
	// Capped so a misconfigured endpoint (pointed at something that is not a
	// store pod) cannot read an unbounded body into a front's memory.
	return io.ReadAll(io.LimitReader(resp.Body, maxSpecBytes))
}

// maxSpecBytes caps a single contract document. Real OpenAPI documents run to a
// few megabytes; this is the ceiling the upload path enforces too, so the two
// ends of the channel agree.
const maxSpecBytes = 8 << 20 // 8 MiB
