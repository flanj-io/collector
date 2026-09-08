package flanjui

// The agent-facing drift read surface: an MCP server the collector's own
// operator points their agent at, so the agent can ask "what changed on the
// dependencies I call?" and be answered by the collector already watching them.
//
// ─── Where it lives, and why here ────────────────────────────────────────────
//
// It is a route on the LOCAL UI extension's server (mcpPath), not a listener of
// its own. That is the whole security story in one sentence: `ui_endpoint` is
// validated to a loopback address (Config.Validate), so the agent surface
// inherits the collector's outbound-only posture unchanged — nothing new binds,
// nothing new is reachable off-host, and an operator who has decided where the
// local UI listens has already decided where this listens.
//
// It is READ-ONLY. There is no flag tool, no acknowledge tool, no contract
// upload — every mutation on this collector stays behind the browser guard
// (guard.go: POST + `X-Flanj-UI: 1` + JSON + same-origin), which an agent does
// not satisfy and is not meant to. Raising a thread with a provider is a human
// act with a human's name on it; suggest-and-approve is a later slice.
//
// ─── The redaction floor on this surface ─────────────────────────────────────
//
// NO RAW BODY EVER CROSSES HERE, and that is enforced by construction, not by
// review: no tool returns a call body, a header map, or a URL. Findings carry
// only the fields the UI already renders, and the three free-VALUE fields among
// them (expected / actual / detail) are put through the redaction floor one
// more time on the way out (redactFindingValues). The floor is idempotent and
// add-only, so a clean value is returned byte-identical and the shape parity
// with GET /api/findings holds; a value that somehow arrived carrying a secret
// is tokenised before an agent — whose next hop may be a model provider — ever
// sees it. mcp_test.go asserts both halves.
//
// ─── Honesty when there is nothing to report ─────────────────────────────────
//
// An empty finding list has two completely different causes: "we looked and
// found nothing" and "nothing looked". This surface never collapses them. Every
// answer carries the per-call validation tally (model.RedactedCall.Validated —
// the drift processor's own verdict, never a mirror of it) for the scope that
// was asked about, and the prose says which of the two it is. A collector with
// no traffic says so; a collector with traffic and no contract says so; neither
// is an error and neither is an all-clear.
//
// ─── Protocol ────────────────────────────────────────────────────────────────
//
// github.com/modelcontextprotocol/go-sdk speaks 2026-07-28 and negotiates down
// through 2025-11-25 (what @modelcontextprotocol/sdk 1.30.0 — the version the
// e2e org-app and mock-mcp harnesses pin — speaks) to 2024-11-05, so the
// harnesses and this server meet on 2025-11-25 with no pin of our own.
// Stateless: a read-only server keeps nothing between calls, so there is no
// session to lose and GET/DELETE answer 405.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/redact"
	"github.com/flanj-io/collector/internal/store"
)

// mcpPath is the agent surface's route on the local UI server.
const mcpPath = "/mcp"

// mcpServerName is the implementation name an agent sees in `initialize`.
const mcpServerName = "flanj-collector"

// mcpCallWindow is the call page every evidence tally is computed over — the
// SAME page GET /api/calls serves the browser, so the agent's "n of m calls
// validated" and the operator's cannot disagree about which calls they mean.
const mcpCallWindow = 200

// mcpDefaultFindingLimit caps a list_findings answer when the caller names no
// limit. The store page above it is 200; a finding list is dedup'd per endpoint
// so 50 is a large answer already.
const mcpDefaultFindingLimit = 50

// mcpInstructions is what an agent is told the server is for, verbatim, at
// initialize. It states the read-only boundary because an agent that believes
// it can flag will waste a turn discovering it cannot.
const mcpInstructions = `Drift findings from the Flanj collector running in this environment.

It watches the API and MCP dependencies this deployment actually calls (edges are
discovered from observed traffic — nothing is configured) and reports where a
provider's live behaviour has departed from its contract.

Read-only. Nothing here changes the collector, and nothing here leaves this
environment. Raising a thread with a provider is done by a person in the local UI.

Start with drift_summary. An empty finding list is NOT an all-clear on its own —
every answer carries how many calls were actually validated against a contract,
and you should read that before reporting "no drift".`

// mcpHandler builds (once) the agent surface's HTTP handler.
//
// Cross-origin protection wraps it in addition to the SDK's own localhost
// DNS-rebinding check: a page in the operator's browser must not be able to
// drive the agent surface just because it shares an origin with a UI the
// operator has open. Non-browser clients send neither Sec-Fetch-Site nor
// Origin and pass untouched, which is every MCP client there is.
func (e *uiExtension) mcpHandler() http.Handler {
	e.mcpOnce.Do(func() {
		h := mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return e.mcpServer() },
			&mcp.StreamableHTTPOptions{
				Stateless:           true,
				JSONResponse:        true,
				MaxRequestBodyBytes: maxSmallBodyBytes,
			},
		)
		e.mcpH = http.NewCrossOriginProtection().Handler(h)
	})
	return e.mcpH
}

