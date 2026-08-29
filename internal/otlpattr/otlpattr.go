// Package otlpattr maps between the frozen flanj.* OTLP log attributes
// (contracts §2) and the collector's in-process record types. Calls arrive from
// the SDK as attributes; findings are carried through the internal pipeline as a
// single JSON attribute so the store exporter can reconstruct them.
package otlpattr

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/edge"
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
	AttrPeerAddr       = "flanj.peer.addr"
	AttrEdgeClass      = "flanj.edge.class"
	AttrCaptureBodies  = "flanj.capture.bodies"
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
	// AttrMCPServerName / AttrMCPServerVersion / AttrMCPProtocolVersion carry
	// the server identity from initialize, when the client surfaces it.
	AttrMCPServerName      = "flanj.mcp.server.name"
	AttrMCPServerVersion   = "flanj.mcp.server.version"
	AttrMCPProtocolVersion = "flanj.mcp.protocol.version"
	// AttrMCPSessionID is the Mcp-Session-Id when the transport exposes one.
	AttrMCPSessionID = "flanj.mcp.session.id"
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
// re-scans on a call record.
var bodyAttrs = []string{AttrReqBody, AttrRespBody, AttrReqHeaders, AttrRespHeaders, AttrTarget, AttrURLFull}

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

// CallFromRecord reconstructs a RedactedCall from a "call" log record. The id is
// a fresh uuidv7 and captured_at derives from the record timestamp.
func CallFromRecord(lr plog.LogRecord) model.RedactedCall {
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
	peerHost := getStr(m, AttrPeerHost)
	edgeClass := getStr(m, AttrEdgeClass)
	// Defense-in-depth: if the SDK omitted the class, reconstruct it from the
	// peer host with the identical heuristic so classification is never lost.
	if edgeClass == "" && peerHost != "" {
		edgeClass = edge.Classify(peerHost)
	}
	return model.RedactedCall{
		SchemaVersion:         model.SchemaVersion,
		ID:                    id,
		CapturedAt:            capturedAt,
		Integration:           getStr(m, AttrIntegration),
		Direction:             getStr(m, AttrDirection),
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
		Transport:          getStr(m, AttrTransport),
		MCPToolName:        getStr(m, AttrMCPToolName),
		MCPIsError:         getBool(m, AttrMCPIsError),
		MCPServerName:      getStr(m, AttrMCPServerName),
		MCPServerVersion:   getStr(m, AttrMCPServerVersion),
		MCPProtocolVersion: getStr(m, AttrMCPProtocolVersion),
		MCPSessionID:       getStr(m, AttrMCPSessionID),
	}
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
	// ObservedAt is the record's own timestamp (the spec's "<ts>" in
	// provenance "observed tools/list at <ts>").
	ObservedAt string
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
	return ContractSnapshot{
		Integration:     getStr(m, AttrIntegration),
		Direction:       getStr(m, AttrDirection),
		PeerHost:        getStr(m, AttrPeerHost),
		EdgeClass:       getStr(m, AttrEdgeClass),
		ServerName:      getStr(m, AttrMCPServerName),
		ServerVersion:   getStr(m, AttrMCPServerVersion),
		ProtocolVersion: getStr(m, AttrMCPProtocolVersion),
		ToolCount:       getInt(m, AttrMCPToolCount),
		SnapshotJSON:    raw,
		ObservedAt:      recordTime(lr),
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
