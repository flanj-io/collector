package flanjui

// The edge-name directory + resolution (v1 phase 1 — edge naming).
//
// Resolution precedence for an OUTBOUND edge's display name, first hit wins:
// named-by-you (settings KV, source `user`) > contract (the title of the
// contract UPLOADED for this domain — the provider's own words) > directory (a
// periodically pulled full table merged OVER the baked seed) > auto (no name;
// the UI renders the humanized host).
//
// The `config` tier was removed with `spec_path` (CONTRACTS §8, 2026-08-31).
// The legacy `provider_display_name` names a provider, but nothing said WHICH
// edge it meant — that linkage came from the config spec's `peer_host`, and
// there are no config specs any more. The upload carries both facts at once, so
// the tier that replaces it derives its name from what the operator actually
// did. `provider_display_name` itself stays: it is still the fallback provider
// name sent on a flag, which needs no edge linkage at all.
//
// The directory is NEVER queried per cache miss — permanently rejected: the
// local UI must work with the CP down, and the CP must never receive the org's
// dependency graph edge-by-edge. The baked seed resolves offline pre-Connect;
// once a collector key exists the sync ticker (sync.go) refreshes the full
// table with a conditional GET (ETag / If-None-Match) and stores the raw JSON
// in the KV with one blind put.
//
// The refresh SHARES that ticker with the findings sync but has its OWN switch,
// `directory_sync` (CONTRACTS §8, default true — owner ruling 2026-08-31):
// this leg is a pure fetch (nothing about this collector's edges leaves), so it
// does not answer to `finding_sync`, which governs an egress. With
// `directory_sync: false` the pull never runs — but note it does not CLEAR
// a table pulled earlier: loadDirectory keeps merging the stored table over the
// seed, so a previously-connected collector goes on serving those names (frozen,
// and going stale) until the store is reset. Turning the switch off stops future
// fetches, not past ones.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/redact"
	"github.com/flanj-io/collector/internal/store"
)

const (
	// settingDirectoryTable holds the last pulled full directory table — the raw
	// JSON body of GET /api/v1/directory, stored with one blind put.
	settingDirectoryTable = "directory.table"
	// settingDirectoryETag holds the ETag of the stored table for the
	// conditional re-fetch (If-None-Match → 304 → no-op).
	settingDirectoryETag = "directory.etag"
)

// directoryEntry is one directory row: `{ "<registrable_domain>": { "name",
// "tier" } }` — the shape the seed file, the pull response and CP storage all
// agree on (brief-common shared vocabulary).
type directoryEntry struct {
	Name string `json:"name"`
	Tier string `json:"tier"` // curated | claimed | community
}

var (
	seedOnce   sync.Once
	seedParsed map[string]directoryEntry
)

// directorySeed returns the parsed baked seed. An unreadable seed is an empty
// table, never an error — the tier simply resolves nothing.
func directorySeed() map[string]directoryEntry {
	seedOnce.Do(func() {
		seedParsed = map[string]directoryEntry{}
		_ = json.Unmarshal(directorySeedRaw, &seedParsed)
	})
	return seedParsed
}

// directoryEnvelope is the CP's GET /api/v1/directory response body (§5.14):
// `{"entries": {"<domain>": {"name","tier"}}, "count": n}` — NOT a bare map.
// Only "entries" is read; unknown sibling fields (like "count") are tolerated
// by construction (encoding/json ignores unknown keys).
type directoryEnvelope struct {
	Entries map[string]directoryEntry `json:"entries"`
}

// parseDirectoryTable decodes a stored pull body — the §5.14 ENVELOPE.
//
// DECISION: the KV (`directory.table`) stores the RAW envelope, byte-for-byte
// as the CP sent it — the pull path (syncDirectoryOnce + promote.GetDirectory)
// stays a blind pass-through, and a future envelope field survives the
// round-trip. The unwrap to the entries map happens HERE, at read time. Bad
// JSON, or an envelope without "entries", is an empty table.
func parseDirectoryTable(raw string) map[string]directoryEntry {
	var env directoryEnvelope
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &env)
	}
	if env.Entries == nil {
		return map[string]directoryEntry{}
	}
	return env.Entries
}

// loadDirectory returns the directory-tier lookup: the baked seed with the
// pulled table merged OVER it (a pulled entry wins over a baked one).
func loadDirectory(st store.Store) map[string]directoryEntry {
	seed := directorySeed()
	raw, ok, err := st.GetSetting(settingDirectoryTable)
	if err != nil || !ok || raw == "" {
		return seed
	}
	pulled := parseDirectoryTable(raw)
	if len(pulled) == 0 {
		return seed
	}
	out := make(map[string]directoryEntry, len(seed)+len(pulled))
	for d, e := range seed {
		out[d] = e
	}
	for d, e := range pulled {
		out[d] = e
	}
	return out
}

// nameResolver is one request's resolution context, loaded ONCE per request
// (never per edge): the stored user names, the uploaded contracts' titles, and
// the merged directory.
type nameResolver struct {
	names map[string]edgeNameRecord
	// contracts is the DOMAIN-wide name: one OpenAPI upload names every host
	// under the domain that has no contract of its own.
	contracts map[string]string // registrable domain → the contract's info.title
	// contractsByHost is what a host's OWN contract calls it, and it outranks
	// the domain-wide name. Carries MCP snapshots too — a tools/list may not
	// name a whole domain, but it is the provider's own word for ITS host.
	contractsByHost map[string]string // peer host → that contract's title
	directory       map[string]directoryEntry
}

