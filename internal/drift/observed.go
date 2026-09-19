package drift

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flanj-io/collector/contract/diff"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// Observed-traffic detectors: R-B's two collector-only rows (Idan,
// 2026-09-17). Both learn from calls the agent already made; neither probes.
//
//   - input_rejection (observed_failure / BREAKING): a call rejected with
//     JSON-RPC -32602 whose ARGUMENT SHAPE previously succeeded on the same
//     tool. A never-seen shape rejected is the caller's own problem and is not
//     reported.
//   - value_change (value / WARNING): a response field whose value FORMAT
//     held steady and then changed and held again — a timestamp from ISO to
//     epoch, an ID from UUID to prefixed, an enum from UPPER to lower, an
//     amount from integer to decimal. The schema can stay identical while the
//     meaning moves; this is the drift a declaration diff cannot see.
//
// Both are deliberately conservative, because a false red is still a false
// claim: value_change needs a format held for valueStable responses, then a
// different one for valueConfirm responses in a row, and a field whose format
// never settles (or is free text) never fires. State is in memory, per edge
// and tool, bounded, and re-learned after a restart.

// CodeInvalidParams is JSON-RPC's invalid-params error.
const CodeInvalidParams = -32602

// Rules for the two kinds.
const (
	RuleArgumentsRejected  = "arguments-previously-accepted-rejected"
	RuleValueFormatChanged = "value-format-changed"
)

const (
	valueStable   = 5   // responses in one format before it counts as the field's format
	valueConfirm  = 3   // consecutive responses in a new format before it is a change
	maxShapes     = 64  // accepted argument shapes remembered per tool
	maxValuePaths = 200 // response paths fingerprinted per tool
)

// observedState is one (edge, tool)'s learned traffic facts.
type observedState struct {
	accepted map[string]bool        // argument shapes that returned a result
	fields   map[string]*fieldTrack // response path -> format history
}

type fieldTrack struct {
	stable      string // the established format ("" until established)
	cur         string // the format of the current run
	run         int    // length of the current run
	newlyStable bool
}

func (d *MCPDetector) observed(edge, tool string) *observedState {
	if d.traffic == nil {
		d.traffic = map[string]*observedState{}
	}
	k := edge + "|" + tool
	s := d.traffic[k]
	if s == nil {
		s = &observedState{accepted: map[string]bool{}, fields: map[string]*fieldTrack{}}
		d.traffic[k] = s
	}
	return s
}

// observeTraffic runs both detectors for one call, attributed to `tool` with
// `args` as its arguments (the inner tool and inner arguments for a
// re-attributed dispatcher call).
func (d *MCPDetector) observeTraffic(call model.RedactedCall, tool, args, viaDispatch string) []model.Finding {
	if tool == "" || call.RequestBodyTruncated {
		return nil
	}
	shape := argShape(args)
	edge := mcpEdgeRef(call.PeerHost, call.Direction)
	var out []model.Finding

	d.mu.Lock()
	st := d.observed(edge, tool)
	switch {
	case call.MCPErrorCode == CodeInvalidParams:
		if shape != "" && st.accepted[shape] {
			out = append(out, rejectionFinding(call, tool, shape, viaDispatch))
		}
	case call.MCPErrorCode == 0 && !call.MCPIsError && call.MCPResultType != model.MCPResultTypeInputRequired:
		if shape != "" && len(st.accepted) < maxShapes {
			st.accepted[shape] = true
		}
		if !call.ResponseBodyTruncated && isJSONContentType(call.ResponseContentType) && call.MCPTaskID == "" {
			for _, ch := range st.fingerprint(call.ResponseBody) {
				out = append(out, valueFinding(call, tool, ch, viaDispatch))
			}
		}
	}
	d.mu.Unlock()
	return out
}

