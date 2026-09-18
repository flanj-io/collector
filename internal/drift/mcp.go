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
	"sort"
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
	// adapters are operator-configured discovery meta-tools per peer host
	// (meta.go); the baked adapters apply without them.
	adapters map[string]MetaAdapter
	// traffic is what the observed-traffic detectors learned (observed.go),
	// keyed edge|tool.
	traffic map[string]*observedState
}

// mcpEdgeState is one MCP edge's snapshot pair. Versioning is by content hash
// (contract.Version.ContentHash); ObservedAt rides contract.Version.
type mcpEdgeState struct {
	integration string
	current     *contract.Contract
	previous    *contract.Contract
	// tools is the CURRENT snapshot's tool list, kept so a toolset-enabled
	// listing can be compared tool by tool against it (LoadSnapshot).
	tools []contract.ToolDef
	// searched is the edge's PARTIAL catalog (meta.go): definitions learned from
	// search results and from toolset-enabled listings — partial by nature,
	// never a source of removals.
	searched map[string]*searchedTool
	// enabledAt is when a toolset-enable call last succeeded on this edge.
	enabledAt string
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
	// A listing right after a toolset was enabled (brief §3.3) is the SESSION's
	// catalog: the toolset is in it because this client asked, and the next
	// session will not see it. Rotating it in as the baseline would read the
	// toolset appearing as a catalog change now and the next plain listing as
	// its removal. So it is compared tool by tool, its new tools join the
	// partial catalog, and the baseline stays.
	if st.current != nil && st.current.Version.ContentHash != c.Version.ContentHash && enabledJustBefore(st.enabledAt, snap.ObservedAt) {
		base, baseAt := st.tools, st.current.Version.ObservedAt
		d.mu.Unlock()
		return d.loadEnabledListing(snap, base, baseAt, tools), model.SpecInfo{}, nil, nil
	}
	var prev *contract.Contract
	var prevTools []contract.ToolDef
	switch {
	case st.current == nil:
		// First snapshot for this edge: it becomes the baseline; nothing to diff.
		st.current, st.tools = c, tools
	case st.current.Version.ContentHash == c.Version.ContentHash:
		// Unchanged surface: keep the versions as they are (re-observations of
		// the same list must not produce findings or rotate the baseline).
	default:
		prevTools = st.tools
		st.previous = st.current
		st.current, st.tools = c, tools
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
		findings = d.classifyListing(snap.Integration, snap.PeerHost, prevTools, tools, prev, cur, now)
	}
	return findings, info, []byte(snap.SnapshotJSON), nil
}

// reportable is the ruled finding set of one comparison: R-B's reported cells
// (additive changes — a new tool, a new optional param, a widened input, a
// newly declared output schema — are real but never findings), minus wording
// changes that differ only in whitespace, letter case or punctuation (R-B's
// wording rule). The other half of that rule, at most one wording finding per
// tool per day, holds by construction here: a tool's description change has
// ONE signature, so every later edit bumps that finding rather than adding one.
func reportable(changes []diff.Change) []diff.Change {
	out := make([]diff.Change, 0, len(changes))
	for _, ch := range diff.Reportable(changes) {
		if ch.Rule == diff.RuleDescriptionChanged {
			b, _ := ch.Before.(string)
			a, _ := ch.After.(string)
			if diff.TrivialWordingChange(b, a) {
				continue
			}
		}
		out = append(out, ch)
	}
	return out
}

