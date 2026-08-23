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
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/redact"
)

// FlagRequest is the POST /api/v1/flags body. It matches
// contracts/cp-flag-request.schema.json exactly (additionalProperties:false).
// v0.1a retired invitee_email: the CP sends no invite email — the consumer
// copies the Thread link into the channel the two teams already share.
type FlagRequest struct {
	IdempotencyKey      string             `json:"idempotency_key"`
	ConsumerDisplayName string             `json:"consumer_display_name"`
	ProviderDisplayName string             `json:"provider_display_name"`
	Message             string             `json:"message"`
	Call                model.RedactedCall `json:"call"`
	Finding             model.Finding      `json:"finding"`
}

// FlagResponse is the CP reply (201 created | 200 existing). ThreadURL is the
// Thread link the consumer copies; PeekURL/MagicToken are the deprecated v0
// aliases an older CP still sends (ThreadURL falls back to PeekURL on decode).
type FlagResponse struct {
	ThreadID       string `json:"thread_id"`
	ThreadPublicID string `json:"thread_public_id"`
	ThreadURL      string `json:"thread_url"`
	PeekURL        string `json:"peek_url,omitempty"`
	MagicToken     string `json:"magic_token,omitempty"`
	State          string `json:"state"`
	Status         string `json:"status"`
}

// Input is what the UI hands the promoter for one flag click.
type Input struct {
	ConsumerDisplayName string
	ProviderDisplayName string // optional; defaults to the humanized integration id
	Message             string // optional; a default is derived from the finding
	Call                model.RedactedCall
	Finding             model.Finding
}

// Build assembles a schema-valid FlagRequest. The idempotency key is derived
// from the finding id so re-flagging the same finding returns the existing
// thread (CONTRACTS §5). The message is redacted defense-in-depth. When no
// provider display name is supplied it defaults to the humanized integration id
// (CONTRACTS §5/§8), so the peek/thread always names the provider side.
func Build(in Input) FlagRequest {
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = defaultMessage(in.Finding)
	}
	msg = redact.New().Redact(msg).Text
	provider := strings.TrimSpace(in.ProviderDisplayName)
	if provider == "" {
		provider = HumanizeIntegration(in.Call.Integration)
	}
	return FlagRequest{
		IdempotencyKey:      "flag_" + in.Finding.ID,
		ConsumerDisplayName: in.ConsumerDisplayName,
		ProviderDisplayName: provider,
		Message:             msg,
		Call:                in.Call,
		Finding:             in.Finding,
	}
}

// HumanizeIntegration turns an integration id into a human display name: split
// on '-', '_', or space and Title Case each word. e.g. "acme-payments" ->
// "Acme Payments". Shared humanize rule across the stack.
func HumanizeIntegration(id string) string {
	fields := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, f := range fields {
		runes := []rune(strings.ToLower(f))
		runes[0] = unicode.ToUpper(runes[0])
		fields[i] = string(runes)
	}
	return strings.Join(fields, " ")
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

// Client talks to the control plane. DeployToken is the install-time
// cp_deploy_token (Bearer for register only, pre-Connect); CollectorKey is the
// per-deployment key register returned (Bearer for everything else). Neither is
// ever logged.
type Client struct {
	BaseURL          string
	DeployToken      string
	CollectorKey     string
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

// Post sends one flag (POST /api/v1/flags). Bearer = the collector key once
// Connected (CONTRACTS §5; the CP answers 412 not_connected |
// contact_unconfirmed otherwise); the schema + collector versions travel as
// headers. 201 created | 200 existing (idempotent replay).
func (c *Client) Post(ctx context.Context, req FlagRequest) (FlagResponse, int, error) {
	var out FlagResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/flags", c.bearer(), req, &out, http.StatusCreated, http.StatusOK)
	if err != nil {
		return FlagResponse{}, status, err
	}
	if out.ThreadURL == "" {
		out.ThreadURL = out.PeekURL // older CP: peek_url was the only name
	}
	return out, status, nil
}
