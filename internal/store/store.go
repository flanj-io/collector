// Package store is the collector's call/finding/edge store — the rolling
// evidence window written by the store exporter, read by the UI extension, and
// stamped with contract metadata by the drift processor. The flanjstore
// extension owns the single in-process handle; every other component reaches it
// via Provider over host.GetExtensions() (collector CLAUDE.md non-negotiable #1).
//
// Two backends implement Store:
//   - sqlite (default): embedded modernc.org/sqlite (pure Go, CGO off), WAL
//     journal, one file on a PVC. Exactly ONE collector pod may own a given
//     file — cross-process access is not supported.
//   - postgres: a shared external database. N collector pods may write to it
//     concurrently; cross-pod safety (finding dedup, pinning, eviction) is
//     this package's job, never the callers'.
//
// Rolling window (ring buffer): after every insert, FIFO-evict the oldest
// pinned=0 rows until the row-count and byte-size caps are satisfied. This gives
// a stable fill level for un-referenced traffic. Two rules protect evidence:
//   - pin-on-finding: a call referenced by a finding is pinned (pinned=1) so the
//     failing call stays reproducible.
//   - evict-after-promote: once a pinned call is flagged to the control plane it
//     is unpinned and stamped promoted_at, so it re-enters the eviction pool.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
)

// evictBatchMax caps how many rows a single eviction DELETE may remove. Steady
// state evicts 1 row per insert; the batch only matters when catching up on a
// burst (lowered caps, post-migration fill, a pod resuming after another held
// the eviction lock).
const evictBatchMax = 256

// Store is the backend-agnostic store surface. Both backends satisfy it; the
// flanjstore extension picks the implementation from its config.
type Store interface {
	// InsertCall stores a RedactedCall (idempotent on id), discovers/updates the
	// edge it belongs to, and then runs eviction.
	InsertCall(c model.RedactedCall) error
	// InsertFinding stores a Finding, deduped by signature (a drift is
	// per-endpoint, not per-call — CONTRACTS §4). The first occurrence creates
	// the finding and pins its source call; repeats only increment
	// occurrence_count/last_seen. The finding id stays the FIRST occurrence's id
	// (the flag idempotency key depends on it).
	//
	// ONE exception to the frozen doc: a definition_change whose EVIDENCE has
	// moved on — see refreshedFindingDoc.
	InsertFinding(f model.Finding) error
	// MarkPromoted implements evict-after-promote: unpin + stamp promoted_at.
	MarkPromoted(id string) error
	GetCall(id string) (model.RedactedCall, bool, error)
	GetFinding(id string) (model.Finding, bool, error)
	ListCalls(limit int) ([]model.RedactedCall, error)
	ListFindings(limit int) ([]model.Finding, error)
	ListEdges(externalOnly bool) ([]model.Edge, error)
	EdgeCallCountsSince(sinceISO string) (map[string]int, error)
	PutSpecInfo(info model.SpecInfo, rawSpec []byte) error
	// PutUploadedSpec is the UPLOAD path's write. It differs from PutSpecInfo
	// in one way that matters: replacing a bound contract rotates the document
	// it displaces into prev_doc rather than dropping it, and returns it, so
	// the caller can diff v1 -> v2 and the card can read "replaced v1.0.0".
	// Exactly one previous document is kept — no archive.
	//
	// Rotation and write are one transaction: a replace that half-applied would
	// leave a host either validating nothing or validating against a document
	// whose recorded metadata describes a different one.
	PutUploadedSpec(info model.SpecInfo, rawSpec []byte) (prev UploadedSpecPrevious, err error)
	// DeleteSpecInfo removes a contract. Remove ships with upload: a contract
	// bound to the wrong host with no undo is worse than no contract.
	DeleteSpecInfo(integration string) (existed bool, err error)
	ListSpecInfos() ([]model.SpecInfo, error)
	GetSpecDoc(integration string) (raw []byte, format string, ok bool, err error)
	Stats() (rows int, bytes int64, err error)
	Counts() (calls int, findings int, err error)
	// GetSetting / PutSetting: a tiny per-DEPLOYMENT key/value store for
	// collector-level state that must outlive a pod and be shared by every pod of
	// a deployment (e.g. the Connect registration key + confirmed contact). Values
	// are opaque strings (JSON-encode structs). Lives in the store — never a
	// per-pod file — so it exists exactly where the evidence does: the single
	// sqlite file, the shared postgres database, or the tiered store pod. Carried
	// by the sqlite→postgres import.
	GetSetting(key string) (value string, ok bool, err error)
	PutSetting(key, value string) error
	Close() error
}

