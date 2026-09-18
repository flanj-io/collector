package drift

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanj-io/collector/contract"
	"github.com/flanj-io/collector/contract/diff"
	"github.com/flanj-io/collector/internal/model"
)

// Discovery meta-tools (brief 2026-09-17 §3, ruling R-E).
//
// Some MCP servers list only discovery meta-tools — a search tool and a
// generic dispatcher — instead of their catalog. An agent then learns a tool
// from a search result and calls it through the dispatcher, so without help
// every contract and every finding lands on `call_tool` rather than on the
// tool the agent actually used. This file gives the detector the two things it
// needs, and nothing more:
//
//   - search results become per-tool contracts (source search_result,
//     completeness partial): a tool RE-OBSERVED with a different definition
//     is a definition change; a tool absent from a later result is nothing —
//     a search page is never the whole catalog.
//   - a dispatcher call is re-attributed to its inner tool ONLY when the inner
//     name exactly matches a tool the SAME server returned in a search result
//     already recorded here (R-E, option C). Nothing is inferred from shape:
//     an unknown name stays attributed to the dispatcher.
//
// Which tools are search tools and dispatchers comes from the baked adapters
// below and from operator config (MetaAdapter, per peer host). The collector
// never probes: everything here is read off traffic the agent already sent.

// MetaAdapter says, for one server, which tools are discovery meta-tools.
type MetaAdapter struct {
	// SearchTools return tool definitions ({name, description?, inputSchema,
	// outputSchema?} entries) in their result.
	SearchTools []string `mapstructure:"search_tools"`
	// DispatchTools call another tool by name.
	DispatchTools []DispatchTool `mapstructure:"dispatch_tools"`
	// EnableTools switch a toolset on for the session (brief §3.3). A tools/list
	// observed right after one of them succeeds is the SESSION's expanded
	// catalog, not a change to the server's: see LoadSnapshot.
	EnableTools []string `mapstructure:"enable_tools"`
}

// DispatchTool is a generic call tool and where its arguments name the inner
// tool and carry the inner arguments.
type DispatchTool struct {
	Name string `mapstructure:"name"`
	// NameArg / ArgsArg default to the first present of name|tool and
	// arguments|args.
	NameArg string `mapstructure:"name_arg"`
	ArgsArg string `mapstructure:"args_arg"`
}

// bakedAdapter is the known-server patterns (brief §3.5), by tool name. Each
// entry is a pattern seen in the wild, not a guess at a shape: Sentry's
// search_sentry_tools / execute_sentry_tool (census 2026-09-17), the CPZAI-
// style search_tools / call_tool pair, Shopware's shopware-tool-search, and
// the GitHub MCP server's dynamic-toolset enable_toolset.
var bakedAdapter = MetaAdapter{
	SearchTools: []string{"search_tools", "search_sentry_tools", "shopware-tool-search"},
	DispatchTools: []DispatchTool{
		{Name: "call_tool"}, {Name: "execute_tool"}, {Name: "execute_sentry_tool"},
	},
	EnableTools: []string{"enable_toolset"},
}

// SetMetaAdapters installs operator-configured adapters, keyed by peer host.
// They are added to the baked ones for that host; they never remove one.
func (d *MCPDetector) SetMetaAdapters(byHost map[string]MetaAdapter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.adapters = byHost
}

// adapterSet is one host's meta-tools, by role.
type adapterSet struct {
	search   map[string]bool
	dispatch map[string]DispatchTool
	enable   map[string]bool
}

// isMeta reports whether a tool is any kind of discovery meta-tool.
func (a adapterSet) isMeta(name string) bool {
	_, dispatch := a.dispatch[name]
	return a.search[name] || dispatch || a.enable[name]
}

func (d *MCPDetector) adapterFor(peerHost string) adapterSet {
	a := adapterSet{search: map[string]bool{}, dispatch: map[string]DispatchTool{}, enable: map[string]bool{}}
	add := func(m MetaAdapter) {
		for _, n := range m.SearchTools {
			a.search[n] = true
		}
		for _, t := range m.DispatchTools {
			a.dispatch[t.Name] = t
		}
		for _, n := range m.EnableTools {
			a.enable[n] = true
		}
	}
	add(bakedAdapter)
	d.mu.Lock()
	if m, ok := d.adapters[peerHost]; ok {
		add(m)
	}
	d.mu.Unlock()
	return a
}

// Where a partial-catalog entry came from.
const (
	partialFromSearch = "search_result"  // a discovery meta-tool's search result
	partialFromEnable = "toolset_enable" // a tools/list right after a toolset was enabled
)