// mcpServer builds the read-only tool set. Built once and shared by every
// session: the tools are static and all state lives in the store.
func (e *uiExtension) mcpServer() *mcp.Server {
	e.mcpSrvOnce.Do(func() {
		s := mcp.NewServer(&mcp.Implementation{
			Name:        mcpServerName,
			Title:       "Flanj collector — drift findings",
			Description: "Read-only drift findings for the dependencies this deployment calls.",
			Version:     collectorVersion,
		}, &mcp.ServerOptions{Instructions: mcpInstructions})

		readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

		mcp.AddTool(s, &mcp.Tool{
			Name:  "drift_summary",
			Title: "Drift summary",
			Description: "Start here. One line per dependency this collector has observed: how much traffic, " +
				"how much of it was actually validated against a contract, and how many open findings it has. " +
				"Reports honestly when nothing has been checked yet.",
			Annotations: readOnly,
		}, e.mcpDriftSummary)

		mcp.AddTool(s, &mcp.Tool{
			Name:  "list_edges",
			Title: "List observed dependencies",
			Description: "The external integration edges this collector discovered from observed traffic " +
				"(no target list is configured). Use the `peer_host` of a row as the `edge` argument elsewhere.",
			Annotations: readOnly,
		}, e.mcpListEdges)

		mcp.AddTool(s, &mcp.Tool{
			Name:  "list_findings",
			Title: "List open drift findings",
			Description: "Open drift findings — where a provider's live behaviour departed from its contract. " +
				"Same rows the local UI shows. Acknowledged findings are excluded unless you ask for them.",
			Annotations: readOnly,
		}, e.mcpListFindings)

		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_finding",
			Title:       "Get one drift finding",
			Description: "One finding by id, plus a body-free summary of the call that evidences it.",
			Annotations: readOnly,
		}, e.mcpGetFinding)

		e.mcpSrv = s
	})
	return e.mcpSrv
}

// ─── evidence ────────────────────────────────────────────────────────────────

// mcpEvidence is the per-call validation tally over the loaded window, for one
// scope (the whole collector, or one edge). It is read STRAIGHT off
// model.RedactedCall.Validated — the verdict the drift processor stamped where
// validation runs. Nothing here re-derives "was this checked?" from facts about
// the edge; that mirror is what reported CONFORMING over calls nothing had
// validated (see model.RedactedCall.Validated's own comment).
type mcpEvidence struct {
	// WindowCalls is how many calls of this scope are in the loaded window.
	WindowCalls int `json:"window_calls"`
	// ValidatedCalls were checked against a contract (clean or drifted).
	ValidatedCalls int `json:"validated_calls"`
	// NotValidatedCalls were seen but not checked, for the reasons below.
	NotValidatedCalls int `json:"not_validated_calls"`
	// NotValidatedReasons counts flanj.validated.reason values ("no-contract",
	// "not-routable", …). "unknown" means the record carried no verdict at all.
	NotValidatedReasons map[string]int `json:"not_validated_reasons,omitempty"`
}

// tally folds one call into the evidence.
func (ev *mcpEvidence) tally(c model.RedactedCall) {
	ev.WindowCalls++
	switch c.Validated {
	case model.ValidatedClean, model.ValidatedDrifted:
		ev.ValidatedCalls++
	default:
		ev.NotValidatedCalls++
		reason := c.ValidatedReason
		if reason == "" {
			reason = model.ValidatedUnknown
		}
		if ev.NotValidatedReasons == nil {
			ev.NotValidatedReasons = map[string]int{}
		}
		ev.NotValidatedReasons[reason]++
	}
}

// sentence renders the evidence as the clause that must accompany any claim
// about findings. `scope` names what was counted ("this collector", "api.acme.com").
func (ev mcpEvidence) sentence(scope string) string {
	switch {
	case ev.WindowCalls == 0:
		return fmt.Sprintf("No calls for %s are in the current window, so nothing has been checked.", scope)
	case ev.ValidatedCalls == 0 && ev.WindowCalls == 1:
		return fmt.Sprintf(
			"The one call for %s in the current window was not validated against a contract (%s), so an empty finding list is not an all-clear.",
			scope, reasonPhrase(ev.NotValidatedReasons))
	case ev.ValidatedCalls == 0:
		return fmt.Sprintf(
			"None of the %d calls for %s in the current window were validated against a contract (%s), so an empty finding list is not an all-clear.",
			ev.WindowCalls, scope, reasonPhrase(ev.NotValidatedReasons))
	default:
		return fmt.Sprintf("%d of %d %s for %s in the current window %s validated against a contract.",
			ev.ValidatedCalls, ev.WindowCalls, plural(ev.WindowCalls, "call", "calls"), scope,
			plural(ev.WindowCalls, "was", "were"))
	}
}

