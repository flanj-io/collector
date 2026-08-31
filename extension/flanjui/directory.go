package flanjui

// The edge-name directory + resolution (v1 phase 1 — edge naming).
//
// Resolution precedence for an OUTBOUND edge's display name, first hit wins
// (brief-common "The model"): named-by-you (settings KV, source `user`) >
// config (the two legacy YAML keys, migrated on boot as source `config`) >
// directory (a periodically pulled full table merged OVER the baked seed) >
// auto (no name; the UI renders the humanized host).
//
// The directory is NEVER queried per cache miss — permanently rejected: the
// local UI must work with the CP down, and the CP must never receive the org's
// dependency graph edge-by-edge. The baked seed resolves offline pre-Connect;
// once a collector key exists the sync ticker (sync.go) refreshes the full
// table with a conditional GET (ETag / If-None-Match) and stores the raw JSON
// in the KV with one blind put.
//
// The refresh SHARES that ticker with the findings sync but has its OWN switch,
// `display_name_sync` (CONTRACTS §8, default true — owner ruling 2026-08-31):
// this leg is a pure fetch (nothing about this collector's edges leaves), so it
// does not answer to `finding_sync`, which governs an egress. With
// `display_name_sync: false` the pull never runs — but note it does not CLEAR
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
// (never per edge): the stored names, the merged directory, and the single
// domain the legacy config keys are linked to (if derivable).
type nameResolver struct {
	names        map[string]edgeNameRecord
	directory    map[string]directoryEntry
	configDomain string
	configName   string
}

// newNameResolver loads the resolution context from the store.
func (e *uiExtension) newNameResolver(st store.Store) nameResolver {
	names, _ := loadEdgeNames(st)
	if names == nil {
		names = map[string]edgeNameRecord{}
	}
	r := nameResolver{names: names, directory: loadDirectory(st)}
	if e.cfg.ProviderDisplayName != "" {
		// The config-tier name passes the redaction floor at READ time exactly
		// as the boot migration does before persist (migrateConfigNames), so the
		// read-time and migrated outputs are byte-identical. A value the floor
		// consumes entirely resolves nowhere — same as the migration's skip.
		if name := strings.TrimSpace(redact.New().Redact(e.cfg.ProviderDisplayName).Text); name != "" {
			r.configDomain = configEdgeDomain(st, e.cfg.IntegrationID)
			r.configName = name
		}
	}
	return r
}

// resolve returns (display name, source) for a registrable domain — the
// precedence chain. An empty name with source "auto" means unnamed: the UI
// humanizes the host itself.
func (r nameResolver) resolve(domain string) (string, string) {
	if domain == "" {
		return "", nameSourceAuto
	}
	if rec, ok := r.names[domain]; ok {
		return rec.Name, rec.Source
	}
	// Read-time config fallback: the precedence output is identical whether or
	// not the boot migration ran (it may not be derivable — see
	// migrateConfigNames), because both use the same configEdgeDomain linkage.
	if r.configName != "" && domain == r.configDomain {
		return r.configName, nameSourceConfig
	}
	if entry, ok := r.directory[domain]; ok && entry.Name != "" {
		return entry.Name, nameSourceDirectory
	}
	return "", nameSourceAuto
}

// configEdgeDomain derives the ONE registrable domain the legacy
// `provider_display_name` config key names: the singular v0 model gives the
// configured integration one spec, whose spec_infos row carries the peer_host
// that scopes it. When no such row exists (no spec loaded, or a spec with no
// peer_host scoping), the linkage is NOT derivable and this returns "" — the
// config tier then resolves nowhere, both here and in the boot migration,
// which keeps the two paths equivalent by construction.
func configEdgeDomain(st store.Store, integrationID string) string {
	if integrationID == "" {
		return ""
	}
	infos, err := st.ListSpecInfos()
	if err != nil {
		return ""
	}
	for _, si := range infos {
		if si.Integration == integrationID && si.Role != "self" && si.PeerHost != "" {
			return edge.RegistrableDomain(si.PeerHost)
		}
	}
	return ""
}

// migrateConfigNames is the C4 boot migration: the legacy
// `provider_display_name` YAML value moves into the KV as a source `config`
// record, keyed by the drift-target edge's registrable domain. Idempotent and
// safe to re-run: a `user` record is NEVER overwritten; a re-run refreshes the
// `config` record when the YAML value changed. When the config→edge linkage is
// not derivable from existing store data, nothing is invented and the
// migration is skipped — the config tier still resolves identically at read
// time (resolve above).
func (e *uiExtension) migrateConfigNames(st store.Store) {
	if e.cfg.ProviderDisplayName == "" {
		return
	}
	domain := configEdgeDomain(st, e.cfg.IntegrationID)
	if domain == "" {
		return
	}
	// The migrated value passes the same redaction floor a UI rename (and a
	// Connect display name) passes before persist.
	name := strings.TrimSpace(redact.New().Redact(e.cfg.ProviderDisplayName).Text)
	if name == "" {
		return
	}
	names, err := loadEdgeNames(st)
	if err != nil {
		return
	}
	if rec, ok := names[domain]; ok {
		if rec.Source == nameSourceUser {
			return // a UI rename always beats a stale YAML value
		}
		if rec.Name == name {
			return // already migrated, unchanged
		}
	}
	if err := putEdgeName(st, domain, name, nameSourceConfig); err != nil {
		e.telemetry.Logger.Warn("edge naming: config migration write failed: " + err.Error())
	}
}

// maybeMigrateNames runs the boot migration exactly once per process, the
// first time the store resolves (extensions start in any order, so Start
// cannot assume the store is up — resolveStore calls this on first success).
func (e *uiExtension) maybeMigrateNames(st store.Store) {
	e.migrateOnce.Do(func() { e.migrateConfigNames(st) })
}

// syncDirectoryOnce is one directory pull, riding the sync ticker after the
// findings tick (same cadence, same skip conditions, all silent): a configured
// CP and a collector key, or nothing happens. The caller (sync.go) gates this
// leg on `display_name_sync` alone. Never on a cache miss, never per edge.
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
