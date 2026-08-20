// Package otlpattr maps between the frozen vinifera.* OTLP log attributes
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

	"github.com/vinifera-io/collector/internal/edge"
	"github.com/vinifera-io/collector/internal/model"
)

// Frozen attribute keys (contracts §2).
const (
	AttrCaptureVersion = "vinifera.capture.version"
	AttrRecordType     = "vinifera.record.type"
	AttrDirection      = "vinifera.direction"
	AttrPeerHost       = "vinifera.peer.host"
	AttrEdgeClass      = "vinifera.edge.class"
	AttrCaptureBodies  = "vinifera.capture.bodies"
	AttrIntegration    = "vinifera.integration"
	AttrMethod         = "vinifera.http.method"
	AttrRoute          = "vinifera.http.route"
	AttrTarget         = "vinifera.http.target"
	AttrURLFull        = "vinifera.http.url.full"
	AttrStatusCode     = "vinifera.http.status_code"
	AttrReqContentType = "vinifera.http.request.content_type"
	AttrReqBody        = "vinifera.http.request.body"
	AttrReqBodyTrunc   = "vinifera.http.request.body.truncated"
	AttrReqHeaders     = "vinifera.http.request.headers"
	AttrRespContent    = "vinifera.http.response.content_type"
	AttrRespBody       = "vinifera.http.response.body"
	AttrRespBodyTrunc  = "vinifera.http.response.body.truncated"
	AttrRespHeaders    = "vinifera.http.response.headers"
	AttrCorrRequestID  = "vinifera.corr.request_id"
	AttrCorrIdemKey    = "vinifera.corr.idempotency_key"
	AttrCorrTraceID    = "vinifera.corr.trace_id"
	AttrCorrSpanID     = "vinifera.corr.span_id"
	AttrDurationMS     = "vinifera.http.duration_ms"
	AttrRedactApplied  = "vinifera.redaction.applied"
	AttrRedactPatterns = "vinifera.redaction.patterns"
	AttrRedactSpecAwr  = "vinifera.redaction.spec_aware"
	// AttrRedactFields is the optional JSON array of whole-value body redactions
	// with the original value's captured properties (CONTRACTS §2, entries
	// {part,path,pattern,props}; sorted by part then path; omitted when empty).
	AttrRedactFields = "vinifera.redaction.fields"

	// Internal-only: the whole finding JSON carried on a finding log record.
	AttrFindingJSON = "vinifera.finding.json"

	// Internal-only: the canonical store id for a call, stamped once by the drift
	// processor so the finding's source_call_id and the exporter's stored call
	// share the same id (they each reconstruct the call independently).
	AttrCallID = "vinifera.call.id"

	RecordTypeCall    = "call"
	RecordTypeFinding = "finding"
)

// bodyAttrs are the redactable string attributes the defense-in-depth pass
// re-scans on a call record.
var bodyAttrs = []string{AttrReqBody, AttrRespBody, AttrReqHeaders, AttrRespHeaders, AttrTarget, AttrURLFull}

// BodyAttrs returns the attribute keys carrying redactable free text.
func BodyAttrs() []string { return bodyAttrs }

// RecordType reads vinifera.record.type (defaults to "call" when absent).
func RecordType(lr plog.LogRecord) string {
	if v, ok := lr.Attributes().Get(AttrRecordType); ok {
		return v.Str()
	}
	return RecordTypeCall
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
	// vinifera.redaction.fields is optional: absent = empty (older SDKs in the
	// compatibility window emit none; drift then keeps token-skipping).
	var fields []model.RedactedFieldRecord
	if v, ok := m.Get(AttrRedactFields); ok {
		_ = json.Unmarshal([]byte(v.Str()), &fields)
	}
	ts := lr.Timestamp()
	if ts == 0 {
		ts = lr.ObservedTimestamp()
	}
	capturedAt := time.Unix(0, int64(ts)).UTC().Format("2006-01-02T15:04:05.000Z07:00")
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
		},
		DurationMS: getInt(m, AttrDurationMS),
		Redaction: model.Redaction{
			Applied:   getBool(m, AttrRedactApplied),
			Patterns:  patterns,
			SpecAware: getBool(m, AttrRedactSpecAwr),
			Fields:    fields,
		},
	}
}

// EnsureCallID returns the record's stable call id, generating and stamping one
// (vinifera.call.id) if absent. Call this once, upstream of both the drift
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

const errNoFinding = sentinel("record carries no vinifera.finding.json attribute")
