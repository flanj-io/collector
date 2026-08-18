// Package promote builds and sends the flag payload that lifts a redacted call +
// finding to the control plane (CONTRACTS §5, POST /api/v1/flags). It is the only
// outbound path in the collector besides OTLP ingest, and the only place a stored
// call leaves the customer environment — and only ever the redacted copy.
//
// Defense-in-depth: the human-facing message is run through the redaction floor
// before it is sent (§5: DLP scan on free-text), so a pasted secret can never
// ride out on the flag.
package promote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/redact"
)

// FlagRequest is the POST /api/v1/flags body. It matches
// contracts/cp-flag-request.schema.json exactly (additionalProperties:false).
type FlagRequest struct {
	IdempotencyKey      string             `json:"idempotency_key"`
	ConsumerDisplayName string             `json:"consumer_display_name"`
	InviteeEmail        string             `json:"invitee_email"`
	Message             string             `json:"message"`
	Call                model.RedactedCall `json:"call"`
	Finding             model.Finding      `json:"finding"`
}

// FlagResponse is the CP reply (201 created | 200 existing).
type FlagResponse struct {
	ThreadID   string `json:"thread_id"`
	PeekURL    string `json:"peek_url"`
	MagicToken string `json:"magic_token"`
	Status     string `json:"status"`
}

// Input is what the UI hands the promoter for one flag click.
type Input struct {
	ConsumerDisplayName string
	InviteeEmail        string
	Message             string // optional; a default is derived from the finding
	Call                model.RedactedCall
	Finding             model.Finding
}

// Build assembles a schema-valid FlagRequest. The idempotency key is derived
// from the finding id so re-flagging the same finding returns the existing
// thread (CONTRACTS §5). The message is redacted defense-in-depth.
func Build(in Input) FlagRequest {
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = defaultMessage(in.Finding)
	}
	msg = redact.New().Redact(msg).Text
	return FlagRequest{
		IdempotencyKey:      "flag_" + in.Finding.ID,
		ConsumerDisplayName: in.ConsumerDisplayName,
		InviteeEmail:        in.InviteeEmail,
		Message:             msg,
		Call:                in.Call,
		Finding:             in.Finding,
	}
}

func defaultMessage(f model.Finding) string {
	if f.Detail != "" {
		return fmt.Sprintf("%s on %s. %s", drift(f), f.Endpoint, f.Detail)
	}
	return fmt.Sprintf("%s on %s: expected %s, saw %s.", drift(f), f.Endpoint, f.Expected, f.Actual)
}

func drift(f model.Finding) string {
	if f.Kind == model.KindVersionDiff {
		return "Breaking spec change"
	}
	return "Contract drift"
}

// Client posts flags to the control plane.
type Client struct {
	BaseURL          string
	DeployToken      string
	CollectorVersion string
	HTTP             *http.Client
}

// NewClient builds a Client with a sane timeout.
func NewClient(baseURL, deployToken, collectorVersion string) *Client {
	return &Client{
		BaseURL:          strings.TrimRight(baseURL, "/"),
		DeployToken:      deployToken,
		CollectorVersion: collectorVersion,
		HTTP:             &http.Client{Timeout: 15 * time.Second},
	}
}

// Post sends one flag. Bearer <cp_deploy_token> is the only outbound auth
// (CONTRACTS §5/§8); the schema + collector versions travel as headers.
func (c *Client) Post(ctx context.Context, req FlagRequest) (FlagResponse, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return FlagResponse{}, 0, fmt.Errorf("marshal flag: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/flags", bytes.NewReader(body))
	if err != nil {
		return FlagResponse{}, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.DeployToken)
	httpReq.Header.Set("X-Vinifera-Collector-Version", c.CollectorVersion)
	httpReq.Header.Set("X-Vinifera-Schema-Version", fmt.Sprintf("%d", model.SchemaVersion))

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return FlagResponse{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return FlagResponse{}, resp.StatusCode, fmt.Errorf("flag POST returned %d", resp.StatusCode)
	}
	var out FlagResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return FlagResponse{}, resp.StatusCode, fmt.Errorf("decode flag response: %w", err)
	}
	return out, resp.StatusCode, nil
}
