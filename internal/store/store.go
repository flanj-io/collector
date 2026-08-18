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
  seq            INTEGER PRIMARY KEY AUTOINCREMENT,
  id             TEXT NOT NULL UNIQUE,
  kind           TEXT NOT NULL,
  severity       TEXT NOT NULL,
  integration    TEXT NOT NULL,
  endpoint       TEXT NOT NULL,
  rule           TEXT NOT NULL,
  source_call_id TEXT,
  detected_at    TEXT NOT NULL,
  doc            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_calls_pinned_seq ON calls(pinned, seq);
CREATE INDEX IF NOT EXISTS idx_findings_source ON findings(source_call_id);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// InsertCall stores a RedactedCall (idempotent on id) and then runs eviction.
func (s *Store) InsertCall(c model.RedactedCall) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal call: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(
		`INSERT OR IGNORE INTO calls
		  (id, captured_at, integration, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,0,?)`,
		c.ID, c.CapturedAt, c.Integration, c.Method, c.Route, c.StatusCode,
		nullStr(c.Correlation.RequestID), nullStr(c.Correlation.IdempotencyKey), nullStr(c.Correlation.TraceID),
		len(doc), string(doc),
	)
	if err != nil {
		return fmt.Errorf("insert call: %w", err)
	}
	return s.evictLocked()
}

// InsertFinding stores a Finding and pins its source call (pin-on-finding) so the
// evidence behind an open finding is never evicted out from under the UI.
func (s *Store) InsertFinding(f model.Finding) error {
	doc, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal finding: %w", err)
	}
	var sourceCallID *string
	if f.SourceCallID != nil && *f.SourceCallID != "" {
		sourceCallID = f.SourceCallID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO findings
		  (id, kind, severity, integration, endpoint, rule, source_call_id, detected_at, doc)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Kind, f.Severity, f.Integration, f.Endpoint, f.Rule, nullPtr(sourceCallID), f.DetectedAt, string(doc),
	); err != nil {
		return fmt.Errorf("insert finding: %w", err)
	}
	if sourceCallID != nil {
		if _, err := s.db.Exec(`UPDATE calls SET pinned=1 WHERE id=?`, *sourceCallID); err != nil {
			return fmt.Errorf("pin source call: %w", err)
		}
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
