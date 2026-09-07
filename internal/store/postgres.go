package store

import (
	"database/sql"
	"fmt"
	"time"

	"encoding/json"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
)

// Advisory-lock keys (PostgreSQL advisory locks are scoped to the connected
// database, so one collector deployment = one database; see docs/STORE.md).
// High 32 bits spell "vinf" so the keys are recognisable in pg_locks.
const (
	pgLockSchema  int64 = 0x76696e6600000001 // serialises concurrent DDL at pod start
	pgLockEvict   int64 = 0x76696e6600000002 // at most one pod evicts at a time
	pgLockMigrate int64 = 0x76696e6600000003 // serialises the one-shot sqlite import
)

// pgLockNSCallPin is the namespace (first int4 key) of the per-call advisory
// xact lock pg_advisory_xact_lock(pgLockNSCallPin, hashtext(call_id)). The
// two-int4 form is a separate keyspace from the int8 keys above. InsertCall
// takes it as the first statement of its tx; InsertFinding takes it (first
// occurrence only) before pinning the source call. That serialises the two
// writers on one call id and closes the READ COMMITTED write-skew window where
// the finding's pin UPDATE cannot see the uncommitted call AND the call's
// late-pin EXISTS cannot see the uncommitted finding. Lock order on both sides
// is lock(id) -> calls row -> edge row, and InsertCall never waits on the
// findings unique index, so there is no cycle.
const pgLockNSCallPin int32 = 0x76696e66 // "vinf"

// pgLockNSSpecUpload is the namespace of the per-contract advisory xact lock
// taken by PutUploadedSpec: pg_advisory_xact_lock(pgLockNSSpecUpload,
// hashtext(integration)). Two operators replacing the same contract at once
// would otherwise interleave the read of the current document and the write
// that displaces it, and one of them would lose the previous document the
// version diff needs. A DIFFERENT namespace from call pinning, so a busy
// ingest never queues behind an upload or vice versa.
const pgLockNSSpecUpload int32 = 0x73706563 // "spec"

// postgresStore is the shared external backend: N collector pods write to one
// database concurrently. There is no process-level mutex — every write path is
// a single atomic statement or a short transaction, and eviction/DDL/migration
// coordinate across pods with advisory locks.
type postgresStore struct {
	base
	maxRows  int
	maxBytes int64
}

// OpenPostgres connects to the shared database, applies the schema (under an
// advisory lock — pods start concurrently), and returns the Store handle.
// maxRows/maxBytes are the window ceilings (<=0 disables that dimension) and
// should be identical on every pod sharing the database.
func OpenPostgres(dsn string, maxRows int, maxBytes int64) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	p := &postgresStore{
		base:     base{db: db, rebind: rebindDollar},
		maxRows:  maxRows,
		maxBytes: maxBytes,
	}
	if err := p.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