// reasonPhrase renders the not-validated reason counts in a stable order, so
// the sentence is deterministic and a test can assert it.
func reasonPhrase(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "no reason recorded"
	}
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, reasons[k]))
	}
	return strings.Join(parts, ", ")
}

// mcpEvidenceFor tallies the loaded call window, optionally narrowed to one
// peer host.
func mcpEvidenceFor(st store.Store, peerHost string) (mcpEvidence, error) {
	calls, err := st.ListCalls(mcpCallWindow)
	if err != nil {
		return mcpEvidence{}, err
	}
	var ev mcpEvidence
	for _, c := range calls {
		if peerHost != "" && c.PeerHost != peerHost {
			continue
		}
		ev.tally(c)
	}
	return ev, nil
}

// ─── finding rows ────────────────────────────────────────────────────────────

// mcpFindingRow is one finding as this surface emits it: the EXACT JSON
// GET /api/findings produces for the same finding (findingView), with the three
// free-value fields put through the redaction floor once more.
//
// It is a map, not a struct, on purpose. Restating findingView's fields here
// would be a second declaration of the same shape, and the two would drift —
// the acceptance is that an agent and a human see the same truth, so the row is
// built by marshalling the very row the browser gets.
type mcpFindingRow = map[string]any

// redactFindingValues applies the redaction floor to the free-VALUE fields of a
// finding row before it leaves for an agent.
//
// Which fields, and why only these: `expected`, `actual` and `detail` are the
// only ones carrying observed content — `actual` can quote a scalar lifted from
// a response body (internal/drift actualFromValue), and `detail` embeds it in
// prose. Everything else is structural (ids, hashes, timestamps, kind, rule,
// endpoint, field_path, integration) and running a redactor over those could
// only corrupt an identifier the caller correlates on.
//
// The floor is idempotent and add-only, so a clean value comes back byte for
// byte and shape parity with the browser's row is preserved.
func redactFindingValues(row mcpFindingRow) {
	r := redact.New()
	for _, k := range []string{"expected", "actual", "detail"} {
		if s, ok := row[k].(string); ok && s != "" {
			row[k] = r.Redact(s).Text
		}
	}
}

// mcpFindingRows renders the browser's finding rows for the agent surface.
func mcpFindingRows(views []findingView) ([]mcpFindingRow, error) {
	raw, err := json.Marshal(views)
	if err != nil {
		return nil, err
	}
	var rows []mcpFindingRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		redactFindingValues(row)
	}
	return rows, nil
}

// findingEdgeHost attributes a finding to an edge.
//
// A finding's own peer_host is the host of its pinned source call, and it is
// EMPTY on a call-less finding — which is exactly the MCP `definition_change`,
// the kind that answers "what changed on my dependencies?" best. Dropping those
// from an edge query would hide the headline case, so a call-less finding falls
// back to the host its contract is bound to (SpecInfo.Integration ->
// SpecInfo.PeerHost), the same pairing the Contracts tab makes.
func findingEdgeHost(row mcpFindingRow, specHosts map[string]string) string {
	if h, _ := row["peer_host"].(string); h != "" {
		return h
	}
	if integration, _ := row["integration"].(string); integration != "" {
		return specHosts[integration]
	}
	return ""
}

// specHostIndex maps a contract's integration id to the host it is bound to.
func specHostIndex(st store.Store) (map[string]string, error) {
	infos, err := st.ListSpecInfos()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(infos))
	for _, in := range infos {
		if in.PeerHost != "" {
			out[in.Integration] = in.PeerHost
		}
	}
	return out, nil
}

// ─── drift_summary ───────────────────────────────────────────────────────────

type mcpDriftSummaryInput struct{}

// mcpEdgeSummary is one dependency's line in the summary.
type mcpEdgeSummary struct {
	PeerHost     string         `json:"peer_host"`
	DisplayName  string         `json:"display_name,omitempty"`
	Direction    string         `json:"direction"`
	CallCount    int            `json:"call_count"`
	DriftCount   int            `json:"drift_count"`
	RPM          float64        `json:"rpm"`
	LastSeen     string         `json:"last_seen,omitempty"`
	OpenFindings int            `json:"open_findings"`
	BySeverity   map[string]int `json:"open_findings_by_severity,omitempty"`
	Evidence     mcpEvidence    `json:"evidence"`
}

