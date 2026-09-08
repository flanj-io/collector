package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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
		base:     base{db: db, rebind: rebindIdentity, octetLength: sqliteOctetLength},
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
  drifted      INTEGER NOT NULL DEFAULT 0,
  validated    TEXT NOT NULL DEFAULT '',
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
  edge_class   TEXT,
  format       TEXT NOT NULL,
  title        TEXT,
  version      TEXT,
  docs_url     TEXT,
  endpoints    INTEGER NOT NULL DEFAULT 0,
  loaded_at    TEXT NOT NULL,
  doc          TEXT NOT NULL,
  source       TEXT NOT NULL DEFAULT 'config',
  prev_doc     TEXT,
  prev_version TEXT,
  prev_loaded_at TEXT
);
CREATE TABLE IF NOT EXISTS settings (
  key          TEXT PRIMARY KEY,
  value        TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS finding_occurrences (
  id             TEXT PRIMARY KEY,
  signature      TEXT NOT NULL,
  source_call_id TEXT,
  seen_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_finding_occurrences_seen ON finding_occurrences(seen_at);
-- The ledger is pruned by the store clock (occurrenceTTL), not by the call a
-- finding names, so the source_call_id index has nothing left to serve. It is
-- dropped rather than left to cost every ledger insert; the COLUMN stays, as
-- the diagnostic trail from a ledger row back to its evidence.
DROP INDEX IF EXISTS idx_finding_occurrences_call;
CREATE INDEX IF NOT EXISTS idx_calls_pinned_seq ON calls(pinned, seq);
CREATE INDEX IF NOT EXISTS idx_findings_source ON findings(source_call_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_findings_signature ON findings(signature);
CREATE INDEX IF NOT EXISTS idx_calls_captured_edge ON calls(captured_at, peer_host, direction);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS never widens a table that already exists, so
	// columns added after a database was first created need an explicit ALTER.
	// SQLite has no ADD COLUMN IF NOT EXISTS: run it and treat "duplicate
	// column" as success, which makes this idempotent and safe on every start.
	for _, col := range specInfoAddedColumns {
		if _, err := s.db.Exec(`ALTER TABLE spec_infos ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate spec_infos: add %s: %w", col, err)
		}
	}
	// calls.drifted / calls.validated — same additive widening (callsAddedColumns).
	for _, col := range callsAddedColumns {
		if _, err := s.db.Exec(`ALTER TABLE calls ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate calls: add %s: %w", col, err)
		}
	}
	// One-shot repair, idempotent: until 2026-09-07 PutSpecInfo never wrote
	// `source`, so every observed MCP snapshot took the column default and was
	// listed as a CONFIG-loaded contract. The rule is specSourceOf's — format
	// "mcp" was observed on the wire — and the postgres schema applies the same
	// statement. Runs blind on every start; after the first it matches nothing.
	if _, err := s.db.Exec(`UPDATE spec_infos SET source='observed' WHERE format='mcp' AND source='config'`); err != nil {
		return fmt.Errorf("migrate spec_infos: repair observed source: %w", err)
	}
	return nil
}

// specInfoAddedColumns are the spec_infos columns introduced after the table
// shipped — contract provenance, and the one previous document kept on replace.
// Additive only: widening is the whole reason this list can be applied blind.
// callsAddedColumns are the calls columns introduced after the table shipped.
// `drifted` records that THIS call produced a finding; `validated` is the drift
// processor's own per-call verdict — see model.RedactedCall. Its default is the
// EMPTY string on purpose: every row that exists when the column arrives gets
// it, and nothing written afterwards ever does (InsertCall always supplies the
// column), so "" is unambiguously "stored before verdicts were recorded" — the
// one case the UI may still fall back to its edge-based guess for.
var callsAddedColumns = []string{
	`drifted INTEGER NOT NULL DEFAULT 0`,
	`validated TEXT NOT NULL DEFAULT ''`,
}

var specInfoAddedColumns = []string{
	`edge_class TEXT`,
	`source TEXT NOT NULL DEFAULT 'config'`,
	`prev_doc TEXT`,
	`prev_version TEXT`,
	`prev_loaded_at TEXT`,
}

// InsertCall stores a RedactedCall (idempotent on id), discovers/updates the edge
// it belongs to, and then runs eviction. Edge discovery is keyed by
// (peer_host, direction) — no target list is configured.
func (s *sqliteStore) InsertCall(c model.RedactedCall) (err error) {
	defer func() { err = classify(err) }() // ErrRejected on a constraint the ON CONFLICT does not absorb
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal call: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`INSERT INTO calls
		  (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, drifted, validated, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?,?)
		 ON CONFLICT(id) DO NOTHING`,
		c.ID, c.CapturedAt, c.Integration, nullStr(c.PeerHost), nullStr(c.Direction), nullStr(c.EdgeClass),
		c.Method, c.Route, c.StatusCode,
		nullStr(c.Correlation.RequestID), nullStr(c.Correlation.IdempotencyKey), nullStr(c.Correlation.TraceID),
		len(doc), insertDrifted(c), c.Validated, string(doc),
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
//
// Idempotent on the finding's OWN id (the occurrence ledger — recordOccurrence):
// the same record delivered twice — a batch the store exporter retried after a
// failed write, a front re-sending after a lost ACK — changes nothing the
// second time. Every statement runs in one transaction, so a write that fails
// half-way leaves no ledger row behind and the retry applies the finding in full.
func (s *sqliteStore) InsertFinding(f model.Finding) (err error) {
	defer func() { err = classify(err) }() // ErrRejected on a constraint the ON CONFLICT does not absorb
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

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("insert finding: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The ledger is stamped with the STORE's clock, not f.DetectedAt: a
	// re-delivered record repeats its DetectedAt, so only the store's own
	// reading says how long this copy can still be in flight (occurrenceTTL).
	now := time.Now()
	applied, err := recordOccurrence(tx, s.rebind, f, sourceCallID, isoTime(now))
	if err != nil {
		return err
	}
	if !applied {
		return nil // a re-delivered record: already applied, nothing to do
	}
	// Bound the ledger from the path that grows it, so a deployment whose
	// findings are all CALL-LESS (a flapping MCP snapshot) — which never
	// reaches the eviction pass — stays bounded too.
	if err := pruneOccurrences(tx, s.rebind, now); err != nil {
		return err
	}

	// First occurrence wins the insert; a known signature conflicts and falls
	// through to the counter bump. The doc is stored once and never rewritten —
	// occurrence_count/last_seen are authoritative in their columns (reads patch
	// them back in), which keeps dedup a pair of atomic statements.
	res, err := tx.Exec(
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
	// mark-on-finding: THIS call drifted, whether or not its signature is new.
	// Distinct from the pin, which marks the ONE representative call kept
	// reproducible — drift is a property of every call that produced a finding,
	// and losing the repeats is what forced the UI to guess per endpoint and
	// relabel conforming neighbours.
	//
	// Per-call kinds only, and BOTH transports have one: an MCP output_mismatch
	// names the call whose structuredContent violated the tool's own declared
	// outputSchema, exactly as live-vs-spec names the call whose body violated
	// the OpenAPI document. Leaving MCP out here is what sent the Traffic tab
	// back to asking "does this TOOL have a mismatch?" — a set keyed by
	// integration and tool, which relabelled every historic call of the tool and
	// accused the provider over results nothing had judged.
	if sourceCallID != nil && marksSourceCallDrifted(f.Kind) {
		if _, err := tx.Exec(`UPDATE calls SET drifted=1 WHERE id=?`, *sourceCallID); err != nil {
			return fmt.Errorf("mark call drifted: %w", err)
		}
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if sourceCallID != nil {
			// pin-on-finding: the representative call stays reproducible.
			if _, err := tx.Exec(`UPDATE calls SET pinned=1 WHERE id=?`, *sourceCallID); err != nil {
				return fmt.Errorf("pin source call: %w", err)
			}
			// Attribute the drift to the source call's edge (one per signature).
			if err := bumpEdgeDrift(tx, s.rebind, *sourceCallID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("insert finding: commit: %w", err)
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
	if err := tx.QueryRow(`SELECT doc FROM findings WHERE signature=?`, f.Signature).Scan(&storedDoc); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read finding doc: %w", err)
	}
	if next, ok := refreshedFindingDoc(storedDoc, f); ok {
		if _, err := tx.Exec(
			`UPDATE findings SET occurrence_count = occurrence_count + 1, last_seen = MAX(last_seen, ?),
			   severity=?, detected_at=?, doc=? WHERE signature=?`,
			seen, f.Severity, f.DetectedAt, next, f.Signature,
		); err != nil {
			return fmt.Errorf("refresh finding evidence: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("refresh finding evidence: commit: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(
		`UPDATE findings SET occurrence_count = occurrence_count + 1, last_seen = MAX(last_seen, ?) WHERE signature=?`,
		seen, f.Signature,
	); err != nil {
		return fmt.Errorf("increment finding occurrence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("increment finding occurrence: commit: %w", err)
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
	// The ledger no longer rides on call eviction — it has its own clock — but
	// this pass is the store's housekeeping beat, so it is where the TTL prune
	// runs on the call path (InsertCall, MarkPromoted). One indexed range
	// delete; in steady state it matches nothing.
	if err := pruneOccurrences(s.db, s.rebind, time.Now()); err != nil {
		return err
	}
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
		n, err := evictOldest(s.db, s.rebind, keepID, batch)
		if err != nil {
			return err
		}
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
//
// An MCP row's loaded_at moves only when its document does (2026-09-08). That
// stamp is the UI's "since this snapshot" anchor and the contract channel's
// change token, and an observed tools/list is written by EVERY front that
// re-observes it, each with its own first-sighting stamp: restamping on each
// write flip-flopped the row between two fronts' stamps forever, and every
// flip re-downloaded the document on every front and flipped calls captured
// before the newer stamp to NOT CHECKED. An OpenAPI row is one uploader's
// document, written once per upload, and keeps the plain upsert.
func (s *sqliteStore) PutSpecInfo(info model.SpecInfo, rawSpec []byte) (err error) {
	defer func() { err = classify(err) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	role := info.Role
	if role == "" {
		role = model.SpecRoleProvider
	}
	// `source` is written, and rewritten on conflict, so the row's provenance
	// always describes the document in it. Left out of the statement (as it
	// was until 2026-09-07) the column default filed every observed MCP
	// snapshot as config.
	_, err = s.db.Exec(
		`INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, version, docs_url, endpoints, loaded_at, doc, source)
		   VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(integration) DO UPDATE SET
		   role=excluded.role, peer_host=excluded.peer_host, edge_class=excluded.edge_class, format=excluded.format, title=excluded.title,
		   version=excluded.version, docs_url=excluded.docs_url, endpoints=excluded.endpoints,
		   loaded_at=CASE WHEN excluded.format=? AND spec_infos.doc=excluded.doc THEN spec_infos.loaded_at ELSE excluded.loaded_at END,
		   doc=excluded.doc, source=excluded.source`,
		info.Integration, role, nullStr(info.PeerHost), nullStr(info.EdgeClass), info.Format, nullStr(info.Title),
		nullStr(info.Version), nullStr(info.DocsURL), info.Endpoints, info.LoadedAt, string(rawSpec), specSourceOf(info),
		model.SpecFormatMCP,
	)
	if err != nil {
		return fmt.Errorf("put spec info: %w", err)
	}
	return nil
}

// PutUploadedSpec writes an uploaded contract, rotating the document it
// replaces into prev_doc. Under the store mutex and in one transaction: a
// half-applied replace would leave the host validating against a document its
// recorded metadata no longer describes.
func (s *sqliteStore) PutUploadedSpec(info model.SpecInfo, rawSpec []byte) (UploadedSpecPrevious, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var prev UploadedSpecPrevious
	tx, err := s.db.Begin()
	if err != nil {
		return prev, fmt.Errorf("put uploaded spec: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	prev, err = readSpecForReplace(tx, func(q string) string { return q }, info.Integration)
	if err != nil {
		return UploadedSpecPrevious{}, fmt.Errorf("put uploaded spec: read previous: %w", err)
	}
	if err := execUploadedSpec(tx, func(q string) string { return q }, info, rawSpec, prev); err != nil {
		return UploadedSpecPrevious{}, err
	}
	if err := tx.Commit(); err != nil {
		return UploadedSpecPrevious{}, fmt.Errorf("put uploaded spec: commit: %w", err)
	}
	return prev, nil
}

// execUploadedSpec writes the row for both backends: the new document current,
// the one it displaced kept as the single previous.
func execUploadedSpec(tx interface {
	Exec(string, ...any) (sql.Result, error)
}, rebind func(string) string, info model.SpecInfo, rawSpec []byte, prev UploadedSpecPrevious) error {
	role := info.Role
	if role == "" {
		role = model.SpecRoleProvider
	}
	source := info.Source
	if source == "" {
		source = model.SpecSourceUpload
	}
	_, err := tx.Exec(rebind(
		`INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, version, docs_url,
		                         endpoints, loaded_at, doc, source, prev_doc, prev_version, prev_loaded_at)
		   VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(integration) DO UPDATE SET
		   role=excluded.role, peer_host=excluded.peer_host, edge_class=excluded.edge_class, format=excluded.format, title=excluded.title,
		   version=excluded.version, docs_url=excluded.docs_url, endpoints=excluded.endpoints,
		   loaded_at=excluded.loaded_at, doc=excluded.doc, source=excluded.source,
		   prev_doc=excluded.prev_doc, prev_version=excluded.prev_version,
		   prev_loaded_at=excluded.prev_loaded_at`),
		info.Integration, role, nullStr(info.PeerHost), nullStr(info.EdgeClass), info.Format, nullStr(info.Title),
		nullStr(info.Version), nullStr(info.DocsURL), info.Endpoints, info.LoadedAt, string(rawSpec),
		source, nullStr(string(prev.Raw)), nullStr(prev.Version), nullStr(prev.LoadedAt),
	)
	if err != nil {
		return fmt.Errorf("put uploaded spec: %w", err)
	}
	return nil
}

// compile-time assertion: the sqlite backend satisfies the store surface.
var _ Store = (*sqliteStore)(nil)
