package promote

import (
	"context"
	"net/http"
)

// Thread-link management against the CP thread endpoints (CONTRACTS §5).
// Replace thread link = revoke every outstanding thread-link token + anonymous
// sessions and mint a fresh one (revoke_existing:true); plain revoke kills the
// links without minting. Owner access and verified participants are untouched.
// The user-facing name is "Thread link"; the wire paths keep the internal
// peek-links name.

// PeekLinkRequest mirrors POST /api/v1/threads/{id}/peek-links. Any channel
// value is ignored by the CP (always "link") and is no longer sent.
type PeekLinkRequest struct {
	RevokeExisting     bool  `json:"revoke_existing,omitempty"`
	CardEndpointDetail *bool `json:"card_endpoint_detail,omitempty"`
}

// PeekLinkResponse is the CP reply for a mint (201). ThreadURL is the new
// Thread link; PeekURL/MagicToken are the deprecated aliases.
type PeekLinkResponse struct {
	ThreadURL  string `json:"thread_url"`
	PeekURL    string `json:"peek_url,omitempty"`
	MagicToken string `json:"magic_token,omitempty"`
	ExpiresAt  string `json:"expires_at"`
	Revoked    int    `json:"revoked"`
}

// PostPeekLink mints a fresh token for the thread's link (201).
func (c *Client) PostPeekLink(ctx context.Context, threadID string, req PeekLinkRequest) (PeekLinkResponse, int, error) {
	var out PeekLinkResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/threads/"+threadID+"/peek-links", c.bearer(), req, &out, http.StatusCreated)
	if err != nil {
		return PeekLinkResponse{}, status, err
	}
	if out.ThreadURL == "" {
		out.ThreadURL = out.PeekURL
	}
	return out, status, nil
}

// RevokeLinks revokes every outstanding thread-link token (and anonymous
// sessions) on the thread. Returns the revoked count.
func (c *Client) RevokeLinks(ctx context.Context, threadID string) (int, int, error) {
	var out struct {
		Revoked int `json:"revoked"`
	}
	status, err := c.do(ctx, http.MethodPost, "/api/v1/threads/"+threadID+"/peek-links/revoke", c.bearer(), struct{}{}, &out, http.StatusOK)
	return out.Revoked, status, err
}