type mcpDriftSummaryOutput struct {
	Headline     string           `json:"headline"`
	Edges        []mcpEdgeSummary `json:"edges"`
	OpenFindings int              `json:"open_findings_total"`
	Acknowledged int              `json:"acknowledged_findings_total"`
	// Unattributed are open findings that reach no edge: call-less, and with no
	// contract bound to a host to attribute them through. They are in
	// OpenFindings but on no edge line, so the per-edge counts would otherwise
	// silently sum to less than the total an agent just read.
	Unattributed int         `json:"unattributed_open_findings,omitempty"`
	Evidence     mcpEvidence `json:"evidence"`
}

func (e *uiExtension) mcpDriftSummary(ctx context.Context, _ *mcp.CallToolRequest, _ mcpDriftSummaryInput) (*mcp.CallToolResult, mcpDriftSummaryOutput, error) {
	st, err := e.mcpStore()
	if err != nil {
		return nil, mcpDriftSummaryOutput{}, err
	}
	edges, op, err := e.edgeRows(st)
	if err != nil {
		return nil, mcpDriftSummaryOutput{}, e.mcpStoreErr(op, err)
	}
	views, op, err := e.findingRows(st)
	if err != nil {
		return nil, mcpDriftSummaryOutput{}, e.mcpStoreErr(op, err)
	}
	rows, err := mcpFindingRows(views)
	if err != nil {
		return nil, mcpDriftSummaryOutput{}, err
	}
	specHosts, err := specHostIndex(st)
	if err != nil {
		return nil, mcpDriftSummaryOutput{}, e.mcpStoreErr("list contracts", err)
	}
	calls, err := st.ListCalls(mcpCallWindow)
	if err != nil {
		return nil, mcpDriftSummaryOutput{}, e.mcpStoreErr("list calls", err)
	}

	perHost := map[string]*mcpEvidence{}
	var all mcpEvidence
	for _, c := range calls {
		all.tally(c)
		ev, ok := perHost[c.PeerHost]
		if !ok {
			ev = &mcpEvidence{}
			perHost[c.PeerHost] = ev
		}
		ev.tally(c)
	}

	out := mcpDriftSummaryOutput{Evidence: all, Edges: make([]mcpEdgeSummary, 0, len(edges))}
	openByHost := map[string]int{}
	sevByHost := map[string]map[string]int{}
	for _, row := range rows {
		if acked, _ := row["acked"].(bool); acked {
			out.Acknowledged++
			continue
		}
		out.OpenFindings++
		h := findingEdgeHost(row, specHosts)
		if h == "" {
			out.Unattributed++
		}
		openByHost[h]++
		sev, _ := row["severity"].(string)
		if sevByHost[h] == nil {
			sevByHost[h] = map[string]int{}
		}
		sevByHost[h][sev]++
	}

	for _, ed := range edges {
		es := mcpEdgeSummary{
			PeerHost:     ed.PeerHost,
			DisplayName:  ed.DisplayName,
			Direction:    ed.Direction,
			CallCount:    ed.CallCount,
			DriftCount:   ed.DriftCount,
			RPM:          ed.RPM,
			LastSeen:     ed.LastSeen,
			OpenFindings: openByHost[ed.PeerHost],
			BySeverity:   sevByHost[ed.PeerHost],
		}
		if ev, ok := perHost[ed.PeerHost]; ok {
			es.Evidence = *ev
		}
		out.Edges = append(out.Edges, es)
	}

	out.Headline = mcpSummaryHeadline(out, all)
	return &mcp.CallToolResult{Content: mcpText(mcpSummaryText(out))}, out, nil
}

// mcpSummaryHeadline is the one line an agent is most likely to quote, so it
// must never be an all-clear the evidence does not support.
func mcpSummaryHeadline(out mcpDriftSummaryOutput, ev mcpEvidence) string {
	if out.OpenFindings > 0 {
		return fmt.Sprintf("%d open drift %s across %d observed %s. %s",
			out.OpenFindings, plural(out.OpenFindings, "finding", "findings"),
			len(out.Edges), plural(len(out.Edges), "dependency", "dependencies"),
			ev.sentence("this collector"))
	}
	if ev.ValidatedCalls == 0 {
		return "Nothing validated yet. " + ev.sentence("this collector")
	}
	return "No open drift findings. " + ev.sentence("this collector")
}