// argShape is the arguments' top-level keys and JSON types, sorted:
// "account_id:string,limit:number". Values never enter it.
func argShape(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+":"+jsonType(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

// formatChange is one field whose established format moved.
type formatChange struct {
	path, from, to string
}

// fingerprint folds one response into the field tracks and returns the fields
// whose format changed and held.
func (st *observedState) fingerprint(body string) []formatChange {
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		return nil
	}
	seen := map[string]string{}
	var walk func(path string, x any)
	walk = func(path string, x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, c := range t {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(p, c)
			}
		case []any:
			for _, c := range t {
				walk(path+".items", c)
			}
		default:
			if f := valueFormat(t); f != "" {
				if prev, ok := seen[path]; ok && prev != f {
					seen[path] = "mixed" // one response, two formats: not a field with a format
				} else if !ok {
					seen[path] = f
				}
			}
		}
	}
	walk("", v)
	var out []formatChange
	for path, f := range seen {
		tr := st.fields[path]
		if tr == nil {
			if len(st.fields) >= maxValuePaths {
				continue
			}
			tr = &fieldTrack{}
			st.fields[path] = tr
		}
		if f == "mixed" {
			tr.cur, tr.run = "", 0
			continue
		}
		if f == tr.cur {
			tr.run++
		} else {
			tr.cur, tr.run = f, 1
		}
		switch {
		case tr.stable == "" && tr.run >= valueStable:
			tr.stable = f
		case tr.stable != "" && f != tr.stable && tr.run >= valueConfirm && sameFamily(tr.stable, f):
			out = append(out, formatChange{path: path, from: tr.stable, to: f})
			tr.stable = f
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

var (
	reUUID       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	rePrefixedID = regexp.MustCompile(`^[a-z]{2,12}_[A-Za-z0-9]{6,}$`)
	reNumericStr = regexp.MustCompile(`^[0-9]{4,}$`)
	reISODate    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reToken      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	reUpper      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	reLower      = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// valueFormat classifies one scalar. "" means "no format worth tracking" (free
// text, booleans, nulls) — such a value never establishes or breaks a format.
func valueFormat(x any) string {
	switch t := x.(type) {
	case float64:
		switch {
		case t >= 1e9 && t < 1e10 && t == math.Trunc(t):
			return "timestamp:epoch-seconds"
		case t >= 1e12 && t < 1e13 && t == math.Trunc(t):
			return "timestamp:epoch-milliseconds"
		case t == math.Trunc(t):
			return "number:integer"
		default:
			return "number:decimal"
		}
	case string:
		switch {
		case isRFC3339(t):
			return "timestamp:iso-8601"
		case reISODate.MatchString(t):
			return "timestamp:date"
		case reUUID.MatchString(t):
			return "id:uuid"
		case rePrefixedID.MatchString(t):
			return "id:prefixed"
		case reNumericStr.MatchString(t):
			return "id:numeric"
		case reToken.MatchString(t) && strings.Contains(t, "_") || reToken.MatchString(t) && len(t) <= 20:
			switch {
			case reUpper.MatchString(t):
				return "enum:UPPER_CASE"
			case reLower.MatchString(t):
				return "enum:lower_case"
			}
		}
	}
	return ""
}

func isRFC3339(s string) bool {
	if len(s) < 20 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// sameFamily: a change is only a change of MEANING within one family —
// timestamp to timestamp, id to id, enum casing, number representation.
// Crossing families (an id field that now holds a timestamp) is a type-level
// story the schema, when declared, already tells.
func sameFamily(a, b string) bool {
	fa, _, _ := strings.Cut(a, ":")
	fb, _, _ := strings.Cut(b, ":")
	if fa == fb {
		return true
	}
	// An epoch number and an ISO string are the same meaning in two spellings.
	return fa == "timestamp" && fb == "timestamp"
}

func rejectionFinding(call model.RedactedCall, tool, shape, via string) model.Finding {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	id := call.ID
	f := model.Finding{
		SchemaVersion: model.SchemaVersion,
		ID:            otlpattr.NewID(),
		Kind:          model.KindInputRejection,
		ChangeKind:    string(diff.KindObservedFailure),
		Severity:      model.SeverityBreaking,
		ViaDispatch:   via,
		Integration:   call.Integration,
		Endpoint:      tool,
		FieldPath:     model.Ptr(""),
		Location:      model.Ptr("$.request.arguments"),
		Expected:      "arguments of shape {" + shape + "} accepted, as on earlier calls",
		Actual:        fmt.Sprintf("JSON-RPC %d (invalid params)", CodeInvalidParams),
		Rule:          RuleArgumentsRejected,
		SourceCallID:  &id,
		DetectedAt:    now,
		Detail: fmt.Sprintf("`%s` rejected arguments of a shape it accepted before ({%s}) with JSON-RPC %d — the tool's input contract changed without the caller changing anything.",
			tool, shape, CodeInvalidParams),
		OccurrenceCount: 1, FirstSeen: now, LastSeen: now,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func valueFinding(call model.RedactedCall, tool string, ch formatChange, via string) model.Finding {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	id := call.ID
	f := model.Finding{
		SchemaVersion: model.SchemaVersion,
		ID:            otlpattr.NewID(),
		Kind:          model.KindValueChange,
		ChangeKind:    string(diff.KindValue),
		Severity:      model.SeverityWarning,
		ViaDispatch:   via,
		Integration:   call.Integration,
		Endpoint:      tool,
		FieldPath:     model.Ptr(ch.path),
		Location:      model.Ptr("$.response.structuredContent." + ch.path),
		Expected:      ch.from,
		Actual:        ch.to,
		Rule:          RuleValueFormatChanged,
		SourceCallID:  &id,
		DetectedAt:    now,
		Detail: fmt.Sprintf("Tool `%s` field `%s` held %s for %d responses and now holds %s in %d in a row — its values changed meaning.",
			tool, ch.path, ch.from, valueStable, ch.to, valueConfirm),
		OccurrenceCount: 1, FirstSeen: now, LastSeen: now,
	}
	f.Signature = f.ComputeSignature()
	return f
}
