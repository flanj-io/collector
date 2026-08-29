package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
)

// sqliteStore is the embedded default backend: one WAL file, one pod. The
// connection pool is pinned to one connection and every write path holds mu —
// with pure-Go SQLite this is the simplest correct concurrency model, and the
// process is the only writer by contract (one pod per file).
type sqliteStore struct {
	base
	maxRows  int
	maxBytes int64
	mu       sync.Mutex // serialises insert+evict so the window stays consistent
}

// OpenSQLite opens (creating if needed) the SQLite database at path, applies
// the schema, and returns the single owning Store. maxRows/maxBytes are the
// window ceilings (<=0 disables that dimension).
func OpenSQLite(path string, maxRows int, maxBytes int64) (Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &sqliteStore{
		base:     base{db: db, rebind: rebindIdentity},
		maxRows:  maxRows,
		maxBytes: maxBytes,
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *sqliteStore) migrate() error {
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
CREATE TABLE IF NOT EXISTS spec_infos (
  integration  TEXT PRIMARY KEY,
  role         TEXT NOT NULL DEFAULT 'provider',
  peer_host    TEXT,
  format       TEXT NOT NULL,
  title        TEXT,
  version      TEXT,
  docs_url     TEXT,
  endpoints    INTEGER NOT NULL DEFAULT 0,
  loaded_at    TEXT NOT NULL,
  doc          TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key          TEXT PRIMARY KEY,
  value        TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_calls_pinned_seq ON calls(pinned, seq);
CREATE INDEX IF NOT EXISTS idx_findings_source ON findings(source_call_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_findings_signature ON findings(signature);
CREATE INDEX IF NOT EXISTS idx_calls_captured_edge ON calls(captured_at, peer_host, direction);
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
func (s *sqliteStore) InsertCall(c model.RedactedCall) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal call: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`INSERT INTO calls
		  (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)
		 ON CONFLICT(id) DO NOTHING`,
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
	if n, _ := res.RowsAffected(); n > 0 {
		if c.PeerHost != "" {
			if err := s.upsertEdgeLocked(c.PeerHost, c.Direction, c.EdgeClass, c.CapturedAt); err != nil {
				return err
			}
		}
		// A finding may already reference this call (it arrived first): pin it
		// now. Atomic vs InsertFinding because both hold s.mu.
		if _, err := latePin(s.db, s.rebind, c); err != nil {
			return err
		}
	}
	// Never let this insert's own eviction take the row it just wrote: the
	// finding that pins it may be a record behind in the same batch (or in
	// flight from a front collector). Costs at most one row over the cap in the
	// degenerate all-pinned regime.
	return s.evictLocked(c.ID)
}

// upsertEdgeLocked records/updates the edge a call belongs to. role and class
// fall out of direction / the call's edge.class. Caller must hold s.mu.
func (s *sqliteStore) upsertEdgeLocked(peerHost, direction, class, at string) error {
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
func (s *sqliteStore) InsertFinding(f model.Finding) error {
	if f.Signature == "" {
		f.Signature = f.ComputeSignature()
	}
	var sourceCallID *string
	if f.SourceCallID != nil && *f.SourceCallID != "" {
		sourceCallID = f.SourceCallID
	}
	seen := f.DetectedAt
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

	s.mu.Lock()
	defer s.mu.Unlock()

	// First occurrence wins the insert; a known signature conflicts and falls
	// through to the counter bump. The doc is stored once and never rewritten —
	// occurrence_count/last_seen are authoritative in their columns (reads patch
	// them back in), which keeps dedup a pair of atomic statements.
	res, err := s.db.Exec(
		`INSERT INTO findings
		  (id, signature, kind, severity, integration, endpoint, rule, source_call_id, occurrence_count, first_seen, last_seen, detected_at, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(signature) DO NOTHING`,
		f.ID, f.Signature, f.Kind, f.Severity, f.Integration, f.Endpoint, f.Rule,
		nullPtr(sourceCallID), f.OccurrenceCount, f.FirstSeen, f.LastSeen, f.DetectedAt, string(doc),
	)
	if err != nil {
		return fmt.Errorf("insert finding: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
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
	}

	// Repeat occurrence — increment count + advance last_seen, no duplicate row.
	// ISO-8601 text compares lexically, so MAX() advances correctly.
	//
	// …and, for a definition_change whose evidence has MOVED ON, rewrite the doc
	// in place (refreshedFindingDoc explains why the frozen doc is wrong for
	// that one kind). Same statement, so the counter and the evidence can never
	// disagree; the id, signature and first_seen are carried over.
	var storedDoc string
	if err := s.db.QueryRow(`SELECT doc FROM findings WHERE signature=?`, f.Signature).Scan(&storedDoc); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read finding doc: %w", err)
	}
	if next, ok := refreshedFindingDoc(storedDoc, f); ok {
		if _, err := s.db.Exec(
			`UPDATE findings SET occurrence_count = occurrence_count + 1, last_seen = MAX(last_seen, ?),
			   severity=?, detected_at=?, doc=? WHERE signature=?`,
			seen, f.Severity, f.DetectedAt, next, f.Signature,
		); err != nil {
			return fmt.Errorf("refresh finding evidence: %w", err)
		}
		return nil
	}
	if _, err := s.db.Exec(
		`UPDATE findings SET occurrence_count = occurrence_count + 1, last_seen = MAX(last_seen, ?) WHERE signature=?`,
		seen, f.Signature,
	); err != nil {
		return fmt.Errorf("increment finding occurrence: %w", err)
	}
	return nil
}

// bumpEdgeDriftLocked increments drift_count on the edge that owns the given
// source call. Caller must hold s.mu.
func (s *sqliteStore) bumpEdgeDriftLocked(sourceCallID string) error {
	var peerHost, direction sql.NullString
	err := s.db.QueryRow(`SELECT peer_host, direction FROM calls WHERE id=?`, sourceCallID).Scan(&peerHost, &direction)
	if err == sql.ErrNoRows || (err == nil && (!peerHost.Valid || peerHost.String == "")) {
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

// MarkPromoted implements evict-after-promote: a flagged call is unpinned and
// stamped promoted_at, returning it to the eviction pool.
func (s *sqliteStore) MarkPromoted(id string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE calls SET pinned=0, promoted_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	return s.evictLocked("")
}

// evictLocked FIFO-evicts oldest pinned=0 rows until both caps are satisfied.
// keepID (may be "") is a row that must survive this pass — the call the
// caller just inserted, whose pinning finding may not have landed yet.
// Row overage is deleted in batches (steady state is a batch of 1; bursts —
// e.g. a lowered cap — catch up 256 rows per statement); byte overage converges
// one row at a time so the window is never cut below its cap. Caller must hold
// s.mu.
func (s *sqliteStore) evictLocked(keepID string) error {
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
		batch := 1
		if overRows {
			if excess := rows - s.maxRows; excess > batch {
				batch = excess
			}
			if batch > evictBatchMax {
				batch = evictBatchMax
			}
		}
		res, err := s.db.Exec(
			`DELETE FROM calls WHERE seq IN (SELECT seq FROM calls WHERE pinned=0 AND id<>? ORDER BY seq ASC LIMIT ?)`, keepID, batch,
		)
		if err != nil {
			return fmt.Errorf("evict: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Everything left is pinned (or is keepID); the window can
			// legitimately exceed the caps to preserve evidence. Stop rather than spin.
			return nil
		}
	}
}

// PutSpecInfo upserts the provider contract loaded by the drift processor,
// keyed by integration. rawSpec is the spec document exactly as loaded; the UI
// serves it verbatim so engineers can open the contract being validated.
func (s *sqliteStore) PutSpecInfo(info model.SpecInfo, rawSpec []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	role := info.Role
	if role == "" {
		role = model.SpecRoleProvider
	}
	_, err := s.db.Exec(
		`INSERT INTO spec_infos (integration, role, peer_host, format, title, version, docs_url, endpoints, loaded_at, doc)
		   VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(integration) DO UPDATE SET
		   role=excluded.role, peer_host=excluded.peer_host, format=excluded.format, title=excluded.title,
		   version=excluded.version, docs_url=excluded.docs_url, endpoints=excluded.endpoints,
		   loaded_at=excluded.loaded_at, doc=excluded.doc`,
		info.Integration, role, nullStr(info.PeerHost), info.Format, nullStr(info.Title),
		nullStr(info.Version), nullStr(info.DocsURL), info.Endpoints, info.LoadedAt, string(rawSpec),
	)
	if err != nil {
		return fmt.Errorf("put spec info: %w", err)
	}
	return nil
}

// compile-time assertion: the sqlite backend satisfies the store surface.
var _ Store = (*sqliteStore)(nil)
