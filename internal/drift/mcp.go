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
	tools = dropUndecodableSchemas(tools)
	edgeRef := mcpEdgeRef(snap.PeerHost, snap.Direction)
	c, err := contract.FromToolsList(tools, edgeRef, snap.ObservedAt, "observed tools/list at "+snap.ObservedAt)
	if err != nil {
		return nil, model.SpecInfo{}, nil, fmt.Errorf("mcp snapshot: %w", err)
	}

	info := model.SpecInfo{
		Integration: snap.Integration,
		// Carried so the UI can tell a stdio server from its HTTP twin: both
		// publish the same serverInfo.name, and a local-process server has no
		// edge row to look it up from.
		EdgeClass: snap.EdgeClass,
		Role:      model.SpecRoleProvider,
		PeerHost:  snap.PeerHost,
		Format:    model.SpecFormatMCP,
		// A tools/list arrived on the wire; nobody configured or uploaded it.
		// Left unset, the store's column default made every snapshot a
		// CONFIG-loaded contract to every consumer of `source` but the one
		// card whose format branch hid it (launch-week item 7, 2026-09-07).
		Source:    model.SpecSourceObserved,
		Title:     snap.ServerName,
		Version:   snap.ServerVersion,
		Endpoints: len(tools),
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

	// The row's loaded_at is when THIS CONTENT was first observed — the
	// current contract's own stamp — not when this record happened to arrive.
	// The two differ on every re-observation of an unchanged list, and the
	// store row is what the UI anchors "validated N calls since this snapshot"
	// on: restamping it on each identical tools/list flipped every call
	// captured before the restamp to NOT CHECKED with no change to the
	// contract at all (seen on the tiered lane, 2026-09-07, where a second
	// front's first listing did exactly that to the first front's calls). The
	// same stability is what lets the store's loaded_at serve as the change
	// token the contract channel refreshes on.
	info.LoadedAt = cur.Version.ObservedAt

	var findings []model.Finding
	if prev != nil {
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
		for _, ch := range diff.Classify(prev, cur) {
			findings = append(findings, definitionChangeFinding(snap.Integration, ch, prev, cur, now))
		}
	}
	return findings, info, []byte(snap.SnapshotJSON), nil
}

// dropUndecodableSchemas blanks any tool schema contract.CanonicalizeSchema
// cannot represent, leaving the rest of the list intact.
//
// It is a blast radius limiter, not leniency. FromToolsList fails the WHOLE
// contract on one bad schema, and LoadSnapshot turns that into a dropped
// tools/list: the edge then keeps whatever contract it last held, forever, and
// no definition_change ever fires again — a silent false green, caused by one
// tool nobody was even looking at. A tool whose schema we cannot decode
// degrades to the honest "no contract declared" state (exactly what a tool
// publishing no outputSchema already gets) and its siblings keep theirs.
//
// Boolean schemas are NOT this path — CanonicalizeSchema represents those
// exactly. This catches what is left: a schema that is a bare array, number or
// string, which is not a JSON Schema in any draft.
func dropUndecodableSchemas(tools []contract.ToolDef) []contract.ToolDef {
	out := tools
	copied := false
	for i := range tools {
		inBad := schemaUndecodable(tools[i].InputSchema)
		outBad := schemaUndecodable(tools[i].OutputSchema)
		if !inBad && !outBad {
			continue
		}
		if !copied { // copy-on-write: the caller's slice is never mutated
			out = append([]contract.ToolDef(nil), tools...)
			copied = true
		}
		if inBad {
			out[i].InputSchema = nil
		}
		if outBad {
			out[i].OutputSchema = nil
		}
	}
	return out
}

func schemaUndecodable(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	_, err := contract.CanonicalizeSchema(raw)
	return err != nil
}

