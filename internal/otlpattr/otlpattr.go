// Package otlpattr maps between the frozen flanj.* OTLP log attributes
// (contracts §2) and the collector's in-process record types. Calls arrive from
// the SDK as attributes; findings are carried through the internal pipeline as a
// single JSON attribute so the store exporter can reconstruct them.
package otlpattr

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/integration"
	"github.com/flanj-io/collector/internal/model"
)

// Frozen attribute keys (contracts §2).
const (
	AttrCaptureVersion = "flanj.capture.version"
	AttrRecordType     = "flanj.record.type"
	AttrDirection      = "flanj.direction"
	AttrPeerHost       = "flanj.peer.host"
	// AttrPeerAddr is the optional socket address (IP) of the peer — transport
	// detail alongside the peer.host identity; omitted when unknown.
	AttrPeerAddr      = "flanj.peer.addr"
	AttrEdgeClass     = "flanj.edge.class"
	AttrCaptureBodies = "flanj.capture.bodies"
	// AttrIntegration is deprecated and IGNORED: no decoder reads it, because
	// the collector derives the key itself (integration.Derive). Kept so tests
	// can send what an older SDK still does and prove it changes nothing.
	AttrIntegration    = "flanj.integration"
	AttrMethod         = "flanj.http.method"
	AttrRoute          = "flanj.http.route"
	AttrTarget         = "flanj.http.target"
	AttrURLFull        = "flanj.http.url.full"
	AttrStatusCode     = "flanj.http.status_code"
	AttrReqContentType = "flanj.http.request.content_type"
	AttrReqBody        = "flanj.http.request.body"
	AttrReqBodyTrunc   = "flanj.http.request.body.truncated"
	AttrReqHeaders     = "flanj.http.request.headers"
	AttrRespContent    = "flanj.http.response.content_type"
	AttrRespBody       = "flanj.http.response.body"
	AttrRespBodyTrunc  = "flanj.http.response.body.truncated"
	AttrRespHeaders    = "flanj.http.response.headers"
	AttrCorrRequestID  = "flanj.corr.request_id"
	AttrCorrIdemKey    = "flanj.corr.idempotency_key"
	AttrCorrTraceID    = "flanj.corr.trace_id"
	AttrCorrSpanID     = "flanj.corr.span_id"
	AttrDurationMS     = "flanj.http.duration_ms"
	AttrRedactApplied  = "flanj.redaction.applied"
	AttrRedactPatterns = "flanj.redaction.patterns"
	AttrRedactSpecAwr  = "flanj.redaction.spec_aware"
	// AttrRedactFields is the optional JSON array of whole-value body redactions
	// with the original value's captured properties (CONTRACTS §2, entries
	// {part,path,pattern,props}; sorted by part then path; omitted when empty).
	AttrRedactFields = "flanj.redaction.fields"

	// v0.5 MCP attributes (CONTRACTS §2 "MCP tool-call records" +
	// "`contract_snapshot` records", Step B — parsed here since Step C).
	// AttrTransport is "mcp" on MCP records; absent = HTTP.
	AttrTransport = "flanj.transport"
	// AttrMCPToolName is the called tool — the operation id detection matches
	// against the contract (Operation.ID / Match.ToolName).
	AttrMCPToolName = "flanj.mcp.tool.name"
	// AttrMCPIsError is the CallToolResult's isError (also true when the call
	// itself rejected). Feeds the error-rate metric; never a finding on its own.
	AttrMCPIsError = "flanj.mcp.is_error"
	// AttrMCPErrorCode (additive, optional — 2026-09-17) is the JSON-RPC error
	// code when the tools/call REQUEST itself was rejected (as opposed to a
	// result with isError). -32602 (invalid params) on arguments that
	// previously succeeded is the observed_failure row.
	AttrMCPErrorCode = "flanj.mcp.error.code"
	// AttrMCPViaDispatch (additive, optional — 2026-09-17) is stamped by
	// the drift processor, never by an SDK: the dispatcher tool a call went
	// through when it was re-attributed to the inner tool it named.
	AttrMCPViaDispatch = "flanj.mcp.via_dispatch"
	// AttrMCPServerName / AttrMCPServerVersion / AttrMCPProtocolVersion carry
	// the server identity from initialize, when the client surfaces it.
	AttrMCPServerName      = "flanj.mcp.server.name"
	AttrMCPServerVersion   = "flanj.mcp.server.version"
	AttrMCPProtocolVersion = "flanj.mcp.protocol.version"
	// AttrMCPServerCommand (optional, additive 2026-09-18) is how the client
	// LAUNCHED a stdio server (`flanj.edge.class` = local-process): a compact
	// JSON array `[command, ...args]`, each element floor-redacted by the SDK,
	// capped at 1024 bytes (last element exactly "…" when args were dropped).
	// It exists so a reader can see which PACKAGE is behind a self-reported
	// serverInfo.name. Opaque display text for the contract card: validated as
	// a JSON array of strings and nothing else, never parsed for meaning, and
	// never part of a flag payload. Rides contract_snapshot records only.
	AttrMCPServerCommand = "flanj.mcp.server.command"
	// AttrMCPSessionID is the Mcp-Session-Id when the transport exposes one.
	// Protocol-level sessions were removed in revision 2026-07-28, so this is
	// permanently absent against a current server; the slot stays for clients
	// still on the 2025-11-25 line.
	AttrMCPSessionID = "flanj.mcp.session.id"
	// AttrMCPResultType is the result's `resultType` (revision 2026-07-28) —
	// "complete", "input_required", or whatever a later revision adds, verbatim.
	// ABSENT means an older server, NEVER "complete": an interactive tool's
	// input_required result carries a payload that is partial by design, and
	// judging it against a contract manufactures findings out of normal traffic.
	AttrMCPResultType = "flanj.mcp.result.type"
	// AttrMCPTaskID is set when the result was a Tasks HANDLE rather than a
	// payload: the tool's real output arrives later via tasks/get, on a surface
	// the SDK does not yet instrument. The record describes the envelope, so
	// nothing may validate response shape from it.
	AttrMCPTaskID = "flanj.mcp.task.id"
	// AttrMCPCatalogTTLMs / AttrMCPCatalogCacheScope are the `ttlMs` /
	// `cacheScope` a tools/list result published (revision 2026-07-28). Clients
	// are now told to CACHE catalogs, so a snapshot may legitimately be up to
	// ttlMs behind the server — see the stale_client note in internal/drift/mcp.go.
	AttrMCPCatalogTTLMs      = "flanj.mcp.catalog.ttl_ms"
	AttrMCPCatalogCacheScope = "flanj.mcp.catalog.cache_scope"
	// AttrCorrClientRequestID is the JSON-RPC id observed on the client's OWN
	// outgoing message — CLIENT-generated, labeled as such, never merged into
	// AttrCorrRequestID (which stays provider-issued only).
	AttrCorrClientRequestID = "flanj.corr.client_request_id"
	// AttrMCPContractSnapshot is the floor-redacted JSON of one COMPLETE
	// observed tools/list ({"tools":[…], "serverInfo"?, "protocolVersion"?,
	// "capabilities"?}; tools decodable by contract.ParseToolsList).
	AttrMCPContractSnapshot = "flanj.mcp.contract_snapshot"
	// AttrMCPToolCount is the number of tools in the snapshot.
	AttrMCPToolCount = "flanj.mcp.tool.count"

	// Internal-only: the whole finding JSON carried on a finding log record.
	AttrFindingJSON = "flanj.finding.json"

	// Internal-only: the contract metadata (model.SpecInfo) JSON carried on a
	// spec_info log record; the raw spec document travels in the record Body
	// as bytes. Emitted by the drift processor so a store pod behind an
	// otlphttp hop learns which contracts the front collectors loaded.
	AttrSpecInfoJSON = "flanj.spec_info.json"

	// Internal-only: the canonical store id for a call, stamped once by the drift
	// processor so the finding's source_call_id and the exporter's stored call
	// share the same id (they each reconstruct the call independently).
	AttrCallID = "flanj.call.id"

	// Internal-only: the drift processor's per-call VERDICT, stamped on the call
	// record where validation runs (CONTRACTS §2, "Collector-internal
	// attributes on call records"). AttrValidated is model.ValidatedClean /
	// ValidatedDrifted / ValidatedNot; AttrValidatedReason names the gate that
	// stopped it when it is ValidatedNot. In the tiered topology it crosses the
	// front→store hop with the record, so the store pod learns what the front
	// did with the call instead of inferring it from the contract list.
	//
	// ABSENT on a record no drift processor saw — an older front, a pipeline
	// without flanjdrift. CallFromRecord decodes absence as model.ValidatedUnknown
	// so the store can tell "never judged" from a row that predates the stamp,
	// and a reader never mistakes either for clean.
	AttrValidated       = "flanj.validated"
	AttrValidatedReason = "flanj.validated.reason"

	RecordTypeCall     = "call"
	RecordTypeFinding  = "finding"
	RecordTypeSpecInfo = "spec_info"
	// RecordTypeContractSnapshot is one complete observed MCP tools/list —
	// the self-delivering local spec (v0.5; emitted by the SDK, loaded by the
	// drift processor, never stored as a call).
	RecordTypeContractSnapshot = "contract_snapshot"

	// TransportMCP is AttrTransport's value on MCP records.
	TransportMCP = "mcp"
)