func mcpSummaryText(out mcpDriftSummaryOutput) string {
	var b strings.Builder
	b.WriteString(out.Headline)
	if len(out.Edges) == 0 {
		b.WriteString("\n\nNo external dependencies have been observed yet. Edges are discovered from traffic; " +
			"until this deployment calls something, there is nothing to report.")
		return b.String()
	}
	b.WriteString("\n")
	for _, es := range out.Edges {
		name := es.PeerHost
		if es.DisplayName != "" {
			name = fmt.Sprintf("%s (%s)", es.DisplayName, es.PeerHost)
		}
		fmt.Fprintf(&b, "\n- %s [%s] — %d %s observed, %d open %s. %s",
			name, es.Direction,
			es.CallCount, plural(es.CallCount, "call", "calls"),
			es.OpenFindings, plural(es.OpenFindings, "finding", "findings"),
			es.Evidence.sentence(es.PeerHost))
	}
	if out.Unattributed > 0 {
		fmt.Fprintf(&b, "\n\n%d open %s not attributable to any edge above (no evidence call and no contract "+
			"bound to a host). Call list_findings with no edge to see %s.",
			out.Unattributed, plural(out.Unattributed, "finding is", "findings are"),
			plural(out.Unattributed, "it", "them"))
	}
	if out.Acknowledged > 0 {
		fmt.Fprintf(&b, "\n\n%d %s acknowledged locally and excluded above.",
			out.Acknowledged, plural(out.Acknowledged, "finding is", "findings are"))
	}
	return b.String()
}

// ─── list_edges ──────────────────────────────────────────────────────────────

type mcpListEdgesInput struct {
	Direction string `json:"direction,omitempty" jsonschema:"Restrict to one orientation: \"client\" for dependencies this deployment calls (outbound), \"server\" for callers of this deployment (inbound). Omit for both."`
}

type mcpListEdgesOutput struct {
	Edges []map[string]any `json:"edges"`
	Count int              `json:"count"`
	Note  string           `json:"note,omitempty"`
}

