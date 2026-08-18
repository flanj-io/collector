// Package model holds the cross-component record types that travel the
// collector pipeline and promote to the control plane. Field tags mirror the
// frozen JSON Schemas in contracts/v1 (redacted-call.schema.json,
// finding.schema.json). Readers are tolerant of unknown fields (§7).
package model

// SchemaVersion is the frozen contract version carried by every record.
const SchemaVersion = 1

// Correlation carries the keys that make a finding actionable to a provider.
type Correlation struct {
	RequestID      string `json:"request_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
}

// Redaction records which floor patterns fired on a call.
type Redaction struct {
	Applied   bool     `json:"applied"`
	Patterns  []string `json:"patterns"`
	SpecAware bool     `json:"spec_aware"`
}

// RedactedCall is the stored, always-redacted representation of one HTTP call.
// Mirrors contracts/v1/redacted-call.schema.json.
type RedactedCall struct {
	SchemaVersion         int               `json:"schema_version"`
	ID                    string            `json:"id"`
	CapturedAt            string            `json:"captured_at"`
	Integration           string            `json:"integration"`
	Direction             string            `json:"direction"`
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
}

// Ptr is a small helper for the nullable string fields.
func Ptr(s string) *string { return &s }