// bodyAttrs are the redactable string attributes the defense-in-depth pass
// re-scans on a call record. The route is stored and shown beside the target, and
// the collector accepts OTLP from any sender, so it is floored like the target.
var bodyAttrs = []string{AttrReqBody, AttrRespBody, AttrReqHeaders, AttrRespHeaders, AttrTarget, AttrURLFull, AttrRoute}

// BodyAttrs returns the attribute keys carrying redactable free text.
func BodyAttrs() []string { return bodyAttrs }

// RecordType reads flanj.record.type (defaults to "call" when absent).
func RecordType(lr plog.LogRecord) string {
	if v, ok := lr.Attributes().Get(AttrRecordType); ok {
		return v.Str()
	}
	return RecordTypeCall
}

// Transport reads flanj.transport ("" = HTTP, TransportMCP = MCP).
func Transport(lr plog.LogRecord) string {
	if v, ok := lr.Attributes().Get(AttrTransport); ok {
		return v.Str()
	}
	return ""
}

// recordTime derives the record's capture time (Timestamp, falling back to
// ObservedTimestamp) as the store's ISO-8601 UTC format.
func recordTime(lr plog.LogRecord) string {
	ts := lr.Timestamp()
	if ts == 0 {
		ts = lr.ObservedTimestamp()
	}
	return time.Unix(0, int64(ts)).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func getStr(m pcommon.Map, k string) string {
	if v, ok := m.Get(k); ok {
		return v.Str()
	}
	return ""
}

func getInt(m pcommon.Map, k string) int {
	if v, ok := m.Get(k); ok {
		return int(v.Int())
	}
	return 0
}

func getBool(m pcommon.Map, k string) bool {
	if v, ok := m.Get(k); ok {
		return v.Bool()
	}
	return false
}

func parseHeaders(s string) map[string]string {
	if s == "" {
		return nil
	}
	out := map[string]string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ResourceServiceName is the OTel semantic-convention RESOURCE attribute naming
// the service that emitted a batch — the caller, for a call record. Not a
// flanj.* key: both SDKs set it on the OTLP resource from their service-name
// option, else OTEL_SERVICE_NAME, else their own default (CONTRACTS §2).
const ResourceServiceName = "service.name"

// ServiceNameOf reads the caller's service.name off a record's RESOURCE (one
// per resource group, shared by every record under it), or "" when unset.
func ServiceNameOf(res pcommon.Resource) string {
	if v, ok := res.Attributes().Get(ResourceServiceName); ok {
		return v.AsString()
	}
	return ""
}

// CallFromRecord reconstructs a RedactedCall from a "call" log record and the
// resource it arrived under. The id is a fresh uuidv7 and captured_at derives
// from the record timestamp. service_name comes off the resource, and the
// integration is DERIVED (integration.Derive — outbound HTTP and MCP by the
// peer host, inbound HTTP by that service name); an SDK-sent flanj.integration
// is ignored (CONTRACTS §2). Every pod decodes through here, so a front and the
// store pod behind it key one record the same way.
func CallFromRecord(res pcommon.Resource, lr plog.LogRecord) model.RedactedCall {
	m := lr.Attributes()
	var patterns []string
	if v, ok := m.Get(AttrRedactPatterns); ok {
		_ = json.Unmarshal([]byte(v.Str()), &patterns)
	}
	// flanj.redaction.fields is optional: absent = empty (older SDKs in the
	// compatibility window emit none; drift then keeps token-skipping).
	var fields []model.RedactedFieldRecord
	if v, ok := m.Get(AttrRedactFields); ok {
		_ = json.Unmarshal([]byte(v.Str()), &fields)
	}
	capturedAt := recordTime(lr)
	id := getStr(m, AttrCallID)
	if id == "" {
		id = NewID()
	}
	// Converge older SDKs onto one edge key. An SDK before the host[:port] fix
	// emits `api.acme.test:443` for an options-object dial and `api.acme.test`
	// for the same origin dialled as a URL string; keying both is two edges for
	// one listener, and a contract bound to either never validates the other.
	peerHost := edge.StripDefaultPort(getStr(m, AttrPeerHost), schemeOf(getStr(m, AttrURLFull)))
	edgeClass := getStr(m, AttrEdgeClass)
	// Defense-in-depth: if the SDK omitted the class, reconstruct it from the
	// peer host with the identical heuristic so classification is never lost.
	if edgeClass == "" && peerHost != "" {
		edgeClass = edge.Classify(peerHost)
	}
	// The verdict is ABSENT on a record no drift processor saw. That is a fact
	// worth keeping distinct from "" — a row stored before verdicts existed — so
	// absence decodes to the explicit unknown, and never, on any path, to clean.
	validated := getStr(m, AttrValidated)
	if validated == "" {
		validated = model.ValidatedUnknown
	}
	direction := getStr(m, AttrDirection)
	transport := getStr(m, AttrTransport)
	serviceName := ServiceNameOf(res)
	return model.RedactedCall{
		SchemaVersion:         model.SchemaVersion,
		ID:                    id,
		CapturedAt:            capturedAt,
		Integration:           integration.Derive(transport == TransportMCP, direction, peerHost, serviceName),
		ServiceName:           serviceName,
		Direction:             direction,
		PeerHost:              peerHost,
		PeerAddr:              getStr(m, AttrPeerAddr),
		EdgeClass:             edgeClass,
		Method:                getStr(m, AttrMethod),
		URL:                   getStr(m, AttrURLFull),
		Route:                 getStr(m, AttrRoute),
		StatusCode:            getInt(m, AttrStatusCode),
		RequestHeaders:        parseHeaders(getStr(m, AttrReqHeaders)),
		RequestBody:           getStr(m, AttrReqBody),
		RequestBodyTruncated:  getBool(m, AttrReqBodyTrunc),
		RequestContentType:    getStr(m, AttrReqContentType),
		ResponseHeaders:       parseHeaders(getStr(m, AttrRespHeaders)),
		ResponseBody:          getStr(m, AttrRespBody),
		ResponseBodyTruncated: getBool(m, AttrRespBodyTrunc),
		ResponseContentType:   getStr(m, AttrRespContent),
		Correlation: model.Correlation{
			RequestID:      getStr(m, AttrCorrRequestID),
			IdempotencyKey: getStr(m, AttrCorrIdemKey),
			TraceID:        getStr(m, AttrCorrTraceID),
			SpanID:         getStr(m, AttrCorrSpanID),
			// Client-generated (MCP): kept in its own slot, never folded into
			// the provider-issued RequestID.
			ClientRequestID: getStr(m, AttrCorrClientRequestID),
		},
		DurationMS: getInt(m, AttrDurationMS),
		Redaction: model.Redaction{
			Applied:   getBool(m, AttrRedactApplied),
			Patterns:  patterns,
			SpecAware: getBool(m, AttrRedactSpecAwr),
			Fields:    fields,
		},
		Transport:          transport,
		MCPToolName:        getStr(m, AttrMCPToolName),
		MCPIsError:         getBool(m, AttrMCPIsError),
		MCPErrorCode:       getInt(m, AttrMCPErrorCode),
		ViaDispatch:        getStr(m, AttrMCPViaDispatch),
		MCPServerName:      getStr(m, AttrMCPServerName),
		MCPServerVersion:   getStr(m, AttrMCPServerVersion),
		MCPProtocolVersion: getStr(m, AttrMCPProtocolVersion),
		MCPSessionID:       getStr(m, AttrMCPSessionID),
		MCPResultType:      getStr(m, AttrMCPResultType),
		MCPTaskID:          getStr(m, AttrMCPTaskID),
		Validated:          validated,
		ValidatedReason:    getStr(m, AttrValidatedReason),
	}
}

// StampValidated writes the drift processor's verdict onto a call record
// (AttrValidated + AttrValidatedReason). The processor calls it exactly once per
// call, on EVERY branch of its per-call path — the branches that validate
// nothing included, which is the whole point: a call the processor could not
// judge says so on the wire, instead of leaving the reader to infer it from the
// edge. A reason from an earlier stamp is removed rather than left beside a
// verdict it no longer describes.
func StampValidated(lr plog.LogRecord, v model.Validation) {
	m := lr.Attributes()
	m.PutStr(AttrValidated, v.Verdict)
	if v.Verdict == model.ValidatedNot && v.Reason != "" {
		m.PutStr(AttrValidatedReason, v.Reason)
	} else {
		m.Remove(AttrValidatedReason)
	}
}

// StampDispatchTarget re-keys a dispatcher call to the inner tool it named:
// the tool name and the route now name the inner
// tool, and the dispatcher is kept as via_dispatch. The request body is left
// untouched — it is the literal dispatcher call, which is what a provider
// needs to reproduce it.
func StampDispatchTarget(lr plog.LogRecord, inner, dispatcher string) {
	m := lr.Attributes()
	m.PutStr(AttrMCPToolName, inner)
	if r, ok := m.Get(AttrRoute); ok && r.Str() == "/"+dispatcher {
		m.PutStr(AttrRoute, "/"+inner)
	}
	m.PutStr(AttrMCPViaDispatch, dispatcher)
}

// ContractSnapshot is one decoded contract_snapshot record: a COMPLETE observed
// MCP tools/list plus the edge + server identity it was observed on (CONTRACTS
// §2, v0.5). SnapshotJSON is the floor-redacted snapshot document verbatim —
// the server's own words; contract.ParseToolsList decodes its tools.
type ContractSnapshot struct {
	Integration     string
	Direction       string
	PeerHost        string
	EdgeClass       string
	ServerName      string
	ServerVersion   string
	ProtocolVersion string
	ToolCount       int
	SnapshotJSON    string
	// CatalogTTLMs / CatalogCacheScope are the tools/list cache directives
	// (revision 2026-07-28), when the server published them. They also ride
	// INSIDE SnapshotJSON, so the stored document stays self-describing.
	CatalogTTLMs      int
	CatalogCacheScope string
	// ServerCommand is flanj.mcp.server.command verbatim when it is a
	// non-empty JSON array of strings, and "" otherwise — absent, or a value
	// that is not that shape (dropped, never a reason to fail the record).
	ServerCommand string
	// ObservedAt is the record's own timestamp (the spec's "<ts>" in
	// provenance "observed tools/list at <ts>").
	ObservedAt string
}

// DecodeServerCommand reports whether raw is a flanj.mcp.server.command the
// collector keeps: a JSON array of strings with at least one element (the
// command itself is always present on the wire). Anything else — not JSON, an
// object, a number in the array, null, [] — is not a command and is dropped.
// The SHAPE is the only thing checked: the value is display text, and nothing
// in the collector acts on what it says.
func DecodeServerCommand(raw string) ([]string, bool) {
	if raw == "" {
		return nil, false
	}
	// Into []any, not []string: encoding/json decodes a null ELEMENT into a
	// string as a silent no-op, so `["npx",null]` would pass as a command.
	var elems []any
	if err := json.Unmarshal([]byte(raw), &elems); err != nil || len(elems) == 0 {
		return nil, false
	}
	parts := make([]string, len(elems))
	for i, e := range elems {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		parts[i] = s
	}
	return parts, true
}

// serverCommandOf keeps a valid command verbatim and drops everything else.
func serverCommandOf(raw string) string {
	if _, ok := DecodeServerCommand(raw); !ok {
		return ""
	}
	return raw
}

// ContractSnapshotFromRecord reconstructs a ContractSnapshot from a
// "contract_snapshot" log record. A record without the snapshot payload is
// rejected (never mistaken for an empty list).
func ContractSnapshotFromRecord(lr plog.LogRecord) (ContractSnapshot, error) {
	m := lr.Attributes()
	raw := getStr(m, AttrMCPContractSnapshot)
	if raw == "" {
		return ContractSnapshot{}, errNoSnapshot
	}
	direction, peerHost := getStr(m, AttrDirection), getStr(m, AttrPeerHost)
	return ContractSnapshot{
		// Derived by the call rule, never read off the record: a server's
		// catalogue and its calls must share one key (CONTRACTS §2).
		Integration:       integration.Derive(true, direction, peerHost, ""),
		Direction:         direction,
		PeerHost:          peerHost,
		EdgeClass:         getStr(m, AttrEdgeClass),
		ServerName:        getStr(m, AttrMCPServerName),
		ServerVersion:     getStr(m, AttrMCPServerVersion),
		ProtocolVersion:   getStr(m, AttrMCPProtocolVersion),
		ToolCount:         getInt(m, AttrMCPToolCount),
		SnapshotJSON:      raw,
		CatalogTTLMs:      getInt(m, AttrMCPCatalogTTLMs),
		CatalogCacheScope: getStr(m, AttrMCPCatalogCacheScope),
		ServerCommand:     serverCommandOf(getStr(m, AttrMCPServerCommand)),
		ObservedAt:        recordTime(lr),
	}, nil
}

// EnsureCallID returns the record's stable call id, generating and stamping one
// (flanj.call.id) if absent. Call this once, upstream of both the drift
// detector and the store exporter, so the finding's source_call_id matches the
// stored call's id.
func EnsureCallID(lr plog.LogRecord) string {
	if v, ok := lr.Attributes().Get(AttrCallID); ok && v.Str() != "" {
		return v.Str()
	}
	id := NewID()
	lr.Attributes().PutStr(AttrCallID, id)
	return id
}

// FindingToRecord writes a Finding into a fresh log record as a single JSON
// attribute plus record.type=finding.
func FindingToRecord(lr plog.LogRecord, f model.Finding) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	lr.Attributes().PutStr(AttrRecordType, RecordTypeFinding)
	lr.Attributes().PutStr(AttrFindingJSON, string(b))
	return nil
}

// AttrFindingInbound marks a finding record whose finding was born from an
// INBOUND call: raised against the self spec and keyed by the service the call
// reached, a name that never leaves the collector (CONTRACTS §3). It is an
// attribute of the collector's own finding record — the processor→exporter
// path and the tiered front→store hop — and never part of the finding's JSON,
// so it cannot reach the control plane. The store persists it
// (store.Store.InsertInboundFinding) whether or not the call is ever stored.
const AttrFindingInbound = "flanj.finding.inbound"

// MarkFindingInbound stamps AttrFindingInbound on a finding record.
func MarkFindingInbound(lr plog.LogRecord) {
	lr.Attributes().PutBool(AttrFindingInbound, true)
}

// FindingInbound reports whether a finding record carries AttrFindingInbound.
// Absent (every record from an older front) is false; the store's own join on
// the source call's direction still applies then.
func FindingInbound(lr plog.LogRecord) bool {
	v, ok := lr.Attributes().Get(AttrFindingInbound)
	return ok && v.Type() == pcommon.ValueTypeBool && v.Bool()
}

// FindingFromRecord reconstructs a Finding from a "finding" log record.
func FindingFromRecord(lr plog.LogRecord) (model.Finding, error) {
	var f model.Finding
	v, ok := lr.Attributes().Get(AttrFindingJSON)
	if !ok {
		return f, errNoFinding
	}
	err := json.Unmarshal([]byte(v.Str()), &f)
	return f, err
}

// SpecInfoToRecord writes a loaded contract into a fresh log record as
// record.type=spec_info: the SpecInfo metadata as one JSON attribute and the
// raw spec document (may be empty) in the Body as bytes — OTLP's opaque
// payload slot, round-tripped losslessly by the otlphttp exporter.
func SpecInfoToRecord(lr plog.LogRecord, info model.SpecInfo, raw []byte) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	lr.Attributes().PutStr(AttrRecordType, RecordTypeSpecInfo)
	lr.Attributes().PutStr(AttrSpecInfoJSON, string(b))
	lr.Body().SetEmptyBytes().FromRaw(raw)
	return nil
}