// classifyListing turns one tools/list comparison into findings. When the
// server moved its catalog behind discovery meta-tools, that is ONE
// catalog/INFO event (R-B) and never a removal per hidden tool: the tools did
// not go away, they stopped being listed.
func (d *MCPDetector) classifyListing(integration, peerHost string, prevTools, curTools []contract.ToolDef, prev, cur *contract.Contract, now string) []model.Finding {
	changes := reportable(diff.Classify(prev, cur))
	if hidden, meta := d.movedBehindMetaTools(peerHost, prevTools, curTools); hidden > 0 {
		// Only a tool BOTH listings carry can have changed. The hidden tools'
		// removals are not removals, and the classifier may pair a hidden tool
		// with a newly listed meta-tool whose input schema matches — a rename
		// that never happened, and a wording change riding on it.
		both := map[string]bool{}
		for _, t := range prevTools {
			both[t.Name] = true
		}
		kept := []diff.Change{catalogMovedChange(hidden, meta)}
		for _, t := range curTools {
			if !both[t.Name] {
				continue
			}
			for _, ch := range changes {
				if ch.OperationID == t.Name && ch.Kind != diff.KindCatalog {
					kept = append(kept, ch)
				}
			}
		}
		changes = kept
	}
	findings := make([]model.Finding, 0, len(changes))
	for _, ch := range changes {
		findings = append(findings, definitionChangeFinding(integration, ch, prev, cur, now))
	}
	return findings
}

// movedBehindMetaTools reports how many tools the previous listing carried
// that the current one hides behind discovery meta-tools, and those
// meta-tools' names. Zero unless the current listing is ONLY meta-tools, with
// at least one search or dispatcher among them.
func (d *MCPDetector) movedBehindMetaTools(peerHost string, prevTools, curTools []contract.ToolDef) (int, []string) {
	if len(curTools) == 0 || len(prevTools) == 0 {
		return 0, nil
	}
	a := d.adapterFor(peerHost)
	entry := false
	names := make([]string, 0, len(curTools))
	listed := map[string]bool{}
	for _, t := range curTools {
		if !a.isMeta(t.Name) {
			return 0, nil
		}
		if _, dispatch := a.dispatch[t.Name]; a.search[t.Name] || dispatch {
			entry = true
		}
		names = append(names, t.Name)
		listed[t.Name] = true
	}
	if !entry {
		return 0, nil
	}
	hidden := 0
	for _, t := range prevTools {
		if !listed[t.Name] && !a.isMeta(t.Name) {
			hidden++
		}
	}
	sort.Strings(names)
	return hidden, names
}

// catalogMovedChange is the ONE change a catalog moving behind meta-tools is.
func catalogMovedChange(hidden int, meta []string) diff.Change {
	kind, sev, reported, _ := diff.Grade(diff.RuleCatalogMovedBehindMetaTools)
	return diff.Change{
		Kind: kind, Severity: sev, Reported: reported,
		Rule:   diff.RuleCatalogMovedBehindMetaTools,
		Before: fmt.Sprintf("%d tools listed directly", hidden),
		After:  "discovery meta-tools only: " + strings.Join(meta, ", "),
		Detail: fmt.Sprintf("The catalog moved behind discovery meta-tools (%s): %d tools are no longer listed directly. They were not removed — calls reach them through search and dispatch.", strings.Join(meta, ", "), hidden),
	}
}

// enableWindow is how long after a toolset-enable call a listing on the same
// edge is read as that session's expanded catalog. A client re-lists on the
// server's tools/list_changed, which follows the enable at once.
const enableWindow = 2 * time.Minute

func enabledJustBefore(enabledAt, observedAt string) bool {
	if enabledAt == "" {
		return false
	}
	e, errE := time.Parse(time.RFC3339Nano, enabledAt)
	o, errO := time.Parse(time.RFC3339Nano, observedAt)
	if errE != nil || errO != nil {
		return false
	}
	return !o.Before(e) && o.Sub(e) <= enableWindow
}