// maxPartialTools bounds one edge's partial catalog. A search tool can return
// anything; memory must not.
const maxPartialTools = 500

// searchedTool is one tool definition learned outside a complete tools/list:
// from a search result, or from a session's toolset-enabled listing.
type searchedTool struct {
	def        contract.ToolDef
	contract   *contract.Contract
	observedAt string // the latest observation of this definition
	changedAt  string // when this CONTENT was first observed
	source     string // partialFromSearch | partialFromEnable
}

// SpecDoc is a contract row to persist: its metadata and its document.
type SpecDoc struct {
	Info model.SpecInfo
	Raw  []byte
}

// ingestSearchResult records the tool definitions a search result carried and
// returns a definition_change for every tool RE-observed with a different
// definition, plus the edge's search-learned catalog as a contract row when
// this result changed it (brief §3.1). First sight of a tool is a baseline,
// never a finding; absence from a later result is never a removal.
func (d *MCPDetector) ingestSearchResult(call model.RedactedCall) ([]model.Finding, *SpecDoc) {
	defs := searchResultTools(call.ResponseBody)
	if len(defs) == 0 {
		return nil, nil
	}
	edge := mcpEdgeRef(call.PeerHost, call.Direction)
	observedAt := call.CapturedAt
	if observedAt == "" {
		observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	var findings []model.Finding
	changed := false
	for _, def := range defs {
		c, err := contract.FromToolsList([]contract.ToolDef{def}, edge, observedAt, "search result from "+call.MCPToolName+" at "+observedAt)
		if err != nil {
			continue // one undecodable definition never costs the others
		}
		d.mu.Lock()
		st := d.edges[edge]
		if st == nil {
			st = &mcpEdgeState{}
			d.edges[edge] = st
		}
		if st.searched == nil {
			st.searched = map[string]*searchedTool{}
		}
		prev := st.searched[def.Name]
		if prev == nil && len(st.searched) >= maxPartialTools {
			d.mu.Unlock()
			continue
		}
		entry := &searchedTool{def: def, contract: c, observedAt: observedAt, changedAt: observedAt, source: partialFromSearch}
		same := prev != nil && prev.source == partialFromSearch && prev.contract.Version.ContentHash == c.Version.ContentHash
		if same {
			entry.changedAt = prev.changedAt // a re-observation of the same content moves nothing
		} else {
			changed = true
		}
		st.searched[def.Name] = entry
		d.mu.Unlock()
		if prev == nil || same {
			continue
		}
		for _, ch := range reportable(diff.Classify(prev.contract, c)) {
			if ch.Rule == diff.RuleOperationRemoved {
				continue // unreachable for a single re-observed tool; never a removal from a search page
			}
			f := definitionChangeFinding(call.Integration, ch, prev.contract, c, now)
			f.Source, f.Completeness = partialFromSearch, "partial"
			f.Signature = f.ComputeSignature()
			findings = append(findings, f)
		}
	}
	if !changed {
		return findings, nil
	}
	return findings, d.searchSpec(call)
}

// searchSpec renders an edge's search-learned catalog as one contract row:
// tools/list-shaped, sorted by name, keyed `<integration>:search`, stamped with
// when its content last changed so an unchanged catalog never restamps.
func (d *MCPDetector) searchSpec(call model.RedactedCall) *SpecDoc {
	edge := mcpEdgeRef(call.PeerHost, call.Direction)
	d.mu.Lock()
	st := d.edges[edge]
	var tools []contract.ToolDef
	var loadedAt string
	if st != nil {
		for _, t := range st.searched {
			if t.source != partialFromSearch {
				continue
			}
			tools = append(tools, t.def)
			if observedAfter(t.changedAt, loadedAt) || loadedAt == "" {
				loadedAt = t.changedAt
			}
		}
	}
	d.mu.Unlock()
	if len(tools) == 0 {
		return nil
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	raw, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return nil
	}
	return &SpecDoc{
		Info: model.SpecInfo{
			Integration: model.SearchSpecIntegration(call.Integration),
			Role:        model.SpecRoleProvider,
			PeerHost:    call.PeerHost,
			EdgeClass:   call.EdgeClass,
			Format:      model.SpecFormatMCP,
			Source:      model.SpecSourceSearchResult,
			Title:       call.MCPServerName,
			Version:     call.MCPServerVersion,
			Endpoints:   len(tools),
			LoadedAt:    loadedAt,
		},
		Raw: raw,
	}
}

// SeedSearched offers an edge the search-learned catalog the STORE holds — so
// a restarted collector, and every tiered front, starts from what any pod
// already learned instead of from nothing. Per tool, the newer observation of
// a different definition wins; adoption reports nothing (the pod that saw the
// change reported it). It reports whether anything was adopted.
func (d *MCPDetector) SeedSearched(info model.SpecInfo, raw []byte) (bool, error) {
	if info.Source != model.SpecSourceSearchResult || len(raw) == 0 {
		return false, nil
	}
	tools, err := contract.ParseToolsList(raw)
	if err != nil {
		return false, fmt.Errorf("seed search catalog %q: %w", info.Integration, err)
	}
	tools = dropUndecodableSchemas(tools)
	edge := mcpEdgeRef(info.PeerHost, "client")
	adopted := false
	for _, def := range tools {
		c, err := contract.FromToolsList([]contract.ToolDef{def}, edge, info.LoadedAt, "search result (seeded) at "+info.LoadedAt)
		if err != nil {
			continue
		}
		d.mu.Lock()
		st := d.edges[edge]
		if st == nil {
			st = &mcpEdgeState{}
			d.edges[edge] = st
		}
		if st.searched == nil {
			st.searched = map[string]*searchedTool{}
		}
		cur := st.searched[def.Name]
		take := cur == nil && len(st.searched) < maxPartialTools
		if cur != nil && cur.contract.Version.ContentHash != c.Version.ContentHash && observedAfter(info.LoadedAt, cur.changedAt) {
			take = true
		}
		if take {
			st.searched[def.Name] = &searchedTool{def: def, contract: c, observedAt: info.LoadedAt, changedAt: info.LoadedAt, source: partialFromSearch}
			adopted = true
		}
		d.mu.Unlock()
	}
	return adopted, nil
}

// partialOpAt is a tool's definition from the edge's partial catalog, when it
// FALLS BACK there: the observation time and where it came from. searchOnly
// restricts it to search results — the only source R-E lets re-key a
// dispatcher call.
func (d *MCPDetector) partialOpAt(peerHost, direction, tool string, searchOnly bool) (*contract.Operation, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.edges[mcpEdgeRef(peerHost, direction)]
	if st == nil || st.searched == nil {
		return nil, "", ""
	}
	s := st.searched[tool]
	if s == nil || (searchOnly && s.source != partialFromSearch) {
		return nil, "", ""
	}
	return s.contract.Op(tool), s.observedAt, s.source
}

// dispatchInner reads a dispatcher call's inner tool name and inner
// arguments. ok is false when the request carries no string name.
func dispatchInner(t DispatchTool, requestBody string) (inner string, args json.RawMessage, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(requestBody), &m) != nil {
		return "", nil, false
	}
	pick := func(explicit string, fallbacks ...string) (string, json.RawMessage) {
		keys := fallbacks
		if explicit != "" {
			keys = []string{explicit}
		}
		for _, k := range keys {
			if v, ok := m[k]; ok {
				return k, v
			}
		}
		return "", nil
	}
	_, rawName := pick(t.NameArg, "name", "tool")
	if rawName == nil || json.Unmarshal(rawName, &inner) != nil || inner == "" {
		return "", nil, false
	}
	_, args = pick(t.ArgsArg, "arguments", "args")
	if args == nil {
		args = json.RawMessage("{}")
	}
	return inner, args, true
}