// Seed offers an edge the snapshot the STORE holds — the org-wide baseline —
// and reports what came of it: whether the edge's state changed, and the
// findings the adoption produced, if any.
//
// The store's spec_infos rows of format "mcp" are written by every collector
// that observes a tools/list (directly when a store is co-located, as a
// forwarded spec_info record from a tiered front). Seeding from them is what
// keeps the baseline from being one process's memory. At Start it is how a
// restarted collector diffs the next observed list against the last one
// anyone persisted instead of silently re-baselining. On every refresh it is
// how a tiered FRONT — which owns no store and has witnessed nothing — learns
// what a SIBLING front observed: the rename front-a saw must make the stale
// client calling through front-b a stale_client finding, not a NOT CHECKED
// call, and it must not take a restart of front-b to get there.
//
// The newer observation wins, ordered by when each list was observed
// (contract.Version.ObservedAt; on the seed side that is the row's loaded_at):
//   - no live baseline: the seed becomes it;
//   - same content as the live baseline: nothing to learn about the contract,
//     but the two stamps are two fronts' FIRST sightings of one list, and the
//     edge converges on the EARLIER. Every front forwards its stamp back up on
//     each re-observation; while each kept its own, the store row alternated
//     between the two forever — every flip re-downloaded the document on every
//     front and moved the UI's "since this snapshot" anchor, the NOT CHECKED
//     flip this seeding exists to end (2026-09-08). With every front holding
//     the earliest stamp they all forward the same one, and the row never
//     moves for a list that did not. A seed at the same instant or later
//     changes nothing;
//   - the seed was observed strictly later: adopted; the live baseline
//     rotates to previous, and the definition diff between them is REPORTED,
//     exactly as the observe path reports it. It used to be adopted silently,
//     on the theory that the front which observed the change had reported
//     it — but a front with NO baseline observes a change and reports nothing
//     (it has nothing to diff), so a rename first seen by a fresh front was
//     reported by nobody while the front holding V1 adopted V2 without a
//     word. Findings dedup by signature, so a front that did already report
//     it adds an occurrence: the honest count, since this front now judges
//     calls against the changed list too;
//   - otherwise — the live baseline is newer, or the two cannot be ordered —
//     live wins. This process is ahead of the store, and the store catches
//     up the way it always has: the spec_info record LoadSnapshot emitted.
func (d *MCPDetector) Seed(info model.SpecInfo, raw []byte) ([]model.Finding, bool, error) {
	if info.Format != model.SpecFormatMCP || len(raw) == 0 {
		return nil, false, nil
	}
	tools, err := contract.ParseToolsList(raw)
	if err != nil {
		return nil, false, fmt.Errorf("seed mcp contract %q: %w", info.Integration, err)
	}
	tools = dropUndecodableSchemas(tools)
	edgeRef := mcpEdgeRef(info.PeerHost, "client")
	c, err := contract.FromToolsList(tools, edgeRef, info.LoadedAt, "observed tools/list at "+info.LoadedAt)
	if err != nil {
		return nil, false, fmt.Errorf("seed mcp contract %q: %w", info.Integration, err)
	}
	d.mu.Lock()
	st := d.edges[edgeRef]
	if st == nil || st.current == nil {
		d.edges[edgeRef] = &mcpEdgeState{integration: info.Integration, current: c}
		d.mu.Unlock()
		return nil, true, nil
	}
	if st.current.Version.ContentHash == c.Version.ContentHash {
		if !observedAfter(st.current.Version.ObservedAt, c.Version.ObservedAt) {
			d.mu.Unlock()
			return nil, false, nil // same list, and live already holds the earlier stamp
		}
		st.current = c // same list, first sighted earlier elsewhere: converge; previous stays
		d.mu.Unlock()
		return nil, true, nil
	}
	if !observedAfter(c.Version.ObservedAt, st.current.Version.ObservedAt) {
		d.mu.Unlock()
		return nil, false, nil // live is newer (or the order is unknowable): live wins
	}
	st.integration = info.Integration
	st.previous = st.current
	st.current = c
	prev, cur := st.previous, st.current
	d.mu.Unlock()

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	var findings []model.Finding
	for _, ch := range diff.Classify(prev, cur) {
		findings = append(findings, definitionChangeFinding(info.Integration, ch, prev, cur, now))
	}
	return findings, true, nil
}

// observedAfter reports whether a was observed strictly later than b. Both are
// RFC 3339 stamps — the SDK's record time on one side, the store row's
// loaded_at on the other — parsed rather than compared as text so a
// millisecond stamp and a whole-second one order correctly. A pair that will
// not parse falls back to the lexical order ISO-8601 UTC provides, and a tie
// is never "after": it must not displace live state.
func observedAfter(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA == nil && errB == nil {
		return ta.After(tb)
	}
	return a > b
}

// HasBaseline reports whether the edge holds a CURRENT snapshot — observed or
// seeded — to judge calls against. A call to an edge without one is captured,
// not judged, which is the caller's cue to ask the store for a baseline.
func (d *MCPDetector) HasBaseline(peerHost, direction string) bool {
	return d.currentContract(peerHost, direction) != nil
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
	fs, _ := d.JudgeCall(call)
	return fs
}

