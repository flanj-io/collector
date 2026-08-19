// Package store is the collector's single embedded SQLite store. It is the ONE
// owner of the database connection (CONTRACTS §3/§4): the store extension opens
// it, the exporter writes through it, and the UI extension reads through it —
// all sharing the same *Store via host.GetExtensions().
//
// Durability & shape:
//   - modernc.org/sqlite (pure Go, CGO off), WAL journal, on a PVC so data
//     survives a collector restart.
//   - Two tables: calls (RedactedCall) and findings (Finding), each storing the
//     canonical contract JSON plus indexed columns for querying and eviction.
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
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vinifera-io/collector/internal/edge"
	"github.com/vinifera-io/collector/internal/model"
)

// Provider is implemented by the store extension. The exporter and UI extension
// discover the store by type-asserting a host extension to this interface — it
// is the single sanctioned way to reach the shared connection.
type Provider interface {
	Store() *Store
}

// Store owns one SQLite connection and enforces the rolling window.
type Store struct {
	db       *sql.DB
	maxRows  int
	maxBytes int64
	mu       sync.Mutex // serialises insert+evict so the window stays consistent
}

// Open opens (creating if needed) the SQLite database at path, applies the
// schema, and returns the single owning Store. maxRows/maxBytes are the window
// ceilings (<=0 disables that dimension). The connection pool is pinned to one
// connection: with pure-Go SQLite this is the simplest correct concurrency model
// and matches "single owner".
func Open(path string, maxRows int, maxBytes int64) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db, maxRows: maxRows, maxBytes: maxBytes}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Store returns the receiver so *Store itself satisfies Provider (handy in tests
// and when the extension embeds a *Store directly).
func (s *Store) Store() *Store { return s }

// Close closes the underlying connection.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS calls (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  id           TEXT NOT NULL UNIQUE,
  captured_at  TEXT NOT NULL,
  integration  TEXT NOT NULL,
  peer_host    TEXT,
  direction    TEXT,
  edge_class   TEXT,
  method       TEXT NOT NULL,
  route        TEXT NOT NULL,
  status_code  INTEGER NOT NULL,
  request_id   TEXT,
  idem_key     TEXT,
  trace_id     TEXT,
  byte_size    INTEGER NOT NULL,
  pinned       INTEGER NOT NULL DEFAULT 0,
  promoted_at  TEXT,
  doc          TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS findings (
  seq              INTEGER PRIMARY KEY AUTOINCREMENT,
  id               TEXT NOT NULL UNIQUE,
  signature        TEXT NOT NULL UNIQUE,
  kind             TEXT NOT NULL,
  severity         TEXT NOT NULL,
  integration      TEXT NOT NULL,
  endpoint         TEXT NOT NULL,
  rule             TEXT NOT NULL,
  source_call_id   TEXT,
  occurrence_count INTEGER NOT NULL DEFAULT 1,
  first_seen       TEXT NOT NULL,
  last_seen        TEXT NOT NULL,
  detected_at      TEXT NOT NULL,
  doc              TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS edges (
  peer_host    TEXT NOT NULL,
  direction    TEXT NOT NULL,
  role         TEXT NOT NULL,
  class        TEXT NOT NULL,
  first_seen   TEXT NOT NULL,
  last_seen    TEXT NOT NULL,
  call_count   INTEGER NOT NULL DEFAULT 0,
  drift_count  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (peer_host, direction)
);
CREATE INDEX IF NOT EXISTS idx_calls_pinned_seq ON calls(pinned, seq);
CREATE INDEX IF NOT EXISTS idx_findings_source ON findings(source_call_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_findings_signature ON findings(signature);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// InsertCall stores a RedactedCall (idempotent on id), discovers/updates the edge
// it belongs to, and then runs eviction. Edge discovery is keyed by
// (peer_host, direction) — no target list is configured.
func (s *Store) InsertCall(c model.RedactedCall) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal call: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO calls
		  (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
		c.ID, c.CapturedAt, c.Integration, nullStr(c.PeerHost), nullStr(c.Direction), nullStr(c.EdgeClass),
		c.Method, c.Route, c.StatusCode,
		nullStr(c.Correlation.RequestID), nullStr(c.Correlation.IdempotencyKey), nullStr(c.Correlation.TraceID),
		len(doc), string(doc),
	)
	if err != nil {
		return fmt.Errorf("insert call: %w", err)
	}
	// Only fold a genuinely new row into the edge (idempotent replays of the same
	// id must not double-count call_count).
	if n, _ := res.RowsAffected(); n > 0 && c.PeerHost != "" {
		if err := s.upsertEdgeLocked(c.PeerHost, c.Direction, c.EdgeClass, c.CapturedAt); err != nil {
			return err
		}
	}
	return s.evictLocked()
}

// upsertEdgeLocked records/updates the edge a call belongs to. role and class
// fall out of direction / the call's edge.class. Caller must hold s.mu.
func (s *Store) upsertEdgeLocked(peerHost, direction, class, at string) error {
	if class == "" {
		class = edge.Classify(peerHost)
	}
	role := edge.Role(direction)
	_, err := s.db.Exec(
		`INSERT INTO edges (peer_host, direction, role, class, first_seen, last_seen, call_count, drift_count)
		   VALUES (?,?,?,?,?,?,1,0)
		 ON CONFLICT(peer_host, direction) DO UPDATE SET
		   last_seen  = MAX(edges.last_seen, excluded.last_seen),
		   first_seen = MIN(edges.first_seen, excluded.first_seen),
		   call_count = edges.call_count + 1,
		   class      = excluded.class,
		   role       = excluded.role`,
		peerHost, direction, role, class, at, at,
	)
	if err != nil {
		return fmt.Errorf("upsert edge: %w", err)
	}
	return nil
}

// InsertFinding stores a Finding, deduped by signature: a drift is per-endpoint,
// not per-call (CONTRACTS §4). The FIRST call carrying a signature creates the
// finding (occurrence_count=1, pinning its representative source call); every
// subsequent matching call increments occurrence_count + last_seen and creates NO
// duplicate row. The stored finding id (and thus the flag idempotency key) stays
// stable across the drift's lifetime.
func (s *Store) InsertFinding(f model.Finding) error {
	if f.Signature == "" {
		f.Signature = f.ComputeSignature()
	}
	var sourceCallID *string
	if f.SourceCallID != nil && *f.SourceCallID != "" {
		sourceCallID = f.SourceCallID
	}
	seen := f.DetectedAt

	s.mu.Lock()
	defer s.mu.Unlock()

	// Is this signature already known?
	var existingDoc string
	var existingOcc int
	var existingFirst string
	err := s.db.QueryRow(
		`SELECT doc, occurrence_count, first_seen FROM findings WHERE signature=?`, f.Signature,
	).Scan(&existingDoc, &existingOcc, &existingFirst)

	switch {
	case err == sql.ErrNoRows:
		// First occurrence — create the finding.
		f.OccurrenceCount = 1
		if f.FirstSeen == "" {
			f.FirstSeen = seen
		}
		if f.LastSeen == "" {
			f.LastSeen = seen
		}
		doc, mErr := json.Marshal(f)
		if mErr != nil {
			return fmt.Errorf("marshal finding: %w", mErr)
		}
		if _, err := s.db.Exec(
			`INSERT INTO findings
			  (id, signature, kind, severity, integration, endpoint, rule, source_call_id, occurrence_count, first_seen, last_seen, detected_at, doc)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.ID, f.Signature, f.Kind, f.Severity, f.Integration, f.Endpoint, f.Rule,
			nullPtr(sourceCallID), f.OccurrenceCount, f.FirstSeen, f.LastSeen, f.DetectedAt, string(doc),
		); err != nil {
			return fmt.Errorf("insert finding: %w", err)
		}
		if sourceCallID != nil {
			// pin-on-finding: the representative call stays reproducible.
			if _, err := s.db.Exec(`UPDATE calls SET pinned=1 WHERE id=?`, *sourceCallID); err != nil {
				return fmt.Errorf("pin source call: %w", err)
			}
			// Attribute the drift to the source call's edge (one per signature).
			if err := s.bumpEdgeDriftLocked(*sourceCallID); err != nil {
				return err
			}
		}
		return nil

	case err != nil:
		return fmt.Errorf("lookup finding by signature: %w", err)

	default:
		// Repeat occurrence — increment count + last_seen, no duplicate row.
		var existing model.Finding
		if uErr := json.Unmarshal([]byte(existingDoc), &existing); uErr != nil {
			return fmt.Errorf("unmarshal existing finding: %w", uErr)
		}
		existing.OccurrenceCount = existingOcc + 1
		if seen > existing.LastSeen {
			existing.LastSeen = seen
		}
		doc, mErr := json.Marshal(existing)
		if mErr != nil {
			return fmt.Errorf("marshal finding: %w", mErr)
		}
		if _, err := s.db.Exec(
			`UPDATE findings SET occurrence_count=?, last_seen=?, doc=? WHERE signature=?`,
			existing.OccurrenceCount, existing.LastSeen, string(doc), f.Signature,
		); err != nil {
			return fmt.Errorf("increment finding occurrence: %w", err)
		}
		return nil
	}
}

// bumpEdgeDriftLocked increments drift_count on the edge that owns the given
// source call. Caller must hold s.mu.
func (s *Store) bumpEdgeDriftLocked(sourceCallID string) error {
	var peerHost, direction sql.NullString
	err := s.db.QueryRow(`SELECT peer_host, direction FROM calls WHERE id=?`, sourceCallID).Scan(&peerHost, &direction)
	if err == sql.ErrNoRows || !peerHost.Valid || peerHost.String == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup source call edge: %w", err)
	}
	if _, err := s.db.Exec(
		`UPDATE edges SET drift_count = drift_count + 1 WHERE peer_host=? AND direction=?`,
		peerHost.String, direction.String,
	); err != nil {
		return fmt.Errorf("bump edge drift: %w", err)
	}
	return nil
}

// PinCall marks a call pinned (kept out of the eviction pool).
func (s *Store) PinCall(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE calls SET pinned=1 WHERE id=?`, id)
	return err
}

// MarkPromoted implements evict-after-promote: a flagged call is unpinned and
// stamped promoted_at, returning it to the eviction pool.
func (s *Store) MarkPromoted(id string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE calls SET pinned=0, promoted_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	return s.evictLocked()
}

// evictLocked FIFO-evicts oldest pinned=0 rows until both caps are satisfied.
// Caller must hold s.mu.
func (s *Store) evictLocked() error {
	for {
		var rows int
		var bytes sql.NullInt64
		if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(byte_size),0) FROM calls`).Scan(&rows, &bytes); err != nil {
			return fmt.Errorf("window stats: %w", err)
		}
		overRows := s.maxRows > 0 && rows > s.maxRows
		overBytes := s.maxBytes > 0 && bytes.Int64 > s.maxBytes
		if !overRows && !overBytes {
			return nil
		}
		res, err := s.db.Exec(
			`DELETE FROM calls WHERE seq = (SELECT seq FROM calls WHERE pinned=0 ORDER BY seq ASC LIMIT 1)`,
		)
		if err != nil {
			return fmt.Errorf("evict: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Everything left is pinned; the window can legitimately exceed the
			// caps to preserve evidence. Stop rather than spin.
			return nil
		}
	}
}

// GetCall returns the stored RedactedCall for id.
func (s *Store) GetCall(id string) (model.RedactedCall, bool, error) {
	var doc string
	err := s.db.QueryRow(`SELECT doc FROM calls WHERE id=?`, id).Scan(&doc)
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
func (s *Store) GetFinding(id string) (model.Finding, bool, error) {
	var doc string
	err := s.db.QueryRow(`SELECT doc FROM findings WHERE id=?`, id).Scan(&doc)
	if err == sql.ErrNoRows {
		return model.Finding{}, false, nil
	}
	if err != nil {
		return model.Finding{}, false, err
	}
	var f model.Finding
	if err := json.Unmarshal([]byte(doc), &f); err != nil {
		return model.Finding{}, false, err
	}
	return f, true, nil
}

// ListCalls returns up to limit most-recent calls, newest first.
func (s *Store) ListCalls(limit int) ([]model.RedactedCall, error) {
	return listDocs[model.RedactedCall](s, `SELECT doc FROM calls ORDER BY seq DESC LIMIT ?`, limit)
}

// ListFindings returns up to limit most-recent findings, newest first.
func (s *Store) ListFindings(limit int) ([]model.Finding, error) {
	return listDocs[model.Finding](s, `SELECT doc FROM findings ORDER BY seq DESC LIMIT ?`, limit)
}

// ListEdges returns discovered edges. When externalOnly is true, internal
// same-team edges are excluded (they are classified out of surfacing). Ordered
// by direction then most-recently-seen so outbound/inbound group naturally.
func (s *Store) ListEdges(externalOnly bool) ([]model.Edge, error) {
	q := `SELECT peer_host, direction, role, class, first_seen, last_seen, call_count, drift_count FROM edges`
	if externalOnly {
		q += ` WHERE class = '` + edge.ClassExternal + `'`
	}
	q += ` ORDER BY direction ASC, last_seen DESC`
	rows, err := s.db.Query(q)
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

// listDocs is a package function (Go methods may not have type parameters).
func listDocs[T any](s *Store, query string, limit int) ([]T, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(query, limit)
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
func (s *Store) Stats() (rows int, bytes int64, err error) {
	var b sql.NullInt64
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(byte_size),0) FROM calls`).Scan(&rows, &b)
	return rows, b.Int64, err
}

// Counts returns the number of calls and findings currently stored.
func (s *Store) Counts() (calls int, findings int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&calls); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&findings)
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