func (p *postgresStore) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS calls (
  seq          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
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
  byte_size    BIGINT NOT NULL,
  pinned       INTEGER NOT NULL DEFAULT 0,
  drifted      INTEGER NOT NULL DEFAULT 0,
  promoted_at  TEXT,
  doc          TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS findings (
  seq              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
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
  call_count   BIGINT NOT NULL DEFAULT 0,
  drift_count  BIGINT NOT NULL DEFAULT 0,
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
CREATE INDEX IF NOT EXISTS idx_finding_occurrences_call ON finding_occurrences(source_call_id);
CREATE INDEX IF NOT EXISTS idx_calls_pinned_seq ON calls(pinned, seq);
CREATE INDEX IF NOT EXISTS idx_findings_source ON findings(source_call_id);
CREATE INDEX IF NOT EXISTS idx_calls_captured_edge ON calls(captured_at, peer_host, direction);

-- Widening: CREATE TABLE IF NOT EXISTS leaves an existing table alone, so
-- columns added after spec_infos first shipped (contract provenance, and the
-- one previous document kept on replace) need an explicit ALTER. Additive and
-- idempotent, so this runs blind on every start. The sqlite backend does the
-- same via specInfoAddedColumns, which has no IF NOT EXISTS to lean on.
ALTER TABLE spec_infos ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'config';
ALTER TABLE spec_infos ADD COLUMN IF NOT EXISTS prev_doc TEXT;
ALTER TABLE spec_infos ADD COLUMN IF NOT EXISTS prev_version TEXT;
ALTER TABLE spec_infos ADD COLUMN IF NOT EXISTS prev_loaded_at TEXT;
ALTER TABLE spec_infos ADD COLUMN IF NOT EXISTS edge_class TEXT;
ALTER TABLE calls ADD COLUMN IF NOT EXISTS drifted INTEGER NOT NULL DEFAULT 0;
`
	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("migrate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Concurrent CREATE TABLE IF NOT EXISTS races in postgres (duplicate pg_type
	// key) — pods start simultaneously, so DDL runs under a blocking lock.
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, pgLockSchema); err != nil {
		return fmt.Errorf("migrate: schema lock: %w", err)
	}
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit: %w", err)
	}
	return nil
}

// InsertCall stores a RedactedCall (idempotent on id), discovers/updates the
// edge it belongs to, and then runs best-effort eviction. The insert + edge
// upsert share one transaction so call_count can never count a call that was
// not stored.
func (p *postgresStore) InsertCall(c model.RedactedCall) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal call: %w", err)
	}
	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("insert call: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Serialise against a concurrent InsertFinding on the same call id (see
	// pgLockNSCallPin). Taken FIRST so this tx holds no row locks while waiting.
	if _, err := tx.Exec(p.rebind(`SELECT pg_advisory_xact_lock(?, hashtext(?))`), pgLockNSCallPin, c.ID); err != nil {
		return fmt.Errorf("insert call: lock: %w", err)
	}
	res, err := tx.Exec(p.rebind(
		`INSERT INTO calls
		  (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)
		 ON CONFLICT (id) DO NOTHING`),
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
			if err := p.upsertEdgeTx(tx, c.PeerHost, c.Direction, c.EdgeClass, c.CapturedAt); err != nil {
				return err
			}
		}
		// A finding may already reference this call (it arrived first): pin it
		// now, inside the tx, under the per-call lock.
		if _, err := latePin(tx, p.rebind, c); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert call: commit: %w", err)
	}
	// Never let this insert's own eviction take the row it just wrote: the
	// finding that pins it may be a record behind in the same batch (or in
	// flight from a front collector). Costs at most one row over the cap in the
	// degenerate all-pinned regime.
	return p.evict(c.ID)
}

// upsertEdgeTx records/updates the edge a call belongs to. The single upsert
// statement is atomic under the row lock it takes, so concurrent pods never
// lose a call_count increment.
func (p *postgresStore) upsertEdgeTx(tx *sql.Tx, peerHost, direction, class, at string) error {
	if class == "" {
		class = edge.Classify(peerHost)
	}
	role := edge.Role(direction)
	_, err := tx.Exec(p.rebind(
		`INSERT INTO edges (peer_host, direction, role, class, first_seen, last_seen, call_count, drift_count)
		   VALUES (?,?,?,?,?,?,1,0)
		 ON CONFLICT (peer_host, direction) DO UPDATE SET
		   last_seen  = GREATEST(edges.last_seen, excluded.last_seen),
		   first_seen = LEAST(edges.first_seen, excluded.first_seen),
		   call_count = edges.call_count + 1,
		   class      = excluded.class,
		   role       = excluded.role`),
		peerHost, direction, role, class, at, at,
	)
	if err != nil {
		return fmt.Errorf("upsert edge: %w", err)
	}
	return nil
}

// InsertFinding stores a Finding, deduped by signature (see the interface doc).
// Cross-pod correctness: a concurrent insert of a new signature blocks on the
// unique index until the winner commits, so the loser's insert reports 0 rows
// and its counter bump (a fresh statement snapshot) sees the committed row.
//
// Idempotent on the finding's OWN id (the occurrence ledger — recordOccurrence,
// the first statement of the transaction): a re-delivered record — a batch the
// store exporter retried after a failed write, a front re-sending after a lost
// ACK, the same batch landing on two pods — applies nothing the second time.
// The ledger row commits with the finding or not at all, so a write that fails
// half-way leaves nothing behind for the retry to trip over.
func (p *postgresStore) InsertFinding(f model.Finding) error {
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

	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("insert finding: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	applied, err := recordOccurrence(tx, p.rebind, f, sourceCallID, seen)
	if err != nil {
		return err
	}
	if !applied {
		return nil // a re-delivered record: already applied, nothing to do
	}

	res, err := tx.Exec(p.rebind(
		`INSERT INTO findings
		  (id, signature, kind, severity, integration, endpoint, rule, source_call_id, occurrence_count, first_seen, last_seen, detected_at, doc)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (signature) DO NOTHING`),
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
	//
	// Inside the transaction so it lands with the finding or not at all.
	if sourceCallID != nil && marksSourceCallDrifted(f.Kind) {
		if _, err := tx.Exec(p.rebind(`SELECT pg_advisory_xact_lock(?, hashtext(?))`), pgLockNSCallPin, *sourceCallID); err != nil {
			return fmt.Errorf("insert finding: lock: %w", err)
		}
		if _, err := tx.Exec(p.rebind(`UPDATE calls SET drifted=1 WHERE id=?`), *sourceCallID); err != nil {
			return fmt.Errorf("mark call drifted: %w", err)
		}
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// First occurrence — pin + drift attribution commit atomically with the
		// finding so no pod ever sees a finding whose evidence isn't pinned.
		if sourceCallID != nil {
			// Serialise against a concurrent InsertCall of the source call (see
			// pgLockNSCallPin) BEFORE touching the calls/edges rows.
			if _, err := tx.Exec(p.rebind(`SELECT pg_advisory_xact_lock(?, hashtext(?))`), pgLockNSCallPin, *sourceCallID); err != nil {
				return fmt.Errorf("insert finding: lock: %w", err)
			}
			if _, err := tx.Exec(p.rebind(`UPDATE calls SET pinned=1 WHERE id=?`), *sourceCallID); err != nil {
				return fmt.Errorf("pin source call: %w", err)
			}
			if err := p.bumpEdgeDriftTx(tx, *sourceCallID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("insert finding: commit: %w", err)
		}
		return nil
	}

	// Repeat occurrence — a single atomic counter bump; the stored doc stays
	// frozen as the first occurrence's JSON (stable finding id → stable flag key).
	//
	// EXCEPT for a definition_change whose evidence has moved on: its doc is
	// rewritten in the same statement, keeping the id, signature and first_seen
	// (refreshedFindingDoc explains why). The row was read inside this tx, so a
	// concurrent writer's refresh cannot be lost between read and write.
	var storedDoc string
	if err := tx.QueryRow(p.rebind(`SELECT doc FROM findings WHERE signature=? FOR UPDATE`), f.Signature).Scan(&storedDoc); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read finding doc: %w", err)
	}
	if next, ok := refreshedFindingDoc(storedDoc, f); ok {
		if _, err := tx.Exec(p.rebind(
			`UPDATE findings SET occurrence_count = occurrence_count + 1, last_seen = GREATEST(last_seen, ?),
			   severity=?, detected_at=?, doc=? WHERE signature=?`),
			seen, f.Severity, f.DetectedAt, next, f.Signature,
		); err != nil {
			return fmt.Errorf("refresh finding evidence: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("refresh finding evidence: commit: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(p.rebind(
		`UPDATE findings SET occurrence_count = occurrence_count + 1, last_seen = GREATEST(last_seen, ?) WHERE signature=?`),
		seen, f.Signature,
	); err != nil {
		return fmt.Errorf("increment finding occurrence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("increment finding occurrence: commit: %w", err)
	}
	return nil
}

// bumpEdgeDriftTx increments drift_count on the edge that owns the given
// source call.
func (p *postgresStore) bumpEdgeDriftTx(tx *sql.Tx, sourceCallID string) error {
	var peerHost, direction sql.NullString
	err := tx.QueryRow(p.rebind(`SELECT peer_host, direction FROM calls WHERE id=?`), sourceCallID).Scan(&peerHost, &direction)
	if err == sql.ErrNoRows || (err == nil && (!peerHost.Valid || peerHost.String == "")) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup source call edge: %w", err)
	}
	if _, err := tx.Exec(p.rebind(
		`UPDATE edges SET drift_count = drift_count + 1 WHERE peer_host=? AND direction=?`),
		peerHost.String, direction.String,
	); err != nil {
		return fmt.Errorf("bump edge drift: %w", err)
	}
	return nil
}

// MarkPromoted implements evict-after-promote: a flagged call is unpinned and
// stamped promoted_at, returning it to the eviction pool.
func (p *postgresStore) MarkPromoted(id string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if _, err := p.db.Exec(p.rebind(`UPDATE calls SET pinned=0, promoted_at=? WHERE id=?`), now, id); err != nil {
		return err
	}
	return p.evict("")
}

// evict FIFO-evicts oldest pinned=0 rows until both caps are satisfied — but
// only on the pod holding the eviction advisory lock. keepID (may be "") is a
// row that must survive this pass — the call the caller just inserted, whose
// pinning finding may not have landed yet. If another pod holds it,
// this is a no-op: eviction is best-effort, and the next insert on any pod
// retries, so the window converges (it may transiently overshoot the caps).
func (p *postgresStore) evict(keepID string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("evict: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var got bool
	if err := tx.QueryRow(`SELECT pg_try_advisory_xact_lock($1)`, pgLockEvict).Scan(&got); err != nil {
		return fmt.Errorf("evict: lock: %w", err)
	}
	if !got {
		return nil // another pod is evicting right now
	}
	for {
		var rows int
		var bytes sql.NullInt64
		if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(SUM(byte_size),0) FROM calls`).Scan(&rows, &bytes); err != nil {
			return fmt.Errorf("window stats: %w", err)
		}
		overRows := p.maxRows > 0 && rows > p.maxRows
		overBytes := p.maxBytes > 0 && bytes.Int64 > p.maxBytes
		if !overRows && !overBytes {
			break
		}
		batch := 1
		if overRows {
			if excess := rows - p.maxRows; excess > batch {
				batch = excess
			}
			if batch > evictBatchMax {
				batch = evictBatchMax
			}
		}
		n, err := evictOldest(tx, p.rebind, keepID, batch)
		if err != nil {
			return err
		}
		if n == 0 {
			// Everything left is pinned (or is keepID); the window can
			// legitimately exceed the caps to preserve evidence. Stop rather than spin.
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("evict: commit: %w", err)
	}
	return nil
}