// newNameResolver loads the resolution context from the store.
func (e *uiExtension) newNameResolver(st store.Store) nameResolver {
	names, _ := loadEdgeNames(st)
	if names == nil {
		names = map[string]edgeNameRecord{}
	}
	byDomain, byHost := contractEdgeNames(st)
	return nameResolver{
		names:           names,
		contracts:       byDomain,
		contractsByHost: byHost,
		directory:       loadDirectory(st),
	}
}

// contractEdgeNames maps a registrable domain to the title of the contract
// uploaded for a host under it.
//
// Naming is DOMAIN-level while contract binding is HOST-level, deliberately and
// for different reasons: subdomains routinely run different APIs (so a contract
// binds to exactly one host), while a name describes the organisation behind the
// domain (so `api.acme.test` and `api-eu.acme.test` read as the same provider).
// One upload therefore names the whole domain, which is the behaviour that makes
// a fifty-provider estate legible after fifty uploads.
//
// The title passes the redaction floor before it can render, exactly as a typed
// rename does — an uploaded document is operator-supplied text like any other.
// A title the floor consumes entirely resolves nowhere, same as an empty one.
func contractEdgeNames(st store.Store) (byDomain, byHost map[string]string) {
	infos, err := st.ListSpecInfos()
	if err != nil {
		return nil, nil
	}
	byDomain = make(map[string]string, len(infos))
	byHost = make(map[string]string, len(infos))
	// Pass one: what each host's OWN contract calls it. An MCP snapshot counts
	// here (see the domain exclusion below — it is barred from naming the
	// DOMAIN, never its own host), and an uploaded OpenAPI document outranks
	// one on the same host because the operator put it there deliberately.
	for _, si := range infos {
		if si.Role == model.SpecRoleSelf || si.PeerHost == "" || si.Title == "" {
			continue
		}
		name := strings.TrimSpace(redact.New().Redact(si.Title).Text)
		if name == "" {
			continue
		}
		if _, taken := byHost[si.PeerHost]; taken && si.Format != model.SpecFormatOpenAPI {
			continue
		}
		byHost[si.PeerHost] = name
	}
	for _, si := range infos {
		// UPLOADED REST contracts only. An MCP snapshot is a provider row with a
		// peer_host and a title too, but its title is the SERVER's name
		// (`acme-tools-mcp`), not the organisation's — and `mcp.acme.test` shares
		// a registrable domain with `api.acme.test`, so letting it through would
		// let an observed server name win the whole domain by nothing more than
		// which row sorted first. Naming is what the operator DID; a tools/list
		// snapshot is something the traffic delivered.
		if si.Format != model.SpecFormatOpenAPI || si.Role == model.SpecRoleSelf ||
			si.PeerHost == "" || si.Title == "" {
			continue
		}
		domain := edge.RegistrableDomain(si.PeerHost)
		if domain == "" {
			continue
		}
		name := strings.TrimSpace(redact.New().Redact(si.Title).Text)
		if name == "" {
			continue
		}
		// First writer wins so the map is stable: two hosts under one domain
		// with different contracts would otherwise flip the name by map order.
		if _, taken := byDomain[domain]; !taken {
			byDomain[domain] = name
		}
	}
	return byDomain, byHost
}

// resolve returns (display name, source) for a registrable domain — the
// precedence chain. An empty name with source "auto" means unnamed: the UI
// humanizes the host itself.
func (r nameResolver) resolve(host, domain string) (string, string) {
	if domain == "" {
		return "", nameSourceAuto
	}
	if rec, ok := r.names[domain]; ok {
		return rec.Name, rec.Source
	}
	// The contract the operator uploaded for this domain names it in the
	// provider's own words. Above `directory` because it is what THIS operator
	// put there for THIS edge, and the curated table is a general fact about the
	// domain; below `user` because a rename is the more specific act.
	//
	// A host that has a contract of its OWN is named by that one first. The
	// domain-wide rule spares an operator fifty renames; it was never meant to
	// overrule the provider's own words about a specific host — which is exactly
	// what it did to an MCP server sharing a domain with an uploaded REST spec.
	if name, ok := r.contractsByHost[host]; ok && name != "" {
		return name, nameSourceContract
	}
	if name, ok := r.contracts[domain]; ok && name != "" {
		return name, nameSourceContract
	}
	if entry, ok := r.directory[domain]; ok && entry.Name != "" {
		return entry.Name, nameSourceDirectory
	}
	return "", nameSourceAuto
}

// syncDirectoryOnce is one directory pull, riding the sync ticker after the
// findings tick (same cadence, same skip conditions, all silent): a configured
// CP and a collector key, or nothing happens. The caller (sync.go) gates this
// leg on `directory_sync` alone. Never on a cache miss, never per edge.
// 304 → no-op; 200 → one blind put of the raw body + the new ETag.
func (e *uiExtension) syncDirectoryOnce(ctx context.Context) {
	if e.cp == nil {
		return
	}
	st := e.resolveStore()
	if st == nil {
		return
	}
	cs, err := loadConnect(st)
	if err != nil || cs.CollectorKey == "" {
		return
	}
	etag, _, _ := st.GetSetting(settingDirectoryETag)
	body, newETag, status, err := e.keyedClient(cs).GetDirectory(ctx, etag)
	if err != nil {
		// Status only — never the bearer, never the body.
		e.telemetry.Logger.Debug(fmt.Sprintf("directory sync: pull failed with status %d — retrying next tick", status))
		return
	}
	if status == 304 {
		return
	}
	if err := st.PutSetting(settingDirectoryTable, string(body)); err != nil {
		return
	}
	if newETag != "" {
		_ = st.PutSetting(settingDirectoryETag, newETag)
	}
	e.telemetry.Logger.Debug("directory sync: table refreshed (status 200)")
}