// JudgeCall is DetectCall plus the verdict the processor stamps on the call
// (model.Validation): whether the RESULT was validated against the tool's
// declared outputSchema and, when it was not, the first gate below that stopped
// it — named in this function's own order, so the reason is the one that
// applied. The stale_client checks on the ARGUMENTS run regardless and never
// move the verdict: they are about the consumer, not the provider's response.
func (d *MCPDetector) JudgeCall(call model.RedactedCall) ([]model.Finding, model.Validation) {
	cur := d.currentContract(call.PeerHost, call.Direction)
	if cur == nil {
		return nil, model.NotValidated(model.NotValidatedNoContract)
	}
	toolName := call.MCPToolName
	if toolName == "" {
		toolName = strings.TrimPrefix(call.Route, "/")
	}
	if toolName == "" {
		return nil, model.NotValidated(model.NotValidatedToolNotListed)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")

	op := cur.Op(toolName)
	if op == nil {
		// stale_client: the agent is calling a tool the CURRENT list no longer
		// declares (renamed/removed server-side, or the client cached an old
		// list). Consumer-side — LOCAL ONLY, never flaggable.
		return []model.Finding{staleToolFinding(call, toolName, now)},
			model.NotValidated(model.NotValidatedToolNotListed)
	}

	// MCP revision 2026-07-28, `resultType: "input_required"`. The server is
	// asking the caller for more input; the exchange is MID-FLIGHT. Neither half
	// of it is contract evidence:
	//   - the result is partial by design, so validating it against outputSchema
	//     reports every required field the server has not filled in yet;
	//   - the arguments are partial by design too — an interactive tool is
	//     designed to be called without them and to ask — so validating them
	//     against inputSchema.required accuses the client of being stale for
	//     doing exactly what the tool asked for.
	// Both would fire on NORMAL traffic, on precisely the tools an agent uses
	// most. The call is still captured; it is simply not judged.
	//
	// This sits AFTER the tool-not-listed check on purpose: calling a tool the
	// current catalog does not declare is a fact about the tool name, not the
	// payload, and stays true whatever the result type.
	if call.MCPResultType == model.MCPResultTypeInputRequired {
		return nil, model.NotValidated(model.NotValidatedInputRequired)
	}

	var findings []model.Finding

	// stale_client: arguments vs the CURRENT inputSchema (spec §4.C.3).
	if op.InputSchema != nil && call.RequestBody != "" && !call.RequestBodyTruncated {
		violations, _ := validateAgainstSchema(op.InputSchema, call.RequestBody, call, "request")
		for _, v := range violations {
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
	// A Tasks handle (revision 2026-07-28) is an ENVELOPE: the call returned
	// {task:{taskId,…}} and the tool's real payload arrives later via tasks/get,
	// a surface the SDK does not instrument yet. The SDK already drops the body
	// of such a record; this second gate states the rule where the judgment is
	// made, so a future capture change cannot quietly start validating a task
	// envelope against the tool's outputSchema.
	//
	// Each gate is its own case so the verdict can name the FIRST one that
	// stopped the check — the same order the comment above lists them in.
	switch {
	case op.OutputSchema == nil:
		return findings, model.NotValidated(model.NotValidatedNoOutputContract)
	case call.MCPIsError:
		return findings, model.NotValidated(model.NotValidatedErrorResult)
	case call.MCPTaskID != "":
		return findings, model.NotValidated(model.NotValidatedTaskHandle)
	case call.ResponseBody == "" || call.ResponseBodyTruncated || !isJSONContentType(call.ResponseContentType):
		return findings, model.NotValidated(model.NotValidatedResultNotJSON)
	}
	violations, judged := validateAgainstSchema(op.OutputSchema, call.ResponseBody, call, "response")
	if !judged {
		// application/json that did not parse: nothing was compared to the
		// schema, and saying "clean" about it would be the lie this verdict
		// exists to stop.
		return findings, model.NotValidated(model.NotValidatedResultNotJSON)
	}
	for _, v := range violations {
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
	return findings, model.VerdictOf(findings)
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
//
// judged is false when NOTHING was compared — an undecodable schema, or a body
// that is not JSON — which the caller must not read as "no violations".
func validateAgainstSchema(schema contract.Schema, body string, call model.RedactedCall, part string) (out []schemaViolation, judged bool) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, false
	}
	var s openapi3.Schema
	if err := s.UnmarshalJSON(raw); err != nil {
		return nil, false // an undecodable schema is the provider's problem, not evidence
	}
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return nil, false // non-JSON / truncated body: nothing to judge
	}
	verr := s.VisitJSON(value, openapi3.MultiErrors())
	if verr == nil {
		return nil, true
	}
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
	return out, true
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