// PutSpecInfo upserts the provider contract loaded by the drift processor —
// a single atomic upsert, safe for concurrent pod starts (last writer wins,
// and every pod loads the same mounted spec).
func (p *postgresStore) PutSpecInfo(info model.SpecInfo, rawSpec []byte) error {
	role := info.Role
	if role == "" {
		role = model.SpecRoleProvider
	}
	_, err := p.db.Exec(p.rebind(
		`INSERT INTO spec_infos (integration, role, peer_host, edge_class, format, title, version, docs_url, endpoints, loaded_at, doc)
		   VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (integration) DO UPDATE SET
		   role=excluded.role, peer_host=excluded.peer_host, edge_class=excluded.edge_class, format=excluded.format, title=excluded.title,
		   version=excluded.version, docs_url=excluded.docs_url, endpoints=excluded.endpoints,
		   loaded_at=excluded.loaded_at, doc=excluded.doc`),
		info.Integration, role, nullStr(info.PeerHost), nullStr(info.EdgeClass), info.Format, nullStr(info.Title),
		nullStr(info.Version), nullStr(info.DocsURL), info.Endpoints, info.LoadedAt, string(rawSpec),
	)
	if err != nil {
		return fmt.Errorf("put spec info: %w", err)
	}
	return nil
}

// PutUploadedSpec writes an uploaded contract, rotating the document it
// replaces into prev_doc. One transaction, and — because N pods may share this
// database — a row lock, so two operators replacing the same contract at once
// cannot interleave the read and the write into a lost previous document.
func (p *postgresStore) PutUploadedSpec(info model.SpecInfo, rawSpec []byte) (UploadedSpecPrevious, error) {
	var prev UploadedSpecPrevious
	tx, err := p.db.Begin()
	if err != nil {
		return prev, fmt.Errorf("put uploaded spec: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(p.rebind(`SELECT pg_advisory_xact_lock(?, hashtext(?))`), pgLockNSSpecUpload, info.Integration); err != nil {
		return prev, fmt.Errorf("put uploaded spec: lock: %w", err)
	}
	prev, err = readSpecForReplace(tx, p.rebind, info.Integration)
	if err != nil {
		return UploadedSpecPrevious{}, fmt.Errorf("put uploaded spec: read previous: %w", err)
	}
	if err := execUploadedSpec(tx, p.rebind, info, rawSpec, prev); err != nil {
		return UploadedSpecPrevious{}, err
	}
	if err := tx.Commit(); err != nil {
		return UploadedSpecPrevious{}, fmt.Errorf("put uploaded spec: commit: %w", err)
	}
	return prev, nil
}

// compile-time assertion: the postgres backend satisfies the store surface.
var _ Store = (*postgresStore)(nil)
