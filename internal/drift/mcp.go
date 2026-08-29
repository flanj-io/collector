// mcp.go — v0.5 Step C: the MCP side of the drift detector.
//
// An MCP server's contract is SELF-DELIVERING: the SDK observes the client's
// own tools/list and emits one contract_snapshot record per complete list —
// nothing is loaded manually. This file turns those snapshots into a versioned
// per-edge contract (contract.FromToolsList) and produces the three v0.5
// findings with the spec §1 flaggability table:
//
//   - output_mismatch  (FLAGGABLE): a tools/call structuredContent violates the
//     tool's declared outputSchema. A tool WITHOUT outputSchema produces NO
//     output_mismatch — the honest "no output contract declared" limit.
//   - definition_change (FLAGGABLE at every class — BREAKING, NON_BREAKING and,
//     since qfix2-2026-08-26, DESCRIPTION): two consecutive snapshots differ;
//     one finding per (edge, operation, rule, fieldPath) via the contract/diff
//     classifier. The evidence is the provider's own published text, before and
//     after, so it passes the evidence rule; flagging is always a human act.
//   - stale_client (LOCAL ONLY, never flaggable): the consumer's agent called a
//     tool absent from the CURRENT tools/list, or with arguments violating the
//     CURRENT inputSchema. Consumer-side — it fails the evidence rule.
//
// Validation uses the SAME JSON Schema validator as the HTTP path (kin-openapi
// — spec §5 "do not add a second") and the SAME token-aware + captured-props
// rules: a schema error whose offending scalar is a ⟦REDACTED:…⟧ token is
// skipped (redacted = unknown), unless the call carries a matching
// redaction.fields record whose captured props DECIDE the constraint.
// Findings ride the exact per-signature dedup machinery of HTTP drift
// (signature, occurrence_count, representative source call).
package drift

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/flanj-io/collector/contract"
	"github.com/flanj-io/collector/contract/diff"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// MCPDetector holds the per-edge MCP contract state: the CURRENT snapshot
// (what calls are validated against) and the PREVIOUS one (what the current
// was diffed from). Contracts are immutable once built; the mutex guards only
// the per-edge pointers, so detection never blocks on a snapshot load.
type MCPDetector struct {
	mu    sync.Mutex
	edges map[string]*mcpEdgeState
}

// mcpEdgeState is one MCP edge's snapshot pair. Versioning is by content hash
// (contract.Version.ContentHash); ObservedAt rides contract.Version.
type mcpEdgeState struct {
	integration string
	current     *contract.Contract
	previous    *contract.Contract
}

// NewMCPDetector returns an empty detector. It needs no configuration: MCP
// contracts arrive with the traffic (contract_snapshot records).
func NewMCPDetector() *MCPDetector {
	return &MCPDetector{edges: map[string]*mcpEdgeState{}}
}

// mcpEdgeRef is the existing edge identity (peer_host, direction) as one key.
func mcpEdgeRef(peerHost, direction string) string {
	if direction == "" {
		direction = "client"
	}
	return peerHost + "|" + direction
}

// LoadSnapshot ingests one contract_snapshot record: decode tools →
// contract.FromToolsList → version by content hash. The FIRST snapshot for an
// edge (and any snapshot with an unchanged hash) yields no findings; a CHANGED
// snapshot rotates current→previous and classifies the diff into
// definition_change findings. The returned SpecInfo + raw snapshot document
// are for the store's spec_infos (the Contracts tab) — an idempotent upsert
// keyed by integration.
func (d *MCPDetector) LoadSnapshot(snap otlpattr.ContractSnapshot) ([]model.Finding, model.SpecInfo, []byte, error) {
	tools, err := contract.ParseToolsList([]byte(snap.SnapshotJSON))
	if err != nil {
		return nil, model.SpecInfo{}, nil, fmt.Errorf("mcp snapshot: %w", err)
	}
	edgeRef := mcpEdgeRef(snap.PeerHost, snap.Direction)
	c, err := contract.FromToolsList(tools, edgeRef, snap.ObservedAt, "observed tools/list at "+snap.ObservedAt)
	if err != nil {
		return nil, model.SpecInfo{}, nil, fmt.Errorf("mcp snapshot: %w", err)
	}

	info := model.SpecInfo{
		Integration: snap.Integration,
		Role:        model.SpecRoleProvider,
		PeerHost:    snap.PeerHost,
		Format:      model.SpecFormatMCP,
		Title:       snap.ServerName,
		Version:     snap.ServerVersion,
		Endpoints:   len(tools),
		LoadedAt:    snap.ObservedAt,
	}

	d.mu.Lock()
	st := d.edges[edgeRef]
	if st == nil {
		st = &mcpEdgeState{}
		d.edges[edgeRef] = st
	}
	st.integration = snap.Integration
	var prev *contract.Contract
	switch {
	case st.current == nil:
		// First snapshot for this edge: it becomes the baseline; nothing to diff.
		st.current = c
	case st.current.Version.ContentHash == c.Version.ContentHash:
		// Unchanged surface: keep the versions as they are (re-observations of
		// the same list must not produce findings or rotate the baseline).
	default:
		st.previous = st.current
		st.current = c
		prev = st.previous
	}
	cur := st.current
	d.mu.Unlock()

	var findings []model.Finding
	if prev != nil {
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
		for _, ch := range diff.Classify(prev, cur) {
			findings = append(findings, definitionChangeFinding(snap.Integration, ch, prev, cur, now))
		}
	}
	return findings, info, []byte(snap.SnapshotJSON), nil
}