// searchResultTools reads tool definitions from a search result body: the
// first array, anywhere in the document, of objects carrying a name and an
// input schema (snake_case keys tolerated). Everything else is ignored.
func searchResultTools(body string) []contract.ToolDef {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &v) != nil {
		return nil
	}
	var arr []any
	var find func(any)
	find = func(x any) {
		if arr != nil {
			return
		}
		switch t := x.(type) {
		case []any:
			for _, e := range t {
				if o, ok := e.(map[string]any); ok && o["name"] != nil && (o["inputSchema"] != nil || o["input_schema"] != nil) {
					arr = t
					return
				}
			}
			for _, e := range t {
				find(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				find(t[k])
			}
		}
	}
	find(v)
	var out []contract.ToolDef
	for _, e := range arr {
		o, ok := e.(map[string]any)
		if !ok {
			continue
		}
		for from, to := range map[string]string{"input_schema": "inputSchema", "output_schema": "outputSchema"} {
			if s, ok := o[from]; ok && o[to] == nil {
				o[to] = s
			}
		}
		b, err := json.Marshal(map[string]any{"tools": []any{o}})
		if err != nil {
			continue
		}
		defs, err := contract.ParseToolsList(b)
		if err == nil && len(defs) == 1 && defs[0].Name != "" {
			out = append(out, dropUndecodableSchemas(defs)...)
		}
	}
	return out
}