// UploadedSpecPrevious describes the contract an upload displaced, empty when
// the upload was the first for that host.
type UploadedSpecPrevious struct {
	// Existed distinguishes a first upload from a replace. A replace with no
	// version string is still a replace.
	Existed bool
	Raw     []byte
	Version string
	// LoadedAt is when the displaced document was itself uploaded.
	LoadedAt string
}

// Provider is implemented by the store extension. The exporter, UI extension,
// and drift processor discover the store by type-asserting a host extension to
// this interface — it is the single sanctioned way to reach the shared handle.
type Provider interface {
	Store() Store
}

// base holds what both backends share: the connection pool and the read path.
// Queries are written with `?` placeholders; rebind converts them to the
// backend's native style ($1..$n for postgres, identity for sqlite).
type base struct {
	db     *sql.DB
	rebind func(string) string
}

// GetSetting returns the value stored under key, ok=false when absent.
func (b *base) GetSetting(key string) (string, bool, error) {
	var v string
	err := b.db.QueryRow(b.rebind(`SELECT value FROM settings WHERE key=?`), key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get setting %q: %w", key, err)
	}
	return v, true, nil
}

// PutSetting upserts key=value (single atomic statement on both backends).
func (b *base) PutSetting(key, value string) error {
	if key == "" {
		return errors.New("put setting: empty key")
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if _, err := b.db.Exec(b.rebind(
		`INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`),
		key, value, now,
	); err != nil {
		return fmt.Errorf("put setting %q: %w", key, err)
	}
	return nil
}

// execer is the subset of *sql.DB / *sql.Tx the shared write helpers need.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// latePin closes the "finding before its call" gap. InsertFinding pins its
// source call and attributes the drift to the call's edge only if the call row
// exists at that moment; a finding that arrives first (cross-request reordering
// between a front collector and the store pod, a partially applied batch that
// is re-delivered, a call re-sent after eviction) would otherwise leave its
// evidence unpinned forever, because repeat occurrences never re-pin. So when a
// genuinely NEW call row lands (RowsAffected>0 — an existing row was pinned
// when its finding came, or was promoted and must not be re-pinned), pin it now
// if any finding already references it and repair the edge drift_count that
// was silently skipped.
//
// Callers invoke it after the edge upsert and under their backend's
// serialisation against InsertFinding: the store mutex on sqlite; the insert tx
// holding the per-call advisory lock on postgres (see pgLockNSCallPin). The
// store is thereby order-independent for call/finding pairs.
func latePin(ex execer, rebind func(string) string, c model.RedactedCall) (pinned bool, err error) {
	res, err := ex.Exec(rebind(
		`UPDATE calls SET pinned=1
		  WHERE id=? AND pinned=0
		    AND EXISTS (SELECT 1 FROM findings WHERE source_call_id=?)`),
		c.ID, c.ID,
	)
	if err != nil {
		return false, fmt.Errorf("late pin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if c.PeerHost != "" {
		// One bump per finding that references this call — exactly what
		// bumpEdgeDrift would have done had the call been present.
		if _, err := ex.Exec(rebind(
			`UPDATE edges
			    SET drift_count = drift_count + (SELECT COUNT(*) FROM findings WHERE source_call_id=?)
			  WHERE peer_host=? AND direction=?`),
			c.ID, c.PeerHost, c.Direction,
		); err != nil {
			return false, fmt.Errorf("late pin: repair edge drift: %w", err)
		}
	}
	return true, nil
}

// rebindIdentity leaves `?` placeholders untouched (sqlite).
func rebindIdentity(q string) string { return q }

// rebindDollar rewrites `?` placeholders to `$1..$n` (postgres), skipping
// quoted literals.
func rebindDollar(q string) string {
	var out []byte
	n := 0
	inStr := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inStr = !inStr
			out = append(out, c)
		case c == '?' && !inStr:
			n++
			out = append(out, '$')
			out = appendInt(out, n)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}

// Close closes the underlying connection pool.
func (b *base) Close() error { return b.db.Close() }

// GetCall returns the stored RedactedCall for id.
func (b *base) GetCall(id string) (model.RedactedCall, bool, error) {
	var (
		doc     string
		drifted bool
	)
	// `drifted` is store-owned (set when the call produced a finding, including
	// repeat occurrences), so it lives in its column and reads patch it back in
	// — the same shape as findings' occurrence_count/last_seen.
	err := b.db.QueryRow(b.rebind(`SELECT doc, drifted FROM calls WHERE id=?`), id).Scan(&doc, &drifted)
	if err == sql.ErrNoRows {
		return model.RedactedCall{}, false, nil
	}
	if err != nil {
		return model.RedactedCall{}, false, err
	}
	var c model.RedactedCall
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		return model.RedactedCall{}, false, err
	}
	c.Drifted = drifted
	return c, true, nil
}

// GetFinding returns the stored Finding for id.
func (b *base) GetFinding(id string) (model.Finding, bool, error) {
	fs, err := b.scanFindings(`SELECT doc, occurrence_count, last_seen FROM findings WHERE id=?`, id)
	if err != nil {
		return model.Finding{}, false, err
	}
	if len(fs) == 0 {
		return model.Finding{}, false, nil
	}
	return fs[0], true, nil
}

// ListCalls returns up to limit most-recent calls, newest first.
//
// `drifted` is store-owned and patched back in from its column: DRIFT IS A
// PROPERTY OF THIS CALL, not of its endpoint. Reading it any coarser marked
// every call on a drifted endpoint as drifted — conforming ones, and ones
// captured before the drift existed.
func (b *base) ListCalls(limit int) ([]model.RedactedCall, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := b.db.Query(b.rebind(`SELECT doc, drifted FROM calls ORDER BY seq DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.RedactedCall, 0, limit)
	for rows.Next() {
		var (
			doc     string
			drifted bool
		)
		if err := rows.Scan(&doc, &drifted); err != nil {
			return nil, err
		}
		var c model.RedactedCall
		if err := json.Unmarshal([]byte(doc), &c); err != nil {
			return nil, err
		}
		c.Drifted = drifted
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListFindings returns up to limit most-recent findings, newest first.
func (b *base) ListFindings(limit int) ([]model.Finding, error) {
	if limit <= 0 {
		limit = 100
	}
	return b.scanFindings(`SELECT doc, occurrence_count, last_seen FROM findings ORDER BY seq DESC LIMIT ?`, limit)
}

// findingEvidenceVersion is the content hash a finding's EVIDENCE is bound to:
// the AFTER snapshot hash of a definition_change — the same hash the UI renders
// as "AFTER (snapshot sha256:…)" and that a local acknowledgement keys on
// (ux-design-v2 §2.8). Empty for every other kind, whose evidence is a call and
// whose recurrence is counted rather than re-evidenced.
func findingEvidenceVersion(f model.Finding) string {
	if f.Kind != model.KindDefinitionChange || f.SpecVersionTo == nil {
		return ""
	}
	return *f.SpecVersionTo
}

// evidenceOrder is the sortable stamp of a finding's evidence: the AFTER
// snapshot's observed-at, falling back to when the drift was detected.
func evidenceOrder(f model.Finding) string {
	if f.SnapshotObservedAt != "" {
		return f.SnapshotObservedAt
	}
	return f.DetectedAt
}

// refreshedFindingDoc decides whether a REPEAT occurrence must replace the
// stored doc instead of leaving it frozen, and returns the replacement JSON.
//
// definition_change is the one kind that needs this. Its signature
// (integration|endpoint|kind|rule|field_path) is STABLE across successive
// changes to the same field, so a second change to the same tool description
// dedups onto the row the FIRST change created. Leaving that first doc in place
// has two consequences, both wrong:
//
//   - spec_version_to would never advance, so an acknowledgement keyed on the
//     evidence version (ux-design-v2 §2.8) could never stop matching: the
//     second change would render silently PRE-acknowledged, which is exactly
//     the hole the evidence-version key exists to close.
//   - a flag on the row would disclose the FIRST change's two definition
//     fragments, snapshot hashes and observed-at times to the provider as
//     "your own published text" — the wrong evidence, on the one claim the
//     amended evidence rule (§2.7.3) rests on.
//
// So when the after-hash moves FORWARD, the doc is rewritten with the new
// evidence and only the identity the rest of the system keys on is carried
// over: the finding id (the flag idempotency key is flag_<id>), the signature,
// first_seen, and a source call the new record does not name. A replay of the
// SAME change (same after-hash) is not a refresh — it falls through to the
// plain counter bump — and neither is an OLDER transition arriving late (a
// front collector replaying, a re-delivered batch), which must never revert the
// row to stale evidence.
func refreshedFindingDoc(storedDoc string, f model.Finding) (string, bool) {
	ev := findingEvidenceVersion(f)
	if ev == "" {
		return "", false
	}
	var stored model.Finding
	if err := json.Unmarshal([]byte(storedDoc), &stored); err != nil {
		return "", false // unreadable stored doc: leave it alone, just count
	}
	if findingEvidenceVersion(stored) == ev {
		return "", false
	}
	// Content hashes carry no order, so the AFTER snapshot's observed-at does
	// (ISO-8601 compares lexically). Only a STRICTLY OLDER record is refused —
	// an indeterminate comparison refreshes, because a stale doc is the failure
	// that silently pre-acknowledges a change nobody saw.
	if evidenceOrder(f) < evidenceOrder(stored) {
		return "", false
	}
	next := f
	next.ID = stored.ID
	next.Signature = stored.Signature
	if stored.FirstSeen != "" {
		next.FirstSeen = stored.FirstSeen
	}
	if next.SourceCallID == nil {
		next.SourceCallID = stored.SourceCallID
	}
	b, err := json.Marshal(next)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// scanFindings runs a query selecting (doc, occurrence_count, last_seen) rows
// and patches the two column-authoritative counters into the unmarshalled doc.
// The doc stays frozen as the FIRST occurrence's JSON (keeping the finding id —
// and thus the flag idempotency key — stable), while dedup advances the
// counters atomically in their columns. The single exception is a
// definition_change whose evidence has moved on, which InsertFinding rewrites
// in place while keeping that same id — see refreshedFindingDoc.
func (b *base) scanFindings(query string, args ...any) ([]model.Finding, error) {
	rows, err := b.db.Query(b.rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Finding, 0)
	for rows.Next() {
		var doc, lastSeen string
		var occ int
		if err := rows.Scan(&doc, &occ, &lastSeen); err != nil {
			return nil, err
		}
		var f model.Finding
		if err := json.Unmarshal([]byte(doc), &f); err != nil {
			return nil, err
		}
		f.OccurrenceCount = occ
		f.LastSeen = lastSeen
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListEdges returns discovered edges. When externalOnly is true, internal
// same-team edges are excluded (they are classified out of surfacing). Ordered
// by direction then most-recently-seen so outbound/inbound group naturally.
func (b *base) ListEdges(externalOnly bool) ([]model.Edge, error) {
	q := `SELECT peer_host, direction, role, class, first_seen, last_seen, call_count, drift_count FROM edges`
	if externalOnly {
		q += ` WHERE class = '` + edge.ClassExternal + `'`
	}
	q += ` ORDER BY direction ASC, last_seen DESC`
	rows, err := b.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Edge, 0)
	for rows.Next() {
		var e model.Edge
		if err := rows.Scan(&e.PeerHost, &e.Direction, &e.Role, &e.Class, &e.FirstSeen, &e.LastSeen, &e.CallCount, &e.DriftCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EdgeCallCountsSince returns the number of stored calls per edge — keyed
// "<peer_host>|<direction>" — captured at or after sinceISO. ISO-8601 UTC
// timestamps compare lexically, so plain string comparison is correct. Used by
// the UI's observed-RPM metric; bounded by the rolling window like everything
// else here.
func (b *base) EdgeCallCountsSince(sinceISO string) (map[string]int, error) {
	rows, err := b.db.Query(b.rebind(
		`SELECT peer_host, direction, COUNT(*) FROM calls
		  WHERE captured_at >= ? AND peer_host IS NOT NULL
		  GROUP BY peer_host, direction`), sinceISO,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var host, dir string
		var n int
		if err := rows.Scan(&host, &dir, &n); err != nil {
			return nil, err
		}
		out[host+"|"+dir] = n
	}
	return out, rows.Err()
}

// ListSpecInfos returns the loaded provider contracts (metadata only, no doc).
func (b *base) ListSpecInfos() ([]model.SpecInfo, error) {
	rows, err := b.db.Query(
		`SELECT integration, role, COALESCE(peer_host,''), format, COALESCE(title,''),
		        COALESCE(version,''), COALESCE(docs_url,''), endpoints, loaded_at,
		        COALESCE(source,'config'), COALESCE(prev_version,''), COALESCE(prev_loaded_at,'')
		   FROM spec_infos ORDER BY role DESC, integration ASC`, // self first
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.SpecInfo, 0)
	for rows.Next() {
		var si model.SpecInfo
		if err := rows.Scan(&si.Integration, &si.Role, &si.PeerHost, &si.Format, &si.Title,
			&si.Version, &si.DocsURL, &si.Endpoints, &si.LoadedAt,
			&si.Source, &si.PrevVersion, &si.PrevLoadedAt); err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// GetSpecDoc returns the raw contract document stored for an integration.
func (b *base) GetSpecDoc(integration string) (raw []byte, format string, ok bool, err error) {
	var doc string
	err = b.db.QueryRow(b.rebind(`SELECT doc, format FROM spec_infos WHERE integration=?`), integration).Scan(&doc, &format)
	if err == sql.ErrNoRows {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return []byte(doc), format, true, nil
}

// DeleteSpecInfo removes a contract and reports whether one was there.
func (b *base) DeleteSpecInfo(integration string) (bool, error) {
	res, err := b.db.Exec(b.rebind(`DELETE FROM spec_infos WHERE integration=?`), integration)
	if err != nil {
		return false, fmt.Errorf("delete spec info: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil // the driver cannot say; the row is gone either way
	}
	return n > 0, nil
}

// GetSpecPrevDoc returns the document this contract replaced, if one is kept.
// Its one use is the version diff; it is stored regardless so turning the diff
// UI on later is a switch rather than a migration.
func (b *base) GetSpecPrevDoc(integration string) (raw []byte, ok bool, err error) {
	var doc sql.NullString
	err = b.db.QueryRow(b.rebind(`SELECT prev_doc FROM spec_infos WHERE integration=?`), integration).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !doc.Valid || doc.String == "" {
		return nil, false, nil
	}
	return []byte(doc.String), true, nil
}

// readSpecForReplace loads what an upload is about to displace, inside the
// caller's transaction.
func readSpecForReplace(q interface {
	QueryRow(string, ...any) *sql.Row
}, rebind func(string) string, integration string) (UploadedSpecPrevious, error) {
	var (
		doc, version, loadedAt sql.NullString
		prev                   UploadedSpecPrevious
	)
	err := q.QueryRow(rebind(`SELECT doc, COALESCE(version,''), loaded_at FROM spec_infos WHERE integration=?`),
		integration).Scan(&doc, &version, &loadedAt)
	if err == sql.ErrNoRows {
		return prev, nil
	}
	if err != nil {
		return prev, err
	}
	prev.Existed = true
	prev.Raw = []byte(doc.String)
	prev.Version = version.String
	prev.LoadedAt = loadedAt.String
	return prev, nil
}

// listDocs is a package function (Go methods may not have type parameters).
func listDocs[T any](b *base, query string, limit int) ([]T, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := b.db.Query(b.rebind(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]T, 0, limit)
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Stats reports the current window fill (row count and byte size of calls).
func (b *base) Stats() (rows int, bytes int64, err error) {
	var bs sql.NullInt64
	err = b.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(byte_size),0) FROM calls`).Scan(&rows, &bs)
	return rows, bs.Int64, err
}

// Counts returns the number of calls and findings currently stored.
func (b *base) Counts() (calls int, findings int, err error) {
	if err = b.db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&calls); err != nil {
		return
	}
	err = b.db.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&findings)
	return
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
