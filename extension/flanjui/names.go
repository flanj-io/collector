package flanjui

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/flanj-io/collector/internal/store"
)

// Edge display names (v1 phase 1 — edge naming): human names for discovered
// OUTBOUND edges, keyed by the registrable domain (internal/edge.RegistrableDomain)
// and persisted in the store's settings KV so every pod of a deployment agrees
// and a name survives a restart. Never a per-pod file.
//
// Storage follows the acks precedent exactly (acks.go): one record per domain
// under `edge.name.<registrable_domain>`, plus the index `edge.names` — a JSON
// array of named domains. Every write is a single blind PutSetting; the KV has
// no delete, so removing a name is a tombstone (empty value) + index removal,
// and the index mutation carries the same bounded re-read-verify retry as the
// ack index (see indexWriteAttempts for the residual-race honesty).
//
// Only `user` names are STORED here — a name the operator typed. The other
// three tiers are resolved at read time (directory.go) and never persisted:
// `contract` (the title of the contract uploaded for this domain), `directory`
// and `auto`.

const (
	// settingEdgeNamePrefix + <registrable_domain> → edgeNameRecord JSON.
	settingEdgeNamePrefix = "edge.name."
	// settingEdgeNamesIndex → JSON array of named registrable domains.
	settingEdgeNamesIndex = "edge.names"

	nameSourceUser = "user"
	// nameSourceContract is the provider's OWN words for their API, read out of
	// the contract uploaded for this domain (`info.title`).
	//
	// It replaces the `config` tier, which died with `spec_path`: the legacy
	// `provider_display_name` names a provider but nothing said WHICH edge it
	// meant — that linkage came from the config spec's `peer_host`, and there
	// are no config specs any more. The upload carries both facts at once (the
	// host it binds to, and the document's title), so the name is derived from
	// what the operator actually did rather than guessed from a YAML value.
	nameSourceContract  = "contract"
	nameSourceDirectory = "directory"
	nameSourceAuto      = "auto"
)

// edgeNameRecord is one stored name. It never leaves this collector except as
// the resolved display_name on the local read API — and, only on an explicit
// per-mapping opt-in, as a directory suggestion.
type edgeNameRecord struct {
	Name      string `json:"name"`
	Source    string `json:"source"` // "user" — the only stored tier
	UpdatedAt string `json:"updated_at"`
}

func loadEdgeNameIndex(st store.Store) ([]string, error) {
	raw, ok, err := st.GetSetting(settingEdgeNamesIndex)
	if err != nil || !ok || raw == "" {
		return nil, err
	}
	var domains []string
	if err := json.Unmarshal([]byte(raw), &domains); err != nil {
		return nil, nil // a corrupt index is treated as empty; records stay reachable by domain
	}
	return domains, nil
}

// errEdgeNameIndexRace mirrors errAckIndexRace: the record itself is persisted,
// only the index entry may be missing after every attempt lost the race.
var errEdgeNameIndexRace = errors.New("edge.names: concurrent writer won every attempt; record saved, index entry missing")

// mutateEdgeNameIndex adds or removes one domain in edge.names with the same
// re-read-before-write + verify retry as the ack index (acks.go).
func mutateEdgeNameIndex(st store.Store, domain string, add bool) error {
	var lastErr error
	for attempt := 0; attempt < indexWriteAttempts; attempt++ {
		domains, err := loadEdgeNameIndex(st) // fresh read right before the write
		if err != nil {
			return err
		}
		if containsID(domains, domain) == add {
			return nil
		}
		next := make([]string, 0, len(domains)+1)
		for _, d := range domains {
			if d != domain {
				next = append(next, d)
			}
		}
		if add {
			next = append(next, domain)
		}
		nb, _ := json.Marshal(next)
		if err := st.PutSetting(settingEdgeNamesIndex, string(nb)); err != nil {
			return err
		}
		after, err := loadEdgeNameIndex(st)
		if err != nil {
			return err
		}
		if containsID(after, domain) == add {
			return nil
		}
		lastErr = errEdgeNameIndexRace
	}
	return lastErr
}

// loadEdgeNames returns every stored name by registrable domain (index walk).
// Tombstoned (empty) and unreadable records are skipped — the row simply
// resolves at the next precedence tier.
func loadEdgeNames(st store.Store) (map[string]edgeNameRecord, error) {
	domains, err := loadEdgeNameIndex(st)
	if err != nil {
		return nil, err
	}
	out := make(map[string]edgeNameRecord, len(domains))
	for _, d := range domains {
		raw, ok, err := st.GetSetting(settingEdgeNamePrefix + d)
		if err != nil || !ok || raw == "" {
			continue
		}
		var rec edgeNameRecord
		if json.Unmarshal([]byte(raw), &rec) != nil || rec.Name == "" {
			continue
		}
		out[d] = rec
	}
	return out, nil
}

// putEdgeName persists one name: the record first (a single blind put), then
// the idempotent index add.
func putEdgeName(st store.Store, domain, name, source string) error {
	rec := edgeNameRecord{
		Name:      name,
		Source:    source,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(rec)
	if err := st.PutSetting(settingEdgeNamePrefix+domain, string(b)); err != nil {
		return err
	}
	return mutateEdgeNameIndex(st, domain, true)
}

// deleteEdgeName removes a name: tombstone (the KV has no delete) + index
// removal — the acks precedent. The row then resolves at the next tier down.
func deleteEdgeName(st store.Store, domain string) error {
	if err := st.PutSetting(settingEdgeNamePrefix+domain, ""); err != nil {
		return err
	}
	return mutateEdgeNameIndex(st, domain, false)
}
