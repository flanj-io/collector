package promote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/flanj-io/collector/internal/model"
)

// The v0.1a control-plane client surface (CONTRACTS §5, collector-facing subset).
//
// Two bearers exist: the install-time deploy token (cp_deploy_token), accepted
// ONLY by register (Connect), and the per-deployment collector key that register
// returns — persisted in the store, never logged — which authorizes everything
// else (flag, thread mutations, summary, me). A Client carries both; bearer()
// picks the key when present. Neither ever appears in an error message or log.

// CPError is a non-2xx control-plane answer with its JSON {error,message} body
// decoded (CONTRACTS §5: errors are JSON {"error": "<code>", "message": "<human>"}).
// Callers match on Status/Code (412 not_connected | contact_unconfirmed, 403
// wrong_origin, 401, 429) and relay the code to the UI.
type CPError struct {
	Status  int
	Code    string
	Message string
	Path    string
}

func (e *CPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("control plane %s returned %d %s", e.Path, e.Status, e.Code)
	}
	return fmt.Sprintf("control plane %s returned %d", e.Path, e.Status)
}

// AsCPError unwraps a CPError from err (nil when err is something else, e.g. a
// transport failure).
func AsCPError(err error) *CPError {
	var ce *CPError
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

// RegisterRequest is POST /api/v1/collectors/register (Bearer cp_deploy_token).
type RegisterRequest struct {
	ConsumerDisplayName string `json:"consumer_display_name"`
	ContactEmail        string `json:"contact_email"`
	ContactDisplayName  string `json:"contact_display_name,omitempty"`
	LocalUIURL          string `json:"local_ui_url,omitempty"`
}

// Confirmation-mail outcomes (CONTRACTS-CP §5.1, additive). A 2xx says the
// REGISTRATION succeeded; it says nothing about the mail, which is why this
// field exists. Empty means the CP reported no outcome — either no mail was
// warranted (the contact is already confirmed) or the CP predates the field.
const (
	ConfirmationMailSent     = "sent"
	ConfirmationMailFailed   = "failed"
	ConfirmationMailCooldown = "cooldown"
)

// RegisterResponse: 201 on first registration, 200 on the idempotent replay
// (same contact) or a contact change. CollectorKey is returned ONCE — on a
// replay it may be empty; callers keep the key they already persisted.
type RegisterResponse struct {
	CollectorID       string `json:"collector_id"`
	CollectorPublicID string `json:"collector_public_id"`
	CollectorKey      string `json:"collector_key"`
	ContactStatus     string `json:"contact_status"`
	// ConfirmationMail is what actually happened to the confirmation mail on
	// this call: sent | failed | cooldown, or "" when the CP reported none.
	// NEVER infer "sent" from the 2xx — that inference is the bug this field
	// closes: a refused SMTP transaction still answers 200/201.
	ConfirmationMail string `json:"confirmation_mail"`
	// ConfirmationMailRetryAfterS is the seconds left on the 1/10min resend
	// floor. Rides only on "cooldown"; 0 everywhere else.
	ConfirmationMailRetryAfterS int `json:"confirmation_mail_retry_after_s"`
}

// MeResponse is GET /api/v1/collectors/me (Bearer collector key).
type MeResponse struct {
	CollectorID         string `json:"collector_id"`
	CollectorPublicID   string `json:"collector_public_id"`
	ConsumerDisplayName string `json:"consumer_display_name"`
	ContactEmail        string `json:"contact_email"`
	ContactDisplayName  string `json:"contact_display_name"`
	ContactStatus       string `json:"contact_status"`
	// ConfirmedContactEmail is the contact currently usable for threads — null
	// until the first confirmation; it STAYS set while a newer contact is pending
	// (CONTRACTS-CP §5.3: the relay must not 412 a flag while it is non-null).
	// nil also when an older CP omits the field.
	ConfirmedContactEmail *string `json:"confirmed_contact_email"`
	RegisteredAt          string  `json:"registered_at"`
	ConfirmedAt           string  `json:"confirmed_at"`
}

// ThreadStateResponse is the close / reopen answer.
type ThreadStateResponse struct {
	State      string  `json:"state"`
	ClosedAt   *string `json:"closed_at"`
	ReopenedAt *string `json:"reopened_at"`
}

// HandoffResponse is POST /api/v1/threads/{id}/handoff: a 10-minute single-use
// owner handoff URL. It is opened in the browser and never stored or logged.
type HandoffResponse struct {
	OwnerURL  string `json:"owner_url"`
	ExpiresAt string `json:"expires_at"`
}

// FixedClaim is summary.fixed_claim.
type FixedClaim struct {
	DisplayName string `json:"display_name"`
	At          string `json:"at"`
}

// LinkStatus is summary.link.
type LinkStatus struct {
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

// ThreadSummary is GET /api/v1/threads/{id}/summary — thread STATE only; the
// conversation itself is read on the control plane. The SAME object is one row
// of GET /api/v1/threads (CONTRACTS-CP §5.5a: "byte-for-byte the §5.5 summary
// object"), so this struct serves both the per-thread poll and the list.
//
// It carries NO finding id, NO thread_url and NO token — a row is a state row,
// never a way to re-obtain access. The collector joins its own local fields on
// by thread id (extension/flanjui/threads.go).
type ThreadSummary struct {
	ID                  string      `json:"id"`
	ThreadPublicID      string      `json:"thread_public_id"`
	State               string      `json:"state"`
	ClosedAt            *string     `json:"closed_at"`
	ReopenedAt          *string     `json:"reopened_at"`
	Turn                string      `json:"turn"`
	ConsumerDisplayName string      `json:"consumer_display_name"`
	ProviderDisplayName string      `json:"provider_display_name"`
	Endpoint            string      `json:"endpoint"`
	EvidenceCount       int         `json:"evidence_count"`
	OpenedCount         int         `json:"opened_count"`
	KnockCount          int         `json:"knock_count"`
	MessageCount        int         `json:"message_count"`
	LastReplyAt         *string     `json:"last_reply_at"`
	FixedClaim          *FixedClaim `json:"fixed_claim"`
	Link                *LinkStatus `json:"link"`
	Archived            bool        `json:"archived"`
	CreatedAt           string      `json:"created_at"`
	// UpdatedAt is last activity — what the §5.5a order sorts on. A reply, a
	// close/reopen, a link replace and a deletion move it; the archive sweep
	// does not.
	UpdatedAt string `json:"updated_at"`
}

// ThreadListResponse is GET /api/v1/threads (CONTRACTS-CP §5.5a) — an ENVELOPE,
// not a bare array. `total` is what the collector has before the limit, so
// has_more (total > count) means "ask again with a bigger limit"; there is no
// cursor.
type ThreadListResponse struct {
	Threads []ThreadSummary `json:"threads"`
	Count   int             `json:"count"`
	Total   int             `json:"total"`
	Limit   int             `json:"limit"`
	HasMore bool            `json:"has_more"`
}

// WithCollectorKey returns a copy of the client that authenticates with the
// per-deployment collector key (everything after Connect).
func (c *Client) WithCollectorKey(key string) *Client {
	cp := *c
	cp.CollectorKey = key
	return &cp
}

// bearer is the Authorization value: the collector key once Connected, else the
// deploy token (pre-Connect; register + legacy calls).
func (c *Client) bearer() string {
	if c.CollectorKey != "" {
		return c.CollectorKey
	}
	return c.DeployToken
}

// Register performs the FIRST Connect of a deployment: POST
// /api/v1/collectors/register with the DEPLOY token (the only call that ever
// uses it after v0.1a). 201 first time, 200 on the idempotent replay. Once a
// collector key exists the collector MUST use RegisterWithKey instead — the CP
// has one deploy token and cannot tell deployments apart by it (CONTRACTS-CP
// §5.1): a deploy-token register with another email would create a NEW
// collector.
func (c *Client) Register(ctx context.Context, req RegisterRequest) (RegisterResponse, int, error) {
	var out RegisterResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/collectors/register", c.DeployToken, req, &out, http.StatusCreated, http.StatusOK)
	return out, status, err
}

// RegisterWithKey re-registers an already-Connected collector: POST
// /api/v1/collectors/register with Bearer <collector key> (CONTRACTS-CP §5.1).
// The same email = resend of the confirmation; a different email = a new
// pending contact on the SAME collector. The key is unchanged either way (the
// response carries none; callers keep the key they hold). 200 (201 tolerated).
func (c *Client) RegisterWithKey(ctx context.Context, req RegisterRequest) (RegisterResponse, int, error) {
	if c.CollectorKey == "" {
		return RegisterResponse{}, 0, errors.New("promote: RegisterWithKey without a collector key (use Register)")
	}
	var out RegisterResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/collectors/register", c.CollectorKey, req, &out, http.StatusOK, http.StatusCreated)
	return out, status, err
}

// Me refreshes the Connect state: GET /api/v1/collectors/me (Bearer collector key).
func (c *Client) Me(ctx context.Context) (MeResponse, int, error) {
	var out MeResponse
	status, err := c.do(ctx, http.MethodGet, "/api/v1/collectors/me", c.bearer(), nil, &out, http.StatusOK)
	return out, status, err
}

// Close closes a thread (key + origin match on the CP).
func (c *Client) Close(ctx context.Context, threadID string) (ThreadStateResponse, int, error) {
	var out ThreadStateResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/threads/"+threadID+"/close", c.bearer(), struct{}{}, &out, http.StatusOK)
	return out, status, err
}

// Reopen reopens a closed thread.
func (c *Client) Reopen(ctx context.Context, threadID string) (ThreadStateResponse, int, error) {
	var out ThreadStateResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/threads/"+threadID+"/reopen", c.bearer(), struct{}{}, &out, http.StatusOK)
	return out, status, err
}

// Handoff mints a 10-minute single-use owner handoff (201). The caller opens it
// in the browser; it is never persisted or logged.
func (c *Client) Handoff(ctx context.Context, threadID string) (HandoffResponse, int, error) {
	var out HandoffResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/threads/"+threadID+"/handoff", c.bearer(), struct{}{}, &out, http.StatusCreated)
	return out, status, err
}

// Summary fetches the thread's state summary (GET).
func (c *Client) Summary(ctx context.Context, threadID string) (ThreadSummary, int, error) {
	var out ThreadSummary
	status, err := c.do(ctx, http.MethodGet, "/api/v1/threads/"+threadID+"/summary", c.bearer(), nil, &out, http.StatusOK)
	return out, status, err
}

// ListThreads fetches the threads this collector created: GET
// /api/v1/threads?limit=<n> (CONTRACTS-CP §5.5a), Bearer collector key, scoped
// on the CP by `collector_id = auth.collector.id` — the caller never names a
// collector. Rows come back most-recently-active first (`updated_at DESC,
// created_at DESC, id ASC`); the order is fixed and there is no cursor.
//
// limit is the only query parameter: default 50, hard cap 200. A value outside
// [1,200] is a 400 on the CP, never a silent clamp, so callers pass a legal one
// (ListThreadsMaxLimit); <= 0 here means "let the CP apply its default".
func (c *Client) ListThreads(ctx context.Context, limit int) (ThreadListResponse, int, error) {
	path := "/api/v1/threads"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out ThreadListResponse
	status, err := c.do(ctx, http.MethodGet, path, c.bearer(), nil, &out, http.StatusOK)
	return out, status, err
}

// ListThreadsMaxLimit is §5.5a's hard cap — the biggest limit the CP accepts.
const ListThreadsMaxLimit = 200

// ReplaceLink = Replace thread link: revoke every outstanding thread-link token
// (and anonymous sessions) and mint a fresh one. Owner access and verified
// participants are untouched by the CP.
func (c *Client) ReplaceLink(ctx context.Context, threadID string) (PeekLinkResponse, int, error) {
	return c.PostPeekLink(ctx, threadID, PeekLinkRequest{RevokeExisting: true})
}

// do sends one JSON request with the given bearer and decodes a 2xx body into
// out. Any other status is returned as a *CPError with the CP's {error,message}
// decoded when present. The bearer never reaches an error string.
func (c *Client) do(ctx context.Context, method, path, bearer string, req any, out any, wantStatus ...int) (int, error) {
	var body io.Reader
	if req != nil {
		b, err := json.Marshal(req)
		if err != nil {
			return 0, fmt.Errorf("marshal %s: %w", path, err)
		}
		body = bytes.NewReader(b)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return 0, err
	}
	if req != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+bearer)
	httpReq.Header.Set("X-Flanj-Collector-Version", c.CollectorVersion)
	httpReq.Header.Set("X-Flanj-Schema-Version", fmt.Sprintf("%d", model.SchemaVersion))

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ok := false
	for _, s := range wantStatus {
		if resp.StatusCode == s {
			ok = true
			break
		}
	}
	if !ok {
		ce := &CPError{Status: resp.StatusCode, Path: method + " " + path}
		var eb struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			ce.Code, ce.Message = strings.TrimSpace(eb.Error), strings.TrimSpace(eb.Message)
		}
		return resp.StatusCode, ce
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}
