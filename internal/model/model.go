// Package model holds the cross-component record types that travel the
// collector pipeline and promote to the control plane. Field tags mirror the
// frozen JSON Schemas in contracts/v1 (redacted-call.schema.json,
// finding.schema.json). Readers are tolerant of unknown fields (§7).
package model

import "github.com/vinifera-io/collector/internal/redact"

// SchemaVersion is the frozen contract version carried by every record.
const SchemaVersion = 1

// Correlation carries the keys that make a finding actionable to a provider.
type Correlation struct {
	RequestID      string `json:"request_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
}

// RedactedFieldRecord is one whole-value body redaction with the ORIGINAL value's
// captured, non-reversible properties (wire: redaction.fields[] — CONTRACTS §2/§6;
// redacted-call.schema.json). Part names which body the RFC 6901 path points into;
// the embedded redact.RedactedField contributes path/pattern/props.
type RedactedFieldRecord struct {
	// Part is "request" or "response".
	Part string `json:"part"`
	redact.RedactedField
}

// Redaction records which floor patterns fired on a call, and (optionally) the
// whole-value field records the drift detector uses to validate the decidable
// constraints of redacted fields (absent/empty when none fired).
type Redaction struct {
	Applied   bool                  `json:"applied"`
	Patterns  []string              `json:"patterns"`
	SpecAware bool                  `json:"spec_aware"`
	Fields    []RedactedFieldRecord `json:"fields,omitempty"`
}

// RedactedCall is the stored, always-redacted representation of one HTTP call.
// Mirrors contracts/v1/redacted-call.schema.json (readers tolerate the extra
// peer_host/edge_class local-discovery fields — additionalProperties:true).
type RedactedCall struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	CapturedAt    string `json:"captured_at"`
	Integration   string `json:"integration"`
	Direction     string `json:"direction"`
	// PeerHost is the other end's host[:port] — the edge key (CONTRACTS §2
	// vinifera.peer.host). Local discovery metadata; not part of the frozen
	// RedactedCall surface but carried for edge attribution.
	PeerHost string `json:"peer_host,omitempty"`
	// EdgeClass is the SDK/heuristic classification of PeerHost: external|internal.
	EdgeClass             string            `json:"edge_class,omitempty"`
	Method                string            `json:"method"`
	URL                   string            `json:"url"`
	Route                 string            `json:"route"`
	StatusCode            int               `json:"status_code"`
	RequestHeaders        map[string]string `json:"request_headers,omitempty"`
	RequestBody           string            `json:"request_body"`
	RequestBodyTruncated  bool              `json:"request_body_truncated"`
	RequestContentType    string            `json:"request_content_type,omitempty"`
	ResponseHeaders       map[string]string `json:"response_headers,omitempty"`
	ResponseBody          string            `json:"response_body"`
	ResponseBodyTruncated bool              `json:"response_body_truncated"`
	ResponseContentType   string            `json:"response_content_type,omitempty"`
	Correlation           Correlation       `json:"correlation"`
	DurationMS            int               `json:"duration_ms,omitempty"`
	Redaction             Redaction         `json:"redaction"`
}

// Finding kinds and severities (contracts §4).
const (
	KindLiveVsSpec  = "live-vs-spec"
	KindVersionDiff = "version-diff"

	SeverityBreaking = "breaking"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Finding is a technical-adherence drift record. Mirrors
// contracts/v1/finding.schema.json.
type Finding struct {
	SchemaVersion   int     `json:"schema_version"`
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Severity        string  `json:"severity"`
	Integration     string  `json:"integration"`
	Endpoint        string  `json:"endpoint"`
	FieldPath       *string `json:"field_path"`
	Location        *string `json:"location"`
	Expected        string  `json:"expected"`
	Actual          string  `json:"actual"`
	Rule            string  `json:"rule"`
	SpecVersionFrom *string `json:"spec_version_from"`
	SpecVersionTo   *string `json:"spec_version_to"`
	SourceCallID    *string `json:"source_call_id"`
	DetectedAt      string  `json:"detected_at"`
	Detail          string  `json:"detail,omitempty"`

	// Dedup fields (CONTRACTS §4): a drift is per-endpoint, not per-call. The
	// Signature (integration|endpoint|kind|rule|field_path) collapses every call
	// carrying the SAME drift into ONE finding; OccurrenceCount counts them and
	// First/LastSeen bound the window. SourceCallID is a REPRESENTATIVE call.
	Signature       string `json:"signature,omitempty"`
	OccurrenceCount int    `json:"occurrence_count,omitempty"`
	FirstSeen       string `json:"first_seen,omitempty"`
	LastSeen        string `json:"last_seen,omitempty"`
}

// Edge is a discovered integration edge, keyed by (peer_host, direction). Edges
// are derived from observed traffic — no target list is configured. role and the
// edge orientation fall out of direction; class is carried from the call's
// edge.class. Only external edges are surfaced.
type Edge struct {
	PeerHost   string `json:"peer_host"`
	Direction  string `json:"direction"`
	Role       string `json:"role"`
	Class      string `json:"class"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	CallCount  int    `json:"call_count"`
	DriftCount int    `json:"drift_count"`
}

// SpecInfo roles: whose contract this is.
const (
	// SpecRoleProvider is a contract a provider we consume publishes — validated
	// against our OUTBOUND (client-direction) traffic.
	SpecRoleProvider = "provider"
	// SpecRoleSelf is the contract WE publish as a provider — validated against
	// our INBOUND (server-direction) responses.
	SpecRoleSelf = "self"
)

// SpecInfo describes an API contract (spec) loaded by the drift processor,
// surfaced on the local UI's Contracts tab. Local metadata only — not part of
// the frozen contract surface.
type SpecInfo struct {
	Integration string `json:"integration"`
	// Role is SpecRoleProvider (their API, our egress) or SpecRoleSelf (our
	// API, our ingress).
	Role string `json:"role"`
	// PeerHost is the discovered edge this spec is matched to (empty when the
	// spec applies to all captured calls).
	PeerHost string `json:"peer_host,omitempty"`
	// Format is the contract document type, e.g. "openapi" (future: "asyncapi").
	Format  string `json:"format"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
	// DocsURL is the spec's externalDocs link when the provider publishes one.
	DocsURL   string `json:"docs_url,omitempty"`
	Endpoints int    `json:"endpoints,omitempty"`
	LoadedAt  string `json:"loaded_at"`
}

// ComputeSignature returns the dedup key for a finding:
// integration|endpoint|kind|rule|field_path (CONTRACTS §4). All calls carrying
// the SAME drift share this signature and collapse into one finding.
func (f Finding) ComputeSignature() string {
	field := ""
	if f.FieldPath != nil {
		field = *f.FieldPath
	}
	return f.Integration + "|" + f.Endpoint + "|" + f.Kind + "|" + f.Rule + "|" + field
}

// Ptr is a small helper for the nullable string fields.
func Ptr(s string) *string { return &s }
