package drift

import (
	"encoding/json"
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
// style search_tools / call_tool pair, Shopware's shopware-tool-search.
var bakedAdapter = MetaAdapter{
	SearchTools: []string{"search_tools", "search_sentry_tools", "shopware-tool-search"},
	DispatchTools: []DispatchTool{
		{Name: "call_tool"}, {Name: "execute_tool"}, {Name: "execute_sentry_tool"},
	},
}

// SetMetaAdapters installs operator-configured adapters, keyed by peer host.
// They are added to the baked ones for that host; they never remove one.
func (d *MCPDetector) SetMetaAdapters(byHost map[string]MetaAdapter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.adapters = byHost
}

func (d *MCPDetector) adapterFor(peerHost string) (search map[string]bool, dispatch map[string]DispatchTool) {
	search, dispatch = map[string]bool{}, map[string]DispatchTool{}
	add := func(a MetaAdapter) {
		for _, n := range a.SearchTools {
			search[n] = true
		}
		for _, t := range a.DispatchTools {
			dispatch[t.Name] = t
		}
	}
	add(bakedAdapter)
	d.mu.Lock()
	if a, ok := d.adapters[peerHost]; ok {
		add(a)
	}
	d.mu.Unlock()
	return search, dispatch
}

// searchedTool is one tool definition learned from a search result.
type searchedTool struct {
	def        contract.ToolDef
	contract   *contract.Contract
	observedAt string
}

// ingestSearchResult records the tool definitions a search result carried and
// returns a definition_change for every tool RE-observed with a different
// definition. First sight of a tool is a baseline, never a finding; absence
// from a later result is never a removal.
func (d *MCPDetector) ingestSearchResult(call model.RedactedCall) []model.Finding {
	defs := searchResultTools(call.ResponseBody)
	if len(defs) == 0 {
		return nil
	}
	edge := mcpEdgeRef(call.PeerHost, call.Direction)
	observedAt := call.CapturedAt
	if observedAt == "" {
		observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	var findings []model.Finding
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
		st.searched[def.Name] = &searchedTool{def: def, contract: c, observedAt: observedAt}
		d.mu.Unlock()
		if prev == nil || prev.contract.Version.ContentHash == c.Version.ContentHash {
			continue
		}
		for _, ch := range diff.Reportable(diff.Classify(prev.contract, c)) {
			if ch.Rule == diff.RuleOperationRemoved {
				continue // unreachable for a single re-observed tool; never a removal from a search page
			}
			f := definitionChangeFinding(call.Integration, ch, prev.contract, c, now)
			f.Source, f.Completeness = "search_result", "partial"
			f.Signature = f.ComputeSignature()
			findings = append(findings, f)
		}
	}
	return findings
}

// searchedOpAt is the search-learned contract for a tool on an edge and when
// it was observed; nil when no recorded search result named that tool.
func (d *MCPDetector) searchedOpAt(peerHost, direction, tool string) (*contract.Operation, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.edges[mcpEdgeRef(peerHost, direction)]
	if st == nil || st.searched == nil {
		return nil, ""
	}
	s := st.searched[tool]
	if s == nil {
		return nil, ""
	}
	return s.contract.Op(tool), s.observedAt
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
