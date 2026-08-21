package promote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Peek-link management against the CP thread endpoints (CONTRACTS §5). Copy-link mints a
// channel-tagged token on the SAME per-thread link so the consumer can paste it into whatever
// channel the two teams already share; revoke + regenerate is immediate. The channel is stored
// on the CP token record, never in the URL.

// PeekLinkRequest mirrors POST /api/v1/threads/{id}/peek-links.
type PeekLinkRequest struct {
	Channel            string `json:"channel,omitempty"`
	RevokeExisting     bool   `json:"revoke_existing,omitempty"`
	CardEndpointDetail *bool  `json:"card_endpoint_detail,omitempty"`
}

// PeekLinkResponse is the CP reply for a mint (201).
type PeekLinkResponse struct {
	PeekURL    string `json:"peek_url"`
	MagicToken string `json:"magic_token"`
	Channel    string `json:"channel"`
	ExpiresAt  string `json:"expires_at"`
	Revoked    int    `json:"revoked"`
}

// PostPeekLink mints a fresh channel-tagged token for the thread's link.
func (c *Client) PostPeekLink(ctx context.Context, threadID string, req PeekLinkRequest) (PeekLinkResponse, int, error) {
	var out PeekLinkResponse
	status, err := c.postJSON(ctx, "/api/v1/threads/"+threadID+"/peek-links", req, &out, http.StatusCreated)
	return out, status, err
}

// RevokePeekLinks revokes every outstanding token on the thread. Returns the revoked count.
func (c *Client) RevokePeekLinks(ctx context.Context, threadID string) (int, int, error) {
	var out struct {
		Revoked int `json:"revoked"`
	}
	status, err := c.postJSON(ctx, "/api/v1/threads/"+threadID+"/peek-links/revoke", struct{}{}, &out, http.StatusOK)
	return out.Revoked, status, err
}

func (c *Client) postJSON(ctx context.Context, path string, req any, out any, wantStatus int) (int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("marshal %s: %w", path, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.DeployToken)
	httpReq.Header.Set("X-Vinifera-Collector-Version", c.CollectorVersion)

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return resp.StatusCode, fmt.Errorf("POST %s returned %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode %s response: %w", path, err)
	}
	return resp.StatusCode, nil
}