func (e *uiExtension) mcpListEdges(ctx context.Context, _ *mcp.CallToolRequest, in mcpListEdgesInput) (*mcp.CallToolResult, mcpListEdgesOutput, error) {
	st, err := e.mcpStore()
	if err != nil {
		return nil, mcpListEdgesOutput{}, err
	}
	edges, op, err := e.edgeRows(st)
	if err != nil {
		return nil, mcpListEdgesOutput{}, e.mcpStoreErr(op, err)
	}
	raw, err := json.Marshal(edges)
	if err != nil {
		return nil, mcpListEdgesOutput{}, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, mcpListEdgesOutput{}, err
	}
	if d := strings.TrimSpace(in.Direction); d != "" {
		kept := rows[:0]
		for _, row := range rows {
			if dir, _ := row["direction"].(string); dir == d {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	out := mcpListEdgesOutput{Edges: rows, Count: len(rows)}
	if len(rows) == 0 {
		out.Edges = []map[string]any{}
		out.Note = "No external edges have been observed. Edges are discovered from traffic — nothing is configured — " +
			"so an empty list means this deployment has not called (or been called by) an external host in the current window."
	}
	return &mcp.CallToolResult{Content: mcpText(mcpEdgesText(out))}, out, nil
}

func mcpEdgesText(out mcpListEdgesOutput) string {
	if out.Count == 0 {
		return out.Note
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d observed %s:", out.Count, plural(out.Count, "edge", "edges"))
	for _, row := range out.Edges {
		host, _ := row["peer_host"].(string)
		dir, _ := row["direction"].(string)
		name, _ := row["display_name"].(string)
		if name != "" {
			host = fmt.Sprintf("%s (%s)", name, host)
		}
		fmt.Fprintf(&b, "\n- %s [%s]", host, dir)
	}
	return b.String()
}

// ─── list_findings ───────────────────────────────────────────────────────────

type mcpListFindingsInput struct {
	Edge                string `json:"edge,omitempty" jsonschema:"Only findings about this dependency — the peer_host of a list_edges row, exactly as reported (host or host:port)."`
	Kind                string `json:"kind,omitempty" jsonschema:"Only this finding kind: type-mismatch, version-diff, output_mismatch, definition_change or stale_client."`
	Severity            string `json:"severity,omitempty" jsonschema:"Only this severity: breaking, non-breaking or info."`
	IncludeAcknowledged bool   `json:"include_acknowledged,omitempty" jsonschema:"Include findings a person has acknowledged locally. Excluded by default — acknowledged means someone already read it."`
	Limit               int    `json:"limit,omitempty" jsonschema:"Maximum rows to return (default 50)."`
}

type mcpListFindingsOutput struct {
	Findings     []mcpFindingRow `json:"findings"`
	Count        int             `json:"count"`
	TotalMatched int             `json:"total_matched"`
	Acknowledged int             `json:"acknowledged_excluded"`
	Evidence     mcpEvidence     `json:"evidence"`
	Note         string          `json:"note,omitempty"`
	// KnownEdges is filled ONLY when `edge` named a host this collector has
	// never observed — the honest answer to "no findings for x" is often "there
	// is no x here", and an agent that gets an empty list without it will
	// report an all-clear about a dependency that was never watched.
	KnownEdges []string `json:"known_edges,omitempty"`
}

func (e *uiExtension) mcpListFindings(ctx context.Context, _ *mcp.CallToolRequest, in mcpListFindingsInput) (*mcp.CallToolResult, mcpListFindingsOutput, error) {
	st, err := e.mcpStore()
	if err != nil {
		return nil, mcpListFindingsOutput{}, err
	}
	views, op, err := e.findingRows(st)
	if err != nil {
		return nil, mcpListFindingsOutput{}, e.mcpStoreErr(op, err)
	}
	rows, err := mcpFindingRows(views)
	if err != nil {
		return nil, mcpListFindingsOutput{}, err
	}
	specHosts, err := specHostIndex(st)
	if err != nil {
		return nil, mcpListFindingsOutput{}, e.mcpStoreErr("list contracts", err)
	}

	edgeFilter := strings.TrimSpace(in.Edge)
	out := mcpListFindingsOutput{Findings: []mcpFindingRow{}}

	// An unknown edge is answered as an unknown edge, never as "no findings".
	if edgeFilter != "" {
		edges, op, eErr := e.edgeRows(st)
		if eErr != nil {
			return nil, mcpListFindingsOutput{}, e.mcpStoreErr(op, eErr)
		}
		known := false
		for _, ed := range edges {
			out.KnownEdges = append(out.KnownEdges, ed.PeerHost)
			if ed.PeerHost == edgeFilter {
				known = true
			}
		}
		if !known {
			out.Note = fmt.Sprintf(
				"This collector has not observed any edge %q, so it has never watched it and cannot say anything about it. Known edges: %s.",
				edgeFilter, edgeListPhrase(out.KnownEdges))
			return &mcp.CallToolResult{Content: mcpText(out.Note)}, out, nil
		}
		out.KnownEdges = nil
	}

	// The scope filters run BEFORE the ack filter, so `acknowledged_excluded`
	// counts what was excluded from THIS answer. Counting acks first made the
	// note say "3 acknowledged findings excluded" on an edge that had none.
	matched := make([]mcpFindingRow, 0, len(rows))
	for _, row := range rows {
		if edgeFilter != "" && findingEdgeHost(row, specHosts) != edgeFilter {
			continue
		}
		if in.Kind != "" {
			if k, _ := row["kind"].(string); k != in.Kind {
				continue
			}
		}
		if in.Severity != "" {
			if s, _ := row["severity"].(string); s != in.Severity {
				continue
			}
		}
		if acked, _ := row["acked"].(bool); acked && !in.IncludeAcknowledged {
			out.Acknowledged++
			continue
		}
		matched = append(matched, row)
	}
	out.TotalMatched = len(matched)

	limit := in.Limit
	if limit <= 0 {
		limit = mcpDefaultFindingLimit
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out.Findings = matched
	out.Count = len(matched)

	scope := "this collector"
	if edgeFilter != "" {
		scope = edgeFilter
	}
	if out.Evidence, err = mcpEvidenceFor(st, edgeFilter); err != nil {
		return nil, mcpListFindingsOutput{}, e.mcpStoreErr("list calls", err)
	}
	out.Note = mcpFindingsNote(out, scope)
	return &mcp.CallToolResult{Content: mcpText(mcpFindingsText(out))}, out, nil
}

// mcpFindingsNote is the sentence that stops an empty list from reading as an
// all-clear, and that says what a non-empty one is a slice of.
func mcpFindingsNote(out mcpListFindingsOutput, scope string) string {
	ev := out.Evidence.sentence(scope)
	if out.TotalMatched == 0 {
		if out.Acknowledged > 0 {
			return fmt.Sprintf("No open drift findings for %s. %s (%d acknowledged %s excluded — pass include_acknowledged to see them.)",
				scope, ev, out.Acknowledged, plural(out.Acknowledged, "finding", "findings"))
		}
		return fmt.Sprintf("No open drift findings for %s. %s", scope, ev)
	}
	if out.Count < out.TotalMatched {
		return fmt.Sprintf("Showing %d of %d matching %s for %s. %s",
			out.Count, out.TotalMatched, plural(out.TotalMatched, "finding", "findings"), scope, ev)
	}
	return fmt.Sprintf("%d open drift %s for %s. %s",
		out.TotalMatched, plural(out.TotalMatched, "finding", "findings"), scope, ev)
}

func mcpFindingsText(out mcpListFindingsOutput) string {
	var b strings.Builder
	b.WriteString(out.Note)
	for _, row := range out.Findings {
		id, _ := row["id"].(string)
		kind, _ := row["kind"].(string)
		sev, _ := row["severity"].(string)
		endpoint, _ := row["endpoint"].(string)
		detail, _ := row["detail"].(string)
		fmt.Fprintf(&b, "\n\n- [%s] %s on %s (%s)", sev, kind, endpoint, id)
		if host, _ := row["peer_host"].(string); host != "" {
			fmt.Fprintf(&b, "\n  provider: %s", host)
		}
		if detail != "" {
			fmt.Fprintf(&b, "\n  %s", detail)
		}
		expected, _ := row["expected"].(string)
		actual, _ := row["actual"].(string)
		if expected != "" || actual != "" {
			fmt.Fprintf(&b, "\n  expected %s, observed %s", expected, actual)
		}
	}
	return b.String()
}

// plural picks the singular or plural form of a noun for a count. The prose on
// this surface is read by a model that may quote it to a person, so "1
// dependency/dependencies" is worth one function.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func edgeListPhrase(hosts []string) string {
	if len(hosts) == 0 {
		return "none — no external edge has been observed yet"
	}
	return strings.Join(hosts, ", ")
}

// ─── get_finding ─────────────────────────────────────────────────────────────

type mcpGetFindingInput struct {
	ID string `json:"id" jsonschema:"The finding id, as reported by list_findings or drift_summary."`
}

// mcpSourceCall is the body-free summary of a finding's evidence call.
//
// It is an ALLOWLIST, and that is the point: the fields are enumerated here so
// that adding one is a deliberate act with a reviewer, rather than a field
// arriving on model.RedactedCall and being forwarded to an agent because the
// struct was passed through whole. Bodies, header maps and the full URL are all
// absent — the URL because a query string is the one place a credential rides
// outside a body, and `route` (the templated path the contract is matched on)
// answers every question an agent has about which endpoint drifted.
type mcpSourceCall struct {
	ID              string `json:"id"`
	CapturedAt      string `json:"captured_at,omitempty"`
	Integration     string `json:"integration,omitempty"`
	PeerHost        string `json:"peer_host,omitempty"`
	Direction       string `json:"direction,omitempty"`
	Method          string `json:"method,omitempty"`
	Route           string `json:"route,omitempty"`
	StatusCode      int    `json:"status_code,omitempty"`
	DurationMS      int    `json:"duration_ms,omitempty"`
	Transport       string `json:"transport,omitempty"`
	MCPToolName     string `json:"mcp_tool_name,omitempty"`
	MCPServerName   string `json:"mcp_server_name,omitempty"`
	MCPIsError      bool   `json:"mcp_is_error,omitempty"`
	Drifted         bool   `json:"drifted,omitempty"`
	Validated       string `json:"validated,omitempty"`
	ValidatedReason string `json:"validated_reason,omitempty"`
	// RedactionApplied says whether the floor fired on this call; the patterns
	// name WHICH floor rules did. Neither is a value.
	RedactionApplied  bool     `json:"redaction_applied"`
	RedactionPatterns []string `json:"redaction_patterns,omitempty"`
}

// sourceCallLine renders the evidence call as prose. Same allowlist as the
// struct — nothing is formatted here that is not a field of mcpSourceCall.
func sourceCallLine(sc mcpSourceCall) string {
	var b strings.Builder
	b.WriteString("Evidence call " + sc.ID)
	if sc.CapturedAt != "" {
		b.WriteString(" captured " + sc.CapturedAt)
	}
	switch {
	case sc.Transport == "mcp":
		fmt.Fprintf(&b, ": MCP tools/call %s", sc.MCPToolName)
		if sc.MCPServerName != "" {
			fmt.Fprintf(&b, " on %s", sc.MCPServerName)
		}
	case sc.Method != "" || sc.Route != "":
		fmt.Fprintf(&b, ": %s %s", sc.Method, sc.Route)
		if sc.StatusCode != 0 {
			fmt.Fprintf(&b, " -> %d", sc.StatusCode)
		}
	}
	if sc.Validated != "" {
		fmt.Fprintf(&b, ". Verdict: %s", sc.Validated)
		if sc.ValidatedReason != "" {
			fmt.Fprintf(&b, " (%s)", sc.ValidatedReason)
		}
		b.WriteString(".")
	}
	b.WriteString(" No request or response body crosses this surface.")
	return b.String()
}

func sourceCallSummary(c model.RedactedCall) mcpSourceCall {
	return mcpSourceCall{
		ID:                c.ID,
		CapturedAt:        c.CapturedAt,
		Integration:       c.Integration,
		PeerHost:          c.PeerHost,
		Direction:         c.Direction,
		Method:            c.Method,
		Route:             c.Route,
		StatusCode:        c.StatusCode,
		DurationMS:        c.DurationMS,
		Transport:         c.Transport,
		MCPToolName:       c.MCPToolName,
		MCPServerName:     c.MCPServerName,
		MCPIsError:        c.MCPIsError,
		Drifted:           c.Drifted,
		Validated:         c.Validated,
		ValidatedReason:   c.ValidatedReason,
		RedactionApplied:  c.Redaction.Applied,
		RedactionPatterns: c.Redaction.Patterns,
	}
}

type mcpGetFindingOutput struct {
	Finding    mcpFindingRow  `json:"finding,omitempty"`
	SourceCall *mcpSourceCall `json:"source_call,omitempty"`
	Found      bool           `json:"found"`
	Note       string         `json:"note,omitempty"`
}

func (e *uiExtension) mcpGetFinding(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetFindingInput) (*mcp.CallToolResult, mcpGetFindingOutput, error) {
	st, err := e.mcpStore()
	if err != nil {
		return nil, mcpGetFindingOutput{}, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, mcpGetFindingOutput{}, fmt.Errorf("id is required — pass the id of a finding from list_findings or drift_summary")
	}
	// Read through findingRows, not GetFinding, so the row carries the SAME ack
	// and peer-host joins the browser's row does.
	views, op, err := e.findingRows(st)
	if err != nil {
		return nil, mcpGetFindingOutput{}, e.mcpStoreErr(op, err)
	}
	rows, err := mcpFindingRows(views)
	if err != nil {
		return nil, mcpGetFindingOutput{}, err
	}
	for _, row := range rows {
		if rid, _ := row["id"].(string); rid != id {
			continue
		}
		out := mcpGetFindingOutput{Finding: row, Found: true}
		if scid, _ := row["source_call_id"].(string); scid != "" {
			if c, ok, cErr := st.GetCall(scid); cErr == nil && ok {
				sc := sourceCallSummary(c)
				out.SourceCall = &sc
			}
		}
		if out.SourceCall == nil {
			out.Note = "This finding has no evidence call — it was detected by comparing contract documents (a version diff, " +
				"or an MCP tools/list snapshot against the previous one), not from a single call."
		}
		text := mcpFindingsText(mcpListFindingsOutput{
			Findings: []mcpFindingRow{row},
			Note:     "Finding " + id + ":",
		})
		if out.SourceCall != nil {
			text += "\n\n" + sourceCallLine(*out.SourceCall)
		} else if out.Note != "" {
			text += "\n\n" + out.Note
		}
		return &mcp.CallToolResult{Content: mcpText(text)}, out, nil
	}
	// Not found is a fact, not a failure: the rolling window evicts, so an id an
	// agent held from a previous turn legitimately stops existing.
	out := mcpGetFindingOutput{
		Found: false,
		Note: fmt.Sprintf("No finding %q is in this collector's current window. Findings are held in a rolling window, "+
			"so an id from an earlier turn can legitimately have aged out. Call list_findings for what is here now.", id),
	}
	return &mcp.CallToolResult{Content: mcpText(out.Note)}, out, nil
}

// ─── plumbing ────────────────────────────────────────────────────────────────

// mcpText wraps prose as the tool result's unstructured content. Every tool
// returns both: the text for a client that shows the user something, the
// structured output for one that reasons over it.
func mcpText(s string) []mcp.Content {
	return []mcp.Content{&mcp.TextContent{Text: s}}
}

// mcpStore resolves the shared store for a tool call, answering the same
// condition the read routes answer with 503: the store is a dependency that is
// not there.
func (e *uiExtension) mcpStore() (store.Store, error) {
	st := e.resolveStore()
	if st == nil {
		return nil, fmt.Errorf("%s", msgStoreUnavailable)
	}
	return st, nil
}

// mcpStoreErr is storeErr for a tool call: the raw error goes to the log and
// nowhere else (a pgx connection error is the DSN in prose), and the caller
// gets the one sentence the browser gets.
func (e *uiExtension) mcpStoreErr(op string, err error) error {
	e.telemetry.Logger.Warn("agent mcp: " + op + ": " + err.Error())
	return fmt.Errorf("%s", msgStoreUnavailable)
}

// mcpState is the extension's lazily-built agent-surface state.
type mcpState struct {
	mcpOnce    sync.Once
	mcpH       http.Handler
	mcpSrvOnce sync.Once
	mcpSrv     *mcp.Server
}