// loadEnabledListing handles a listing observed right after a toolset was
// enabled: tools the baseline also lists are compared like any
// re-observation; tools only this session sees join the partial catalog
// (source toolset_enable), so calls to them are judged, not called stale; the
// baseline is not replaced; nothing is ever reported removed from it.
func (d *MCPDetector) loadEnabledListing(snap otlpattr.ContractSnapshot, base []contract.ToolDef, baseAt string, listed []contract.ToolDef) []model.Finding {
	edgeRef := mcpEdgeRef(snap.PeerHost, snap.Direction)
	byName := make(map[string]contract.ToolDef, len(base))
	for _, t := range base {
		byName[t.Name] = t
	}
	var before, after []contract.ToolDef
	for _, t := range listed {
		if b, ok := byName[t.Name]; ok {
			before, after = append(before, b), append(after, t)
			continue
		}
		c, err := contract.FromToolsList([]contract.ToolDef{t}, edgeRef, snap.ObservedAt, "toolset-enabled tools/list at "+snap.ObservedAt)
		if err != nil {
			continue
		}
		d.mu.Lock()
		st := d.edges[edgeRef]
		if st.searched == nil {
			st.searched = map[string]*searchedTool{}
		}
		if cur := st.searched[t.Name]; (cur == nil && len(st.searched) < maxPartialTools) || (cur != nil && cur.source == partialFromEnable) {
			st.searched[t.Name] = &searchedTool{def: t, contract: c, observedAt: snap.ObservedAt, changedAt: snap.ObservedAt, source: partialFromEnable}
		}
		d.mu.Unlock()
	}
	if len(before) == 0 {
		return nil
	}
	prev, err := contract.FromToolsList(before, edgeRef, baseAt, "observed tools/list at "+baseAt)
	if err != nil {
		return nil
	}
	cur, err := contract.FromToolsList(after, edgeRef, snap.ObservedAt, "toolset-enabled tools/list at "+snap.ObservedAt)
	if err != nil || prev.Version.ContentHash == cur.Version.ContentHash {
		return nil
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	var findings []model.Finding
	for _, ch := range reportable(diff.Classify(prev, cur)) {
		f := definitionChangeFinding(snap.Integration, ch, prev, cur, now)
		f.Source, f.Completeness = partialFromEnable, "partial"
		f.Signature = f.ComputeSignature()
		findings = append(findings, f)
	}
	return findings
}

// noteEnable records a successful toolset-enable call on its edge.
func (d *MCPDetector) noteEnable(call model.RedactedCall) {
	at := call.CapturedAt
	if at == "" {
		at = time.Now().UTC().Format(time.RFC3339Nano)
	}
	edgeRef := mcpEdgeRef(call.PeerHost, call.Direction)
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.edges[edgeRef]
	if st == nil {
		st = &mcpEdgeState{}
		d.edges[edgeRef] = st
	}
	st.enabledAt = at
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
	if st == nil {
		st = &mcpEdgeState{}
		d.edges[edgeRef] = st
	}
	if st.current == nil {
		st.integration, st.current, st.tools = info.Integration, c, tools
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
	prevTools := st.tools
	st.previous = st.current
	st.current, st.tools = c, tools
	prev, cur := st.previous, st.current
	d.mu.Unlock()

	// The same ruled set the observe path reports: before 2026-09-18 this
	// path classified without R-B's reported filter, so a front adopting a
	// sibling's newer listing reported ADDITIVE changes too.
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return d.classifyListing(info.Integration, info.PeerHost, prevTools, tools, prev, cur, now), true, nil
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
	j := d.Judge(call)
	return j.Findings, j.Validation
}

// Judgement is everything judging one tools/call yields.
type Judgement struct {
	Findings   []model.Finding
	Validation model.Validation
	// InnerTool / ViaDispatch are set when a dispatcher call was re-attributed
	// to the inner tool it named (R-E, brief §3.2): the processor re-keys the
	// stored call to InnerTool and keeps the dispatcher as its via_dispatch.
	InnerTool, ViaDispatch string
	// SearchSpec is the edge's search-learned catalog when this call (a search)
	// changed it — a contract row to persist (brief §3.1).
	SearchSpec *SpecDoc
}

// Judge is JudgeCall with the rest of what the processor needs.
func (d *MCPDetector) Judge(call model.RedactedCall) Judgement {
	toolName := call.MCPToolName
	if toolName == "" {
		toolName = strings.TrimPrefix(call.Route, "/")
	}
	var j Judgement

	// Discovery meta-tools (meta.go, R-E). A search result teaches the edge
	// tool definitions; a dispatcher call whose inner name exactly matches one
	// a SEARCH returned is judged as THAT tool, with the dispatcher kept as
	// evidence; a successful toolset-enable call marks the listing that follows
	// it as the session's. None of this needs the tools/list baseline, so all
	// of it runs before its check.
	a := d.adapterFor(call.PeerHost)
	if a.search[toolName] && !call.MCPIsError && !call.ResponseBodyTruncated {
		j.Findings, j.SearchSpec = d.ingestSearchResult(call)
	}
	if a.enable[toolName] && !call.MCPIsError && call.MCPErrorCode == 0 {
		d.noteEnable(call)
	}
	if t, ok := a.dispatch[toolName]; ok && !call.RequestBodyTruncated {
		if inner, args, ok := dispatchInner(t, call.RequestBody); ok {
			if op, seen, src := d.partialOpAt(call.PeerHost, call.Direction, inner, true); op != nil {
				innerCall := call
				innerCall.RequestBody = string(args)
				fs, v := judgeOp(innerCall, op, inner, seen)
				for i := range fs {
					fs[i].ViaDispatch = toolName
					fs[i].Source, fs[i].Completeness = src, "partial"
					fs[i].Signature = fs[i].ComputeSignature()
				}
				fs = append(fs, d.observeTraffic(call, inner, string(args), toolName)...)
				j.Findings = append(j.Findings, fs...)
				j.Validation, j.InnerTool, j.ViaDispatch = v, inner, toolName
				return j
			}
			// An inner name no search result named stays attributed to the
			// dispatcher. Nothing is guessed from the shape of the call.
		}
	}

	// The observed-traffic detectors need no contract at all: they compare
	// the server with its own earlier behaviour.
	j.Findings = append(j.Findings, d.observeTraffic(call, toolName, call.RequestBody, "")...)

	cur := d.currentContract(call.PeerHost, call.Direction)
	if cur == nil {
		j.Validation = model.NotValidated(model.NotValidatedNoContract)
		return j
	}
	if toolName == "" {
		j.Validation = model.NotValidated(model.NotValidatedToolNotListed)
		return j
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")

	op := cur.Op(toolName)
	if op == nil {
		// A tool the complete listing does not declare but the PARTIAL catalog
		// does — a searched tool called directly, or a toolset this session
		// enabled — is judged against that definition (brief §3.4), never
		// called stale: the agent is using what the server told it.
		if pop, seen, src := d.partialOpAt(call.PeerHost, call.Direction, toolName, false); pop != nil {
			fs, v := judgeOp(call, pop, toolName, seen)
			for i := range fs {
				fs[i].Source, fs[i].Completeness = src, "partial"
				fs[i].Signature = fs[i].ComputeSignature()
			}
			j.Findings = append(j.Findings, fs...)
			j.Validation = v
			return j
		}
		// stale_client: the agent is calling a tool the CURRENT list no longer
		// declares (renamed/removed server-side, or the client cached an old
		// list). Consumer-side — LOCAL ONLY, never flaggable.
		j.Findings = append(j.Findings, staleToolFinding(call, toolName, now))
		j.Validation = model.NotValidated(model.NotValidatedToolNotListed)
		return j
	}
	fs, v := judgeOp(call, op, toolName, cur.Version.ObservedAt)
	j.Findings = append(j.Findings, fs...)
	j.Validation = v
	return j
}

// judgeOp validates one call against one operation: the arguments against its
// inputSchema (stale_client) and the result against its outputSchema
// (output_mismatch). snapshotObservedAt is when that operation's definition
// was observed — the tools/list snapshot, or the search result it came from.
func judgeOp(call model.RedactedCall, op *contract.Operation, toolName, snapshotObservedAt string) ([]model.Finding, model.Validation) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")

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
				changeKind:     string(diff.KindObservedFailure),
				severity:       model.SeverityBreaking,
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
			changeKind:     string(diff.KindOutput),
			severity:       model.SeverityBreaking,
			locationPrefix: "$.response.structuredContent",
			detailNoun:     "result field",
			// The optional snapshot_observed_at (CONTRACTS §4): when the
			// definition this call was validated against was observed.
			snapshotObservedAt: snapshotObservedAt,
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
	kind string
	// changeKind is R-A's axis (output for output_mismatch, observed_failure
	// for stale_client) — see model.Finding.ChangeKind.
	changeKind     string
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
		ChangeKind:         spec.changeKind,
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
		SchemaVersion: model.SchemaVersion,
		ID:            otlpattr.NewID(),
		Kind:          model.KindStaleClient,
		ChangeKind:    string(diff.KindObservedFailure),
		// R-B (Idan, 2026-09-17): stale_client on a real call is
		// observed_failure / BREAKING — the call the agent just made fails.
		// Still LOCAL ONLY: it is consumer-side, and the kind rule
		// (model.Finding.Flaggable) is unchanged.
		Severity:        model.SeverityBreaking,
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

// severityOf maps the classifier's ruled severity (R-B's upper-case
// vocabulary) onto the finding wire's lower-case one. The two spellings exist
// on purpose: R-B is written in upper case and the drift dataset publishes it
// that way, while model.Severity* is a field the control plane, the dashboard
// and e2e all already read, so re-casing it would be a breaking wire change
// for no gain. This function is the only place the two meet.
//
// Additive (unreported) changes never reach here: LoadSnapshot builds
// findings from diff.Reportable. An empty severity is therefore a programming
// error, not a data case; it maps to info (never flaggable) rather than
// silently inheriting a severity nobody ruled.
func severityOf(s diff.Severity) string {
	switch s {
	case diff.SeverityBreaking:
		return model.SeverityBreaking
	case diff.SeverityWarning:
		return model.SeverityWarning
	case diff.SeverityInfo:
		return model.SeverityInfo
	}
	return model.SeverityInfo
}

// definitionChangeFinding maps one classified change onto the finding shape:
// one finding per (edge, operation.id, rule, fieldPath) — the signature
// convention — with the classifier's before/after FRAGMENTS as evidence and
// both snapshot versions + timestamps. No source call (the evidence is the
// snapshot pair, exactly like version-diff).
func definitionChangeFinding(integration string, ch diff.Change, prev, cur *contract.Contract, now string) model.Finding {
	severity := severityOf(ch.Severity)
	f := model.Finding{
		SchemaVersion: model.SchemaVersion,
		ID:            otlpattr.NewID(),
		Kind:          model.KindDefinitionChange,
		// ChangeKind is R-A's kind (wording | input | output | catalog): WHAT
		// moved, a finer axis than Kind, which names WHICH DETECTOR spoke
		// (definition_change here). The two are separate fields because they
		// answer different questions and one cannot be derived from the other.
		ChangeKind:      string(ch.Kind),
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
		// The classifier's Detail (set only where the class is not readable
		// off the rule id — the optional-removal cells) goes BEFORE the
		// timestamp tail, which two UIs regex out of the end of this string
		// (TestDefinitionChangeDetailTail_UIRegex).
		Detail: fmt.Sprintf("Definition change (%s/%s): %s on `%s`%s%s — tools/list observed %s → %s.",
			ch.Kind, ch.Severity, ch.Rule, ch.OperationID, atFieldPath(ch.FieldPath), consequence(ch.Detail),
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

// consequence renders a classifier Detail as a clause of the finding's Detail
// prose, or nothing when the change carries none.
func consequence(d string) string {
	if d == "" {
		return ""
	}
	return " — " + d
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