// Seed restores an edge's CURRENT snapshot from the store (spec_infos rows
// with format "mcp", written by earlier LoadSnapshots) so a restarted
// collector diffs the next observed list against the last persisted one
// instead of silently re-baselining. It never overrides live state.
func (d *MCPDetector) Seed(info model.SpecInfo, raw []byte) error {
	if info.Format != model.SpecFormatMCP || len(raw) == 0 {
		return nil
	}
	tools, err := contract.ParseToolsList(raw)
	if err != nil {
		return fmt.Errorf("seed mcp contract %q: %w", info.Integration, err)
	}
	edgeRef := mcpEdgeRef(info.PeerHost, "client")
	c, err := contract.FromToolsList(tools, edgeRef, info.LoadedAt, "observed tools/list at "+info.LoadedAt)
	if err != nil {
		return fmt.Errorf("seed mcp contract %q: %w", info.Integration, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if st := d.edges[edgeRef]; st != nil && st.current != nil {
		return nil // live state wins
	}
	d.edges[edgeRef] = &mcpEdgeState{integration: info.Integration, current: c}
	return nil
}

// currentContract returns the edge's CURRENT contract (nil when no snapshot
// has been observed yet).
func (d *MCPDetector) currentContract(peerHost, direction string) *contract.Contract {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st := d.edges[mcpEdgeRef(peerHost, direction)]; st != nil {
		return st.current
	}
	return nil
}

// DetectCall validates one MCP tools/call record against the edge's CURRENT
// snapshot. With no snapshot observed yet it is a pass-through (capture + edge
// discovery only — same posture as the HTTP path without a spec).
func (d *MCPDetector) DetectCall(call model.RedactedCall) []model.Finding {
	cur := d.currentContract(call.PeerHost, call.Direction)
	if cur == nil {
		return nil
	}
	toolName := call.MCPToolName
	if toolName == "" {
		toolName = strings.TrimPrefix(call.Route, "/")
	}
	if toolName == "" {
		return nil
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")

	op := cur.Op(toolName)
	if op == nil {
		// stale_client: the agent is calling a tool the CURRENT list no longer
		// declares (renamed/removed server-side, or the client cached an old
		// list). Consumer-side — LOCAL ONLY, never flaggable.
		return []model.Finding{staleToolFinding(call, toolName, now)}
	}

	var findings []model.Finding

	// stale_client: arguments vs the CURRENT inputSchema (spec §4.C.3).
	if op.InputSchema != nil && call.RequestBody != "" && !call.RequestBodyTruncated {
		for _, v := range validateAgainstSchema(op.InputSchema, call.RequestBody, call, "request") {
			findings = append(findings, mcpFinding(mcpFindingSpec{
				kind:           model.KindStaleClient,
				severity:       model.SeverityWarning,
				locationPrefix: "$.request.arguments",
				detailNoun:     "argument",
			}, v, call, toolName, now))
		}
	}

	// output_mismatch: structuredContent vs the declared outputSchema.
	// No outputSchema → NO output_mismatch (the honest limit; never synthesize).
	// isError results carry error output, not contract evidence — they feed the
	// error-rate metric instead (spec §1 "Schema-vs-implementation").
	// structuredContent is stored with content-type application/json; the
	// content[] text fallback (text/plain) is not governed by outputSchema.
	if op.OutputSchema != nil && !call.MCPIsError &&
		call.ResponseBody != "" && !call.ResponseBodyTruncated &&
		isJSONContentType(call.ResponseContentType) {
		for _, v := range validateAgainstSchema(op.OutputSchema, call.ResponseBody, call, "response") {
			findings = append(findings, mcpFinding(mcpFindingSpec{
				kind:           model.KindOutputMismatch,
				severity:       model.SeverityBreaking,
				locationPrefix: "$.response.structuredContent",
				detailNoun:     "result field",
				// The optional snapshot_observed_at (CONTRACTS §4): the CURRENT
				// snapshot this call was validated against.
				snapshotObservedAt: cur.Version.ObservedAt,
			}, v, call, toolName, now))
		}
	}
	return findings
}

func isJSONContentType(ct string) bool {
	return ct == "application/json" || strings.HasPrefix(ct, "application/json;")
}

// schemaViolation is one surviving schema error after the token-aware +
// captured-props judgment, with the finding's `actual` already rendered.
type schemaViolation struct {
	se     *openapi3.SchemaError
	actual string
}

// validateAgainstSchema validates one stored body (JSON text) against a
// neutral contract.Schema via the SAME validator as the HTTP path
// (kin-openapi), honoring the token-aware rules exactly like DetectLiveVsSpec:
// a redacted scalar's error is skipped unless the call carries a matching
// captured-props record (part = "request"|"response") that DECIDES the
// constraint as violated.
func validateAgainstSchema(schema contract.Schema, body string, call model.RedactedCall, part string) []schemaViolation {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var s openapi3.Schema
	if err := s.UnmarshalJSON(raw); err != nil {
		return nil // an undecodable schema is the provider's problem, not evidence
	}
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return nil // non-JSON / truncated body: nothing to judge
	}
	verr := s.VisitJSON(value, openapi3.MultiErrors())
	if verr == nil {
		return nil
	}
	var out []schemaViolation
	for _, se := range collectSchemaErrors(verr) {
		if redactedValue(se.Value) {
			// Same one-directional skip as the HTTP path: redacted = unknown,
			// unless the captured props prove the ORIGINAL violated a decidable
			// constraint (type, min/maxLength).
			props, ok := capturedFieldProps(call, part, se)
			if !ok || evaluateRedactedConstraint(se, props) != propsViolated {
				continue
			}
			out = append(out, schemaViolation{se: se, actual: actualFromProps(se.SchemaField, props)})
			continue
		}
		out = append(out, schemaViolation{se: se, actual: actualFromValue(se.Value)})
	}
	return out
}

// mcpFindingSpec parametrizes the shared call-scoped finding builder over the
// two call-evidence kinds (output_mismatch / stale_client args).
type mcpFindingSpec struct {
	kind           string
	severity       string
	locationPrefix string
	detailNoun     string
	// snapshotObservedAt is set for output_mismatch only (the CURRENT
	// snapshot's ObservedAt); stale_client stays a local notice without it.
	snapshotObservedAt string
}

// mcpFinding builds one call-scoped MCP finding in the EXISTING contract-drift
// artifact shape: expected-per-contract vs actual at fieldPath, representative
// source call (which carries the MCP correlation keys), dedup signature,
// occurrence seed. Endpoint = the tool name (Operation.ID) per the signature
// convention (edge, operation.id, rule, fieldPath).
func mcpFinding(spec mcpFindingSpec, v schemaViolation, call model.RedactedCall, toolName, now string) model.Finding {
	fieldPath := strings.Join(v.se.JSONPointer(), ".")
	location := spec.locationPrefix
	if fieldPath != "" {
		location += "." + fieldPath
	}
	rule := ruleFromSchemaField(v.se.SchemaField)
	sourceID := call.ID
	detail := fmt.Sprintf("Tool `%s` %s `%s` %s.", toolName, spec.detailNoun, lastSegment(fieldPath), v.se.Reason)
	if v.se.Reason == "" {
		detail = fmt.Sprintf("Tool `%s` %s `%s` violates the declared schema (%s).", toolName, spec.detailNoun, lastSegment(fieldPath), rule)
	}
	if spec.kind == model.KindStaleClient {
		detail = fmt.Sprintf("Your client is calling `%s` against a stale definition — %s", toolName, detail)
	}
	f := model.Finding{
		SchemaVersion:      model.SchemaVersion,
		ID:                 otlpattr.NewID(),
		Kind:               spec.kind,
		Severity:           spec.severity,
		Integration:        call.Integration,
		Endpoint:           toolName,
		FieldPath:          model.Ptr(fieldPath),
		Location:           model.Ptr(location),
		Expected:           expectedFromSchema(v.se),
		Actual:             v.actual,
		Rule:               rule,
		SourceCallID:       &sourceID,
		DetectedAt:         now,
		Detail:             detail,
		SnapshotObservedAt: spec.snapshotObservedAt,
		OccurrenceCount:    1,
		FirstSeen:          now,
		LastSeen:           now,
	}
	f.Signature = f.ComputeSignature()
	return f
}

// staleToolFinding is the "tool no longer listed" case of stale_client.
func staleToolFinding(call model.RedactedCall, toolName, now string) model.Finding {
	sourceID := call.ID
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              otlpattr.NewID(),
		Kind:            model.KindStaleClient,
		Severity:        model.SeverityWarning,
		Integration:     call.Integration,
		Endpoint:        toolName,
		FieldPath:       model.Ptr(""),
		Location:        model.Ptr("$.request"),
		Expected:        "a tool declared in the current tools/list",
		Actual:          fmt.Sprintf("tools/call to `%s` (not listed)", toolName),
		Rule:            RuleToolNotListed,
		SourceCallID:    &sourceID,
		DetectedAt:      now,
		Detail:          fmt.Sprintf("Your client is calling `%s` against a stale definition — the server's current tools/list does not declare it.", toolName),
		OccurrenceCount: 1,
		FirstSeen:       now,
		LastSeen:        now,
	}
	f.Signature = f.ComputeSignature()
	return f
}

// RuleToolNotListed is the stale_client rule for a call to a tool absent from
// the CURRENT tools/list.
const RuleToolNotListed = "tool-not-listed"

// definitionChangeFinding maps one classified change onto the finding shape:
// one finding per (edge, operation.id, rule, fieldPath) — the signature
// convention — with the classifier's before/after FRAGMENTS as evidence and
// both snapshot versions + timestamps. No source call (the evidence is the
// snapshot pair, exactly like version-diff).
func definitionChangeFinding(integration string, ch diff.Change, prev, cur *contract.Contract, now string) model.Finding {
	severity := model.SeverityBreaking
	switch ch.Class {
	case diff.ClassNonBreaking:
		severity = model.SeverityInfo
	case diff.ClassDescription:
		// Informational: a wording change is not a severity claim. Flaggable
		// since qfix2-2026-08-26 (the evidence is the provider's own published
		// text) — but only ever by a human pressing the control; the detector
		// never flags anything.
		severity = model.SeverityWarning
	}
	f := model.Finding{
		SchemaVersion:   model.SchemaVersion,
		ID:              otlpattr.NewID(),
		Kind:            model.KindDefinitionChange,
		Severity:        severity,
		Integration:     integration,
		Endpoint:        ch.OperationID,
		FieldPath:       model.Ptr(ch.FieldPath),
		Expected:        fragmentJSON(ch.Before),
		Actual:          fragmentJSON(ch.After),
		Rule:            ch.Rule,
		SpecVersionFrom: model.Ptr(shortHash(prev.Version.ContentHash)),
		SpecVersionTo:   model.Ptr(shortHash(cur.Version.ContentHash)),
		DetectedAt:      now,
		Detail: fmt.Sprintf("Definition change (%s): %s on `%s`%s — tools/list observed %s → %s.",
			ch.Class, ch.Rule, ch.OperationID, atFieldPath(ch.FieldPath),
			prev.Version.ObservedAt, cur.Version.ObservedAt),
		// The optional snapshot_observed_at (CONTRACTS §4): the AFTER snapshot.
		SnapshotObservedAt: cur.Version.ObservedAt,
		// The optional snapshot_observed_from (CONTRACTS §4): the BEFORE
		// snapshot — structured, so readers never regex the Detail prose.
		SnapshotObservedFrom: prev.Version.ObservedAt,
		OccurrenceCount:      1,
		FirstSeen:            now,
		LastSeen:             now,
	}
	f.Signature = f.ComputeSignature()
	return f
}

func atFieldPath(p string) string {
	if p == "" {
		return ""
	}
	return " at " + p
}

// fragmentJSON renders a classifier before/after schema FRAGMENT (never a
// whole schema) for the finding's expected/actual slots.
func fragmentJSON(v any) string {
	if v == nil {
		return "(none)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// shortHash abbreviates a snapshot content hash for the version slots.
func shortHash(h string) string {
	if len(h) > 12 {
		return "sha256:" + h[:12]
	}
	return h
}
