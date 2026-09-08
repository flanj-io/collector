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

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/redact"
)

// FlagRequest is the POST /api/v1/flags body. It matches
// contracts/cp-flag-request.schema.json (additionalProperties:false).
// v0.1a retired invitee_email: the CP sends no invite email — the consumer
// copies the Thread link into the channel the two teams already share.
//
// Call is a POINTER since qfix2-2026-08-26: a definition_change is CALL-LESS by
// nature — its evidence is the provider's own two published tools/list
// snapshots, not a failing call — so the field is omitted entirely rather than
// carrying a fabricated or empty call record. We never invent evidence.
//
// Finding is a POINTER since v1p4-2026-09-08 for the same reason one step
// further: a QUESTION thread started from an edge row has no finding either — an
// edge is a registrable domain, not a drift — so the key is omitted rather than
// carrying a zero-valued Finding, which would fail the CP's schema read and
// would be a fabricated artifact if it did not.
//
// ProviderHost rides only on a call-less request: the domain is the anchor and
// the name is decoration (CONTRACTS §5), so a thread with no call still names
// the edge it is about and the thread page can resolve a verified directory name
// for its provider slot instead of an unattributed consumer-asserted one.
type FlagRequest struct {
	IdempotencyKey      string              `json:"idempotency_key"`
	ConsumerDisplayName string              `json:"consumer_display_name"`
	ProviderDisplayName string              `json:"provider_display_name"`
	ProviderHost        string              `json:"provider_host,omitempty"`
	Message             string              `json:"message"`
	Call                *model.RedactedCall `json:"call,omitempty"`
	Finding             *model.Finding      `json:"finding,omitempty"`
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
	// Call is the redacted failing call — nil for a CALL-LESS finding
	// (definition_change), whose evidence is the two published snapshots the
	// Finding itself carries.
	Call    *model.RedactedCall
	Finding model.Finding
}

// QuestionInput is what the UI hands the promoter for one "Start a thread" click
// on an EDGE row (v1p4). There is no finding and no call: the operator is asking
// about a counterparty, not reporting drift on one. The message is therefore
// REQUIRED — the CP refuses a flag that carries neither evidence nor words — and
// the idempotency key comes from the caller, because an edge has no finding id to
// derive one from and two different questions about one edge are two threads.
type QuestionInput struct {
	IdempotencyKey      string
	ConsumerDisplayName string
	ProviderDisplayName string
	// ProviderHost is the edge's observed host — the anchor for the thread page's
	// provider slot. Empty is legal; it just costs the verified-name resolution.
	ProviderHost string
	Message      string
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
		// A call-less finding names its own integration.
		integration := in.Finding.Integration
		if in.Call != nil {
			integration = in.Call.Integration
		}
		provider = HumanizeIntegration(integration)
	}
	finding := in.Finding
	return FlagRequest{
		IdempotencyKey:      "flag_" + in.Finding.ID,
		ConsumerDisplayName: in.ConsumerDisplayName,
		ProviderDisplayName: provider,
		Message:             msg,
		Call:                in.Call,
		Finding:             &finding,
	}
}

// BuildQuestion assembles a schema-valid message-only FlagRequest: no call, no
// finding, and a message that must already be non-empty (the caller validates it
// so the operator sees the refusal in the sheet, not as a CP round-trip). The
// message is redacted defense-in-depth on this path too — free text is free text,
// and a question is exactly where someone pastes the response they are asking
// about.
func BuildQuestion(in QuestionInput) FlagRequest {
	return FlagRequest{
		IdempotencyKey:      in.IdempotencyKey,
		ConsumerDisplayName: in.ConsumerDisplayName,
		ProviderDisplayName: strings.TrimSpace(in.ProviderDisplayName),
		ProviderHost:        strings.TrimSpace(in.ProviderHost),
		Message:             redact.New().Redact(strings.TrimSpace(in.Message)).Text,
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

// defaultMessage is what the provider reads when the flag arrives with no
// message. The sheet prefills one and labels the field "Message (optional)", so
// an emptied textarea lands HERE — the default is a shipped user-facing string,
// not a fallback nobody sees.
func defaultMessage(f model.Finding) string {
	// DESCRIPTION definition changes (ux-design-v2 §2.7.4) ask a question; they
	// never make a defect claim. "Contract drift on <tool>. Definition change
	// (DESCRIPTION): …" would file a wording change at the provider as a defect
	// — the exact mute risk the sheet's guard line ("This isn't a bug report —
	// you're asking whether the change was intended.") exists to prevent. So the
	// default here is the sheet's own prefilled question, verbatim.
	if f.Kind == model.KindDefinitionChange && f.Rule == model.RuleDescriptionChanged {
		on := ""
		if d := shortDate(f.SnapshotObservedAt); d != "" {
			on = " on " + d
		}
		return fmt.Sprintf("Your tools/list description for %s changed%s. The schema didn't change, but the wording did, and our agent picks tools from that text. Can you confirm the new wording is intended and stable?", f.Endpoint, on)
	}
	if f.Detail != "" {
		return fmt.Sprintf("%s on %s. %s", drift(f), f.Endpoint, f.Detail)
	}
	return fmt.Sprintf("%s on %s: expected %s, saw %s.", drift(f), f.Endpoint, f.Expected, f.Actual)
}

// shortDate renders an ISO-8601 instant the way the sheet does (ui/src/threads.ts
// shortDate — month short + day numeric), so a message typed in the sheet and
// one produced here read the same. An absent or unparseable time yields "" and
// the caller drops the clause rather than printing a broken date.
func shortDate(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return t.UTC().Format("Jan 2")
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