// SpecInfoFromRecord reconstructs the SpecInfo + raw document from a
// "spec_info" log record.
func SpecInfoFromRecord(lr plog.LogRecord) (model.SpecInfo, []byte, error) {
	var info model.SpecInfo
	v, ok := lr.Attributes().Get(AttrSpecInfoJSON)
	if !ok {
		return info, nil, errNoSpecInfo
	}
	if err := json.Unmarshal([]byte(v.Str()), &info); err != nil {
		return info, nil, err
	}
	var raw []byte
	if lr.Body().Type() == pcommon.ValueTypeBytes {
		raw = lr.Body().Bytes().AsRaw()
	}
	return info, raw, nil
}

// schemeOf reads the lowercased scheme from an absolute URL, "" when there is
// none. `flanj.http.url.full` is the only place a call record carries the scheme
// its peer host was dialled on.
func schemeOf(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return ""
	}
	return strings.ToLower(rawURL[:i])
}

// NewID returns a fresh uuidv7 string (time-ordered — good for FIFO windows).
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

type sentinel string

func (s sentinel) Error() string { return string(s) }

const errNoFinding = sentinel("record carries no flanj.finding.json attribute")
const errNoSpecInfo = sentinel("record carries no flanj.spec_info.json attribute")
const errNoSnapshot = sentinel("record carries no flanj.mcp.contract_snapshot attribute")
