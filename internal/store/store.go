// Package store is the collector's call/finding/edge store — the rolling
// evidence window written by the store exporter, read by the UI extension, and
// stamped with contract metadata by the drift processor. The viniferastore
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
	"fmt"

	"github.com/vinifera-io/collector/internal/edge"
	"github.com/vinifera-io/collector/internal/model"
)

// evictBatchMax caps how many rows a single eviction DELETE may remove. Steady
// state evicts 1 row per insert; the batch only matters when catching up on a
// burst (lowered caps, post-migration fill, a pod resuming after another held
// the eviction lock).
const evictBatchMax = 256

// Store is the backend-agnostic store surface. Both backends satisfy it; the
// viniferastore extension picks the implementation from its config.
type Store interface {
	// InsertCall stores a RedactedCall (idempotent on id), discovers/updates the
	// edge it belongs to, and then runs eviction.
	InsertCall(c model.RedactedCall) error
	// InsertFinding stores a Finding, deduped by signature (a drift is
	// per-endpoint, not per-call — CONTRACTS §4). The first occurrence creates
	// the finding and pins its source call; repeats only increment
	// occurrence_count/last_seen. The finding id stays the FIRST occurrence's id
	// (the flag idempotency key depends on it).
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
	ListSpecInfos() ([]model.SpecInfo, error)
	GetSpecDoc(integration string) (raw []byte, format string, ok bool, err error)
	Stats() (rows int, bytes int64, err error)
	Counts() (calls int, findings int, err error)
	Close() error
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
	var doc string
	err := b.db.QueryRow(b.rebind(`SELECT doc FROM calls WHERE id=?`), id).Scan(&doc)
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
func (b *base) ListCalls(limit int) ([]model.RedactedCall, error) {
	return listDocs[model.RedactedCall](b, `SELECT doc FROM calls ORDER BY seq DESC LIMIT ?`, limit)
}

// ListFindings returns up to limit most-recent findings, newest first.
func (b *base) ListFindings(limit int) ([]model.Finding, error) {
	if limit <= 0 {
		limit = 100
	}
	return b.scanFindings(`SELECT doc, occurrence_count, last_seen FROM findings ORDER BY seq DESC LIMIT ?`, limit)
}

// scanFindings runs a query selecting (doc, occurrence_count, last_seen) rows
// and patches the two column-authoritative counters into the unmarshalled doc.
// The doc stays frozen as the FIRST occurrence's JSON (keeping the finding id —
// and thus the flag idempotency key — stable), while dedup advances the
// counters atomically in their columns.
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
		        COALESCE(version,''), COALESCE(docs_url,''), endpoints, loaded_at
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
			&si.Version, &si.DocsURL, &si.Endpoints, &si.LoadedAt); err != nil {
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
