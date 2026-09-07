// Package model holds the cross-component record types that travel the
// collector pipeline and promote to the control plane. Field tags mirror the
// frozen JSON Schemas in contracts/ (redacted-call.schema.json,
// finding.schema.json). Readers are tolerant of unknown fields (§7).
package model

import "github.com/flanj-io/collector/internal/redact"

// SchemaVersion is the frozen contract version carried by every record.
const SchemaVersion = 1

// Correlation carries the keys that make a finding actionable to a provider.
type Correlation struct {
	RequestID      string `json:"request_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
	// ClientRequestID is the JSON-RPC id observed on the client's OWN outgoing
	// MCP message (CONTRACTS §2 flanj.corr.client_request_id, v0.5). It is
	// CLIENT-generated: it appears in the provider's logs only if they log it.
	// Rendered as "JSON-RPC id (client-generated)" and NEVER merged into
	// RequestID, which stays provider-issued only.
	ClientRequestID string `json:"client_request_id,omitempty"`
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
// Mirrors contracts/redacted-call.schema.json (readers tolerate the extra
// peer_host/edge_class local-discovery fields — additionalProperties:true).
type RedactedCall struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	CapturedAt    string `json:"captured_at"`
	Integration   string `json:"integration"`
	Direction     string `json:"direction"`
	// PeerHost is the other end's host[:port] — the edge key (CONTRACTS §2
	// flanj.peer.host). Local discovery metadata; not part of the frozen
	// RedactedCall surface but carried for edge attribution.
	PeerHost string `json:"peer_host,omitempty"`
	// PeerAddr is the peer's socket address (IP) when the SDK captured one
	// (CONTRACTS §2 flanj.peer.addr, optional). Transport detail for display;
	// never an identity or edge key.
	PeerAddr string `json:"peer_addr,omitempty"`
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

	// v0.5 MCP fields (CONTRACTS §2 "MCP tool-call records"; additive — absent
	// on HTTP records, tolerated by every reader via additionalProperties:true).
	// Transport is "mcp" for an MCP tools/call record; empty means HTTP.
	Transport string `json:"transport,omitempty"`
	// MCPToolName is the called tool — the operation id detection matches
	// against the contract (Operation.ID / Match.ToolName).
	MCPToolName string `json:"mcp_tool_name,omitempty"`
	// MCPIsError mirrors the CallToolResult's isError (also true when the call
	// itself rejected). Feeds the error-rate metric; never a finding on its own.
	MCPIsError bool `json:"mcp_is_error,omitempty"`
	// MCPServerName / MCPServerVersion carry serverInfo when the client
	// surfaced it (never guessed).
	MCPServerName    string `json:"mcp_server_name,omitempty"`
	MCPServerVersion string `json:"mcp_server_version,omitempty"`
	// MCPProtocolVersion is the negotiated MCP protocol version, when surfaced.
	MCPProtocolVersion string `json:"mcp_protocol_version,omitempty"`
	// MCPSessionID is the Mcp-Session-Id when the transport exposes one.
	// Protocol sessions were removed in MCP revision 2026-07-28, so this is
	// permanently absent against a current server.
	MCPSessionID string `json:"mcp_session_id,omitempty"`
	// MCPResultType is the result's `resultType` (revision 2026-07-28):
	// "complete", "input_required", or a later revision's value, verbatim.
	// Empty means an older server said nothing — NOT "complete".
	//
	// `input_required` is normal traffic on an interactive tool: the server is
	// asking for more input, so the payload is partial BY DESIGN. Detection
	// skips it rather than judging it, or every elicitation turn becomes a
	// finding.
	MCPResultType string `json:"mcp_result_type,omitempty"`
	// MCPTaskID is set when the result was a Tasks HANDLE rather than a payload
	// (revision 2026-07-28 moved long-running work to the Tasks extension: the
	// call returns `{task:{taskId,…}}` and the payload arrives via tasks/get).
	// Such a record carries the ENVELOPE, never the tool's output, so nothing
	// may validate or model response shape from it.
	MCPTaskID string `json:"mcp_task_id,omitempty"`
	// Drifted is TRUE when THIS call produced a finding saying the call itself
	// departed from a contract — live-vs-spec on REST, output_mismatch on MCP
	// (store.marksSourceCallDrifted owns the list). Set by the store on every
	// finding insert, including repeat occurrences of a signature that was
	// already recorded.
	//
	// NOT set by definition_change (the snapshot detector: no call produced it),
	// stale_client (the consumer's own arguments were stale — the provider's
	// contract was not departed from) or version-diff (document vs document).
	//
	// It exists because the UI otherwise had to ask "has this ENDPOINT ever
	// drifted?", which marked every call on the endpoint — including ones that
	// conformed, and ones captured BEFORE the drift — as drifted. Drift is a
	// property of a CALL; anything coarser is a false accusation against the
	// provider and against the operator's own reading of their traffic.
	Drifted bool `json:"drifted,omitempty"`

	// Validated is the drift processor's OWN verdict on THIS call — stamped
	// where validation runs, at the moment it ran or explicitly could not
	// (CONTRACTS §2 `flanj.validated`, §3). One of ValidatedClean,
	// ValidatedDrifted, ValidatedNot (ValidatedReason says why) or
	// ValidatedUnknown (the record reached the store carrying no verdict);
	// EMPTY on a row stored before verdicts were recorded, and on nothing else.
	//
	// It exists because the UI used to DERIVE "was this call checked?" from
	// facts about the EDGE: a contract bound to the host, later refined to a
	// contract bound BEFORE the call was captured. That mirror said `checked`
	// over calls the processor never validated — the operator's upload had
	// landed in the store while the processor's spec cache still held nothing
	// for the host (up to five seconds on a single pod, ten on a tiered front,
	// forever on a front whose store_pod_token is wrong) — and a call nothing
	// judged, with no finding and no drifted flag, rendered CONFORMING. Only the
	// process that validates can say whether it did; this is it saying so.
	Validated string `json:"validated,omitempty"`
	// ValidatedReason names WHY when Validated is ValidatedNot — one of the
	// NotValidated* constants, the first gate the processor hit in its own
	// order. Empty otherwise.
	ValidatedReason string `json:"validated_reason,omitempty"`
}

// Per-call validation verdicts — RedactedCall.Validated, stamped by the drift
// processor as CONTRACTS §2 `flanj.validated` (additive; schema_version stays 1).
const (
	// ValidatedClean: the processor validated the call and found nothing.
	ValidatedClean = "clean"
	// ValidatedDrifted: validated, and the call departed from its contract — a
	// per-call finding (PerCallDriftKinds) names it.
	ValidatedDrifted = "drifted"
	// ValidatedNot: the processor saw the call and explicitly could not
	// validate it; RedactedCall.ValidatedReason says why.
	ValidatedNot = "not-validated"
	// ValidatedUnknown: the record reached the store carrying no verdict at
	// all — the collector that captured it predates the stamp, or its pipeline
	// runs no drift processor. Decoder-assigned (otlpattr.CallFromRecord), never
	// stamped by a processor. Readers treat it as NOT validated.
	ValidatedUnknown = "unknown"
)

// Why a call was not validated — RedactedCall.ValidatedReason, CONTRACTS §2
// `flanj.validated.reason`. Named after the processor's own gates, in the order
// it applies them, so the reason stamped is the first one that stopped it.
const (
	// NotValidatedNoContract: nothing was bound to the call's edge in the
	// processor's cache at that moment. REST: no upload for the host, an upload
	// not yet loaded, a tiered front that cannot reach the store pod, no self
	// contract configured. MCP: no tools/list snapshot observed yet.
	NotValidatedNoContract = "no-contract"
	// NotValidatedNotRoutable: a document was bound but could not route the call
	// (method + path not in it, or the request could not be reconstructed).
	NotValidatedNotRoutable = "not-routable"
	// NotValidatedStatusUndeclared: REST — the bound document routes the call
	// but declares no response for this status (a 422 the contract never
	// mentions), so there was no schema to compare the body to. kin-openapi
	// reports it as a ResponseError with no SchemaError, which the finding path
	// drops — until 2026-09-07 that read as "no findings", i.e. clean. Not a
	// finding kind yet (the detector reports schema violations only), but never
	// clean. Split from the media-type case because the operator's fix differs:
	// declare the status, versus declare (or map) the media type.
	NotValidatedStatusUndeclared = "status-undeclared"
	// NotValidatedMediaTypeUndeclared: REST — the status is declared, but not
	// with this media type (an `application/problem+json` body under a
	// contract that declares only `application/json` for it). Same refusal
	// shape as above.
	//
	// TRANSITIONAL. flanj-io/collector#44 turns exactly this case into a
	// live-vs-spec finding (rule `content-type-mismatch`: the provider's own
	// response shape departed from what it published), so the call is stamped
	// drifted via VerdictOf, and it validates an RFC 6839 `+json` body against
	// the declared application/json schema before any of these gates. Whichever
	// of the two lands second removes this constant on rebase — the
	// status-undeclared case above stays not-validated either way (a gateway's
	// `502 text/html` is not the provider breaching its contract).
	NotValidatedMediaTypeUndeclared = "media-type-undeclared"
	// NotValidatedBodyNotDecodable: REST — the response body could not be read
	// or decoded as its declared media type, so nothing was compared.
	NotValidatedBodyNotDecodable = "body-not-decodable"
	// NotValidatedValidatorError: the validator refused the call for a reason
	// the collector does not classify; nothing was compared.
	NotValidatedValidatorError = "validator-error"
	// NotValidatedToolNotListed: MCP — the CURRENT tools/list does not declare
	// the called tool (a stale_client finding says so); there is nothing to
	// judge the result against.
	NotValidatedToolNotListed = "tool-not-listed"
	// NotValidatedInputRequired: MCP — resultType input_required, a mid-flight
	// exchange whose payload is partial by design.
	NotValidatedInputRequired = "input-required"
	// NotValidatedNoOutputContract: MCP — the tool declares no outputSchema.
	NotValidatedNoOutputContract = "no-output-contract"
	// NotValidatedErrorResult: MCP — isError; error output is not contract evidence.
	NotValidatedErrorResult = "error-result"
	// NotValidatedTaskHandle: MCP — the result was a Tasks handle, an envelope.
	NotValidatedTaskHandle = "task-handle"
	// NotValidatedResultNotJSON: MCP — no complete JSON structuredContent to
	// judge (empty, truncated, not application/json, or not parseable).
	NotValidatedResultNotJSON = "result-not-json"
)

// Validation is one call's verdict as the drift processor stamps it.
type Validation struct {
	Verdict string
	// Reason is set iff Verdict == ValidatedNot.
	Reason string
}

// NotValidated builds the verdict for a call the processor could not judge.
func NotValidated(reason string) Validation {
	return Validation{Verdict: ValidatedNot, Reason: reason}
}

// VerdictOf is the verdict for a call the processor DID judge: drifted when any
// finding it produced is a per-call drift kind, clean otherwise. A stale_client
// finding on its own leaves the verdict clean — it is about the consumer's
// arguments, not the provider's response.
func VerdictOf(findings []Finding) Validation {
	for _, f := range findings {
		if MarksCallDrifted(f.Kind) {
			return Validation{Verdict: ValidatedDrifted}
		}
	}
	return Validation{Verdict: ValidatedClean}
}

// PerCallDriftKinds are the finding kinds that mean THE CALL they name departed
// from its contract — one per transport, and the same fact: live-vs-spec (a
// REST response violated the bound OpenAPI document) and output_mismatch (an
// MCP result's structuredContent violated the tool's own declared outputSchema,
// internal/drift/mcp.go). Both are per-call and carry a SourceCallID.
//
// The kinds that must NOT qualify:
//   - definition_change — the SNAPSHOT detector, comparing two tools/list
//     observations. Call-less: no call produced it, so no call drifted.
//   - stale_client — the CONSUMER's own arguments were stale. The call is
//     evidence about this agent, not about the provider's contract, and
//     marking it drifted would accuse the provider of our bug.
//   - version-diff — a document-to-document comparison, not traffic.
//
// ONE list, read by everything that answers "did this call drift": the drift
// processor's per-call stamp (VerdictOf) and the store's `calls.drifted` mark —
// on insert, on InsertFinding for both backends, and on the late-pin repair. A
// kind honoured on one path and not another makes a call's verdict depend on
// which record arrived first.
var PerCallDriftKinds = []string{KindLiveVsSpec, KindOutputMismatch}

// MarksCallDrifted reports whether kind is one of PerCallDriftKinds.
func MarksCallDrifted(kind string) bool {
	for _, k := range PerCallDriftKinds {
		if kind == k {
			return true
		}
	}
	return false
}

// Finding kinds and severities (contracts §4).
const (
	KindLiveVsSpec  = "live-vs-spec"
	KindVersionDiff = "version-diff"

	// v0.5 MCP finding kinds (spec §1/§4.C).
	// KindOutputMismatch: a tool call's structuredContent violates the tool's
	// declared outputSchema. FLAGGABLE — the purest evidence-rule case: their
	// schema vs their own response.
	KindOutputMismatch = "output_mismatch"
	// KindDefinitionChange: two consecutive observed tools/list snapshots
	// differ; one finding per (edge, operation, rule, fieldPath) with the
	// classifier's class. FLAGGABLE at every class — BREAKING, NON_BREAKING and
	// (since qfix2-2026-08-26) DESCRIPTION: the evidence is the provider's own
	// published tools/list text, two content-hashed snapshots with observation
	// timestamps, which they can verify by reading their own two versions.
	// Flagging is always a human pressing the control; nothing auto-flags.
	KindDefinitionChange = "definition_change"
	// KindStaleClient: the consumer's agent called a tool absent from the
	// CURRENT tools/list, or with args violating the current inputSchema.
	// Consumer-side, LOCAL ONLY — never flaggable, no flag control anywhere.
	KindStaleClient = "stale_client"

	// MCPResultTypeComplete / MCPResultTypeInputRequired are the two `resultType`
	// values MCP revision 2026-07-28 defines. They are compared, never assumed:
	// an EMPTY MCPResultType is an older server, not a complete result.
	MCPResultTypeComplete      = "complete"
	MCPResultTypeInputRequired = "input_required"

	SeverityBreaking = "breaking"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// RuleDescriptionChanged mirrors contract/diff.RuleDescriptionChanged (a test
// in internal/drift pins the equality). Kept as a mirror so this package —
// plain record types — does not import the classifier.
const RuleDescriptionChanged = "description-changed"

// Flaggable reports whether this finding may be flagged cross-org (v0.5 spec
// §6 evidence rule, AMENDED qfix2-2026-08-26): stale_client is consumer-side —
// it is local-only, the relay REFUSES it server-side, the UI shows no flag
// control anywhere, and it never reaches the control plane.
//
// DESCRIPTION-only definition changes are no longer local-only. The evidence
// rule is amended, not broken: a description change IS verifiable in the
// provider's own systems (their published tools/list text, before and after,
// content-hashed and timestamped). What failed the bar was the claim, not the
// evidence — the flag sheet now carries the claim honestly. Nothing auto-flags:
// a description change only ever leaves this collector when a human presses the
// control.
func (f Finding) Flaggable() bool {
	// stale_client only. Never widen this without re-reading the evidence rule.
	return f.Kind != KindStaleClient
}

// Finding is a technical-adherence drift record. Mirrors
// contracts/finding.schema.json.
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
	// SnapshotObservedAt (v0.5, additive, optional — CONTRACTS §4 MCP block) is
	// the ObservedAt of the tools/list snapshot backing an MCP finding: the
	// CURRENT snapshot the call was validated against for output_mismatch, the
	// AFTER snapshot for definition_change. Empty on every other kind (and on
	// findings from older collectors) — readers must tolerate its absence.
	SnapshotObservedAt string `json:"snapshot_observed_at,omitempty"`
	// SnapshotObservedFrom (additive, optional — CONTRACTS §4 MCP block) is the
	// PREVIOUS snapshot's ObservedAt on a definition_change finding — the
	// structured sibling of SnapshotObservedAt (which stays the AFTER snapshot),
	// so readers never have to parse the Detail prose for the before-time. Empty
	// on every other kind and on findings from older collectors — readers must
	// tolerate its absence.
	SnapshotObservedFrom string `json:"snapshot_observed_from,omitempty"`

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

// SpecInfo formats: the contract document type behind a spec_infos row.
const (
	SpecFormatOpenAPI = "openapi"
	// SpecFormatMCP marks an observed MCP tools/list snapshot (v0.5 Step C) —
	// the raw doc stored alongside is the snapshot JSON exactly as captured
	// ({"tools":[…], "serverInfo"?, …}), decodable by contract.ParseToolsList.
	SpecFormatMCP = "mcp"

	// SpecSourceUpload marks a contract an operator uploaded in the UI. It is
	// the only way a PROVIDER contract enters the collector (CONTRACTS §8 —
	// `spec_path` was removed 2026-08-31), and it never leaves.
	SpecSourceUpload = "upload"
	// SpecSourceConfig marks a contract loaded from a mounted file — in v1 that
	// is `self_spec_path` and nothing else.
	SpecSourceConfig = "config"
	// SpecSourceObserved marks a contract the traffic delivered: an MCP
	// tools/list snapshot, which needs no configuring and no uploading.
	SpecSourceObserved = "observed"

	// Edge classes, shared by RedactedCall and SpecInfo. `local-process` is the
	// SDK's own word for a server spawned as a child process (MCP over stdio);
	// it never appears on GET /api/edges, which is external-only.
	EdgeClassExternal     = "external"
	EdgeClassInternal     = "internal"
	EdgeClassLocalProcess = "local-process"
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
	// EdgeClass mirrors RedactedCall.EdgeClass exactly — external | internal |
	// local-process — and exists so the UI can tell two contracts apart when
	// their titles collide.
	//
	// It has to be here rather than derived from the edges list: a
	// `local-process` MCP server (stdio) has NO edge row at all, because
	// GET /api/edges is external-only. So the one fact that distinguishes a
	// stdio server from its HTTP twin is unreachable from every other source.
	// Reusing `edge_class` rather than inventing `transport` is deliberate:
	// `transport` already means "mcp" on a call, and a second meaning for a
	// shipped word is how the next collision gets built.
	EdgeClass string `json:"edge_class,omitempty"`
	// Format is the contract document type, e.g. "openapi" (future: "asyncapi").
	Format  string `json:"format"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
	// DocsURL is the spec's externalDocs link when the provider publishes one.
	DocsURL   string `json:"docs_url,omitempty"`
	Endpoints int    `json:"endpoints,omitempty"`
	LoadedAt  string `json:"loaded_at"`

	// Source is how this contract got here: SpecSourceUpload (an operator
	// uploaded it in the UI), SpecSourceConfig (a mounted self_spec_path), or
	// SpecSourceObserved (an MCP tools/list, which delivers itself). The UI's
	// provenance word tracks it — "uploaded" vs "loaded" — so which one is live
	// is legible on sight. Empty means config, for rows written before uploads
	// existed.
	Source string `json:"source,omitempty"`

	// PrevVersion / PrevLoadedAt describe the document this one REPLACED, kept
	// at N=2 (one previous, no archive — nobody wants a spec museum in a
	// localhost debugging tool). They are what lets the card read
	// "updated 2 hours ago · v2.1.0 · replaced v1.0.0", and what makes the
	// version diff on replace a switch rather than a migration.
	PrevVersion  string `json:"prev_version,omitempty"`
	PrevLoadedAt string `json:"prev_loaded_at,omitempty"`
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
