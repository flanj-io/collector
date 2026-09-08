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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
)

// evictBatchMax caps how many rows a single eviction DELETE may remove. Steady
// state evicts 1 row per insert; the batch only matters when catching up on a
// burst (lowered caps, post-migration fill, a pod resuming after another held
// the eviction lock).
const evictBatchMax = 256

// occurrenceTTL is how long a finding-occurrence ledger row is kept, measured
// on the STORE's own clock (seen_at, stamped at insert — never the record's
// DetectedAt, which is the front's clock and is what a re-delivery repeats).
//
// It is sized to the RE-DELIVERY HORIZON: the longest a copy of one record can
// still be in flight somewhere. That is this store exporter's retry budget
// (retry_on_failure.max_elapsed_time, 15 min) plus a tiered front's own
// otlphttp budget (5 min) — 20 minutes, rounded up to an hour so a slow
// operator restart in between is still covered. A ledger row younger than the
// TTL therefore still recognises every copy that can arrive; one older than it
// cannot be re-delivered any more and is dead weight.
//
// Why a clock and not the source call: until 2026-09-08 the ledger was pruned
// with the CALL a finding named (evictOldest), which got both halves wrong. A
// busy window rolls past a call in seconds, so a retry landing after the roll
// found no ledger row and counted the finding again — or, under a new
// signature, hit findings.id UNIQUE (rejected_test.go drives exactly that).
// And a CALL-LESS finding (definition_change, version-diff) named no call at
// all, so its row was never pruned: a flapping MCP snapshot grew the ledger
// without bound.
const occurrenceTTL = time.Hour

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
	// Idempotent on the record's own id: the SAME finding record delivered
	// twice (a retried or re-sent batch) counts once — a repeat is a NEW
	// detection carrying a NEW id (see recordOccurrence). Both backends.
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
	// CallPeerHosts resolves call ids to the peer host each call was captured
	// against. Ids with no stored call — and calls stored without a host, e.g.
	// a local-process MCP server — are absent from the map rather than present
	// and empty.
	CallPeerHosts(ids []string) (map[string]string, error)
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

// The contract set is CACHED in memory by the drift processor and refreshed on
// a ticker — the 2026-08-31 owner ruling: no store read per call. That leaves a
// window between the moment a contract is uploaded, replaced or removed and the
// moment detection acts on it, and the UI's copy ("Validating from now on")
// promises there is none. These two halves close it in-process: the component
// that CHANGES the contract set announces it, and the component that CACHES it
// refreshes early. A notification, never a read — the ruling stands.
//
// In-process only, by construction. A tiered front runs the drift processor in
// a DIFFERENT process from the store pod that owns the uploads, and each pod of
// a shared-postgres deployment caches on its own; those cases converge on the
// refresh ticker instead, which is why that interval is short enough to be
// honest about.

// SpecPublisher is the announcing half, implemented by the store extension and
// called by whatever mutates a contract row (today: the UI's upload and remove
// handlers). Never blocks the caller on a subscriber.
type SpecPublisher interface {
	// NotifySpecsChanged announces that the stored contract set moved.
	NotifySpecsChanged()
}

// SpecSubscriber is the listening half, implemented by the same store
// extension and called by whatever caches contracts (today: the drift
// processor's spec cache).
type SpecSubscriber interface {
	// OnSpecsChanged registers fn, called on every subsequent announcement.
	// fn runs on the announcer's goroutine and MUST NOT block — the sanctioned
	// shape is a non-blocking send on a buffered channel.
	OnSpecsChanged(fn func())
}

// ContractServer is implemented by the store extension and answers ONE
// question the UI cannot answer for itself: does this pod serve its stored
// contracts to FRONT collectors?
//
// It exists because the document cap is a property of that HOP and of nothing
// else. A co-located drift processor reads the store in-process, crosses no
// boundary and applies no cap, so a document past MaxContractDocBytes is bound
// and validating on a single pod — and a card that called it "too large to
// serve" there would be a warning about a thing that is working, which is the
// class of lie this surface exists to end. The same row on a store pod with a
// configured `spec_endpoint` is refused at both ends of the channel and its
// edge really is unchecked on every front.
//
// Discovered the way Provider and SpecPublisher are: by type-asserting a host
// extension. An extension that predates this interface answers nothing, which
// reads as "no fronts" — the pre-tiered default, and the safe one.
type ContractServer interface {
	// ServesContracts reports whether this pod's intra-cluster contract
	// endpoint is configured and listening.
	ServesContracts() bool
}

// base holds what both backends share: the connection pool and the read path.
// Queries are written with `?` placeholders; rebind converts them to the
// backend's native style ($1..$n for postgres, identity for sqlite).
type base struct {
	db     *sql.DB
	rebind func(string) string
	// octetLength renders "the size of this TEXT column IN BYTES" for the
	// backend. It is a dialect difference with a silent wrong answer, which is
	// why it is a field rather than one portable-looking expression: sqlite's
	// length() counts CHARACTERS of a TEXT value, so a document full of
	// multi-byte UTF-8 measured smaller than the cap it had already blown,
	// while postgres's length() is the same trap and octet_length() is its
	// answer. Both are given the expression that counts bytes.
	octetLength func(column string) string
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

// ErrRejected marks a write the store REFUSED for what the record IS — a
// constraint the statement's ON CONFLICT does not absorb, or a value the
// backend cannot encode — as opposed to a write that FAILED for where the
// store is (connection gone, file locked, timeout). The same record would be
// refused again on every attempt, so the store exporter turns it into a
// permanent error (consumererror.NewPermanent) and drops the batch after ONE
// attempt instead of holding a queue consumer for max_elapsed_time. Both
// backends wrap it around InsertCall, InsertFinding and PutSpecInfo (the three
// writes the exporter makes); every other error is returned as-is and stays
// retryable.
var ErrRejected = errors.New("store: record rejected")

// classify wraps a deterministic driver rejection in ErrRejected and returns
// every other error unchanged. The two drivers spell "the record, not the
// store" differently:
//   - sqlite: primary result code SQLITE_CONSTRAINT (19 — the extended code
//     is masked off), SQLITE_TOOBIG (18) or SQLITE_MISMATCH (20);
//   - postgres: SQLSTATE class 23 (integrity constraint violation) or 22 (data
//     exception — e.g. 22021, a NUL byte in a text column).
//
// SQLITE_BUSY / SQLITE_LOCKED, class 08 (connection), 40 (transaction
// rollback, incl. deadlock) and 57 (operator intervention) are all left alone:
// those go away when the store does.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_CONSTRAINT, sqlite3.SQLITE_TOOBIG, sqlite3.SQLITE_MISMATCH:
			return fmt.Errorf("%w: %w", ErrRejected, err)
		}
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if strings.HasPrefix(pe.Code, "23") || strings.HasPrefix(pe.Code, "22") {
			return fmt.Errorf("%w: %w", ErrRejected, err)
		}
	}
	return err
}

// execer is the subset of *sql.DB / *sql.Tx the shared write helpers need.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// queryExecer is execer plus reads — what both a *sql.DB and a *sql.Tx offer,
// so a helper can run inside a backend's transaction or on its bare handle.
type queryExecer interface {
	execer
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// recordOccurrence is the finding OCCURRENCE LEDGER: one row per finding
// record ever applied, keyed by the record's own id. It is what makes
// InsertFinding idempotent on re-delivery.
//
// Why it exists: the store exporter retries a batch whose write failed (a
// store outage mid-batch), and a front re-sends a batch whose ACK it never got.
// Both hand the store the SAME finding record again. Calls survive that on
// their own — `INSERT … ON CONFLICT (id) DO NOTHING` — but a finding's second
// arrival used to look exactly like a repeat occurrence and bumped
// occurrence_count, so one drifting call was counted twice, and a partially
// applied batch re-counted everything before the record that failed. The
// finding row itself cannot carry this: it keeps the FIRST occurrence's id
// only, and forgets every repeat's.
//
// Keyed by the record id, not by (signature, source call): the drift processor
// mints a fresh id per detection, so a genuine repeat — the same call
// re-validated after a restart re-seeds from the stored snapshots — still
// counts (TestDefinitionChangeRefreshesEvidence pins that), while the same
// record twice does not. Reports false when the id was already applied; the
// caller then applies nothing. A record with no id cannot be tracked and is
// applied unconditionally, as before.
//
// Bounded by occurrenceTTL, on the store's own clock: seenAt is stamped HERE,
// at insert, and pruneOccurrences drops every row older than the TTL. It is
// deliberately not the record's DetectedAt — that is the front's clock and it
// is identical on every copy of a re-delivered record, so it says when the
// drift was detected, never how long the copy can still be in flight. Every
// kind is bounded the same way, calls or no calls (source_call_id is kept for
// diagnostics only; nothing reads it back).
func recordOccurrence(ex execer, rebind func(string) string, f model.Finding, sourceCallID *string, seenAt string) (bool, error) {
	if f.ID == "" {
		return true, nil
	}
	res, err := ex.Exec(rebind(
		`INSERT INTO finding_occurrences (id, signature, source_call_id, seen_at)
		 VALUES (?,?,?,?)
		 ON CONFLICT (id) DO NOTHING`),
		f.ID, f.Signature, nullPtr(sourceCallID), seenAt,
	)
	if err != nil {
		return false, fmt.Errorf("record finding occurrence: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// pruneOccurrences drops ledger rows the re-delivery horizon has passed —
// everything stamped before now-occurrenceTTL. Timestamps are the store's
// fixed-width ISO-8601 UTC text, which compares lexically, so this is one
// indexed range delete (idx_finding_occurrences_seen); in steady state it
// matches nothing.
//
// Run from every path that writes the ledger, so no shape leaves it unbounded:
// the eviction pass (InsertCall, MarkPromoted) and InsertFinding itself — a
// deployment whose only findings are call-less, a flapping MCP snapshot, never
// reaches the eviction path at all.
func pruneOccurrences(ex execer, rebind func(string) string, now time.Time) error {
	if _, err := ex.Exec(rebind(
		`DELETE FROM finding_occurrences WHERE seen_at < ?`),
		isoTime(now.Add(-occurrenceTTL)),
	); err != nil {
		return fmt.Errorf("prune finding occurrences: %w", err)
	}
	return nil
}

// isoTime renders the store's one timestamp format: fixed-width ISO-8601 in
// UTC, so text columns order and compare chronologically.
func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// evictOldest deletes up to batch of the oldest unpinned calls (never keepID)
// and returns how many went — the same statement on both backends, inside the
// caller's serialisation (the store mutex on sqlite, the eviction tx on
// postgres).
//
// It does NOT touch the occurrence ledger. Until 2026-09-08 it did, deleting
// the ledger rows of the calls it evicted, and that coupling is the bug
// occurrenceTTL replaces: the window rolls on traffic, the re-delivery horizon
// runs on the clock, and a retry that outlived the window found its ledger row
// already gone. pruneOccurrences owns the bound now.
func evictOldest(ex execer, rebind func(string) string, keepID string, batch int) (int, error) {
	res, err := ex.Exec(rebind(
		`DELETE FROM calls
		  WHERE seq IN (SELECT seq FROM calls WHERE pinned=0 AND id<>? ORDER BY seq ASC LIMIT ?)`),
		keepID, batch)
	if err != nil {
		return 0, fmt.Errorf("evict: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("evict: rows affected: %w", err)
	}
	return int(n), nil
}

// bumpEdgeDrift increments drift_count on the edge that owns sourceCallID,
// inside the caller's transaction — ONE helper for both backends (they had
// identical bodies modulo rebind until 2026-09-08). A call with no row, or one
// captured without a peer host (a local-process MCP server), attributes to no
// edge and is a no-op.
func bumpEdgeDrift(q queryExecer, rebind func(string) string, sourceCallID string) error {
	var peerHost, direction sql.NullString
	err := q.QueryRow(rebind(`SELECT peer_host, direction FROM calls WHERE id=?`), sourceCallID).Scan(&peerHost, &direction)
	if err == sql.ErrNoRows || (err == nil && (!peerHost.Valid || peerHost.String == "")) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup source call edge: %w", err)
	}
	if _, err := q.Exec(rebind(
		`UPDATE edges SET drift_count = drift_count + 1 WHERE peer_host=? AND direction=?`),
		peerHost.String, direction.String,
	); err != nil {
		return fmt.Errorf("bump edge drift: %w", err)
	}
	return nil
}

// marksSourceCallDrifted reports whether a finding of this kind means THE CALL
// it names departed from the contract — the question `calls.drifted` answers.

// perCallDriftKinds / marksSourceCallDrifted: the finding kinds that mean THE
// CALL a finding names departed from its contract — the question `calls.drifted`
// answers. model.PerCallDriftKinds owns the list and the reasoning (why
// output_mismatch is in and stale_client is out), because the drift processor's
// own per-call verdict (RedactedCall.Validated) is computed off the same list:
// the store's mark and the processor's stamp must never disagree.
//
// Every site that writes `calls.drifted` reads THIS slice: InsertCall (from the
// stamp, via insertDrifted), InsertFinding on both backends (via
// marksSourceCallDrifted) and latePin's repair (which expands it into the SQL
// `IN` list). A kind marked on one path and not the other makes drift depend on
// record ORDER.
var perCallDriftKinds = model.PerCallDriftKinds

func marksSourceCallDrifted(kind string) bool { return model.MarksCallDrifted(kind) }

// insertDrifted is the `drifted` a NEW call row starts with: 1 when the drift
// processor's own stamp says the call drifted. Its finding record follows in the
// same batch and InsertFinding marks the row again (idempotent), but reading the
// stamp here closes the one-record window in which a stamped-drifted call could
// still list as conforming — and keeps the two facts in agreement by
// construction rather than by arrival order.
func insertDrifted(c model.RedactedCall) int {
	if c.Validated == model.ValidatedDrifted {
		return 1
	}
	return 0
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
	// Repair the per-call drift mark FIRST, and independently of whether this
	// call still needs pinning. InsertFinding's `UPDATE calls SET drifted=1`
	// matched no row when the finding arrived first, and nothing else ever
	// recomputes the column — so without this the call is kept as evidence,
	// counted as a drift on its edge, and still rendered `conforming`. Mirror
	// InsertFinding exactly — the same per-call kinds, no more and no less, off
	// the one shared list. Idempotent (`drifted=0` guard), so a replayed call
	// cannot double anything.
	args := []any{c.ID, c.ID}
	for _, k := range perCallDriftKinds {
		args = append(args, k)
	}
	if _, err := ex.Exec(rebind(
		`UPDATE calls SET drifted=1
		  WHERE id=? AND drifted=0
		    AND EXISTS (SELECT 1 FROM findings WHERE source_call_id=? AND kind IN (`+
			placeholders(len(perCallDriftKinds))+`))`),
		args...,
	); err != nil {
		return false, fmt.Errorf("late pin: repair drifted: %w", err)
	}
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

// placeholders renders `?,?,…` for an n-element IN list, so a kind list can
// grow in ONE place (perCallDriftKinds) without a hand-counted SQL literal
// drifting out of step with it.
func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.Repeat(",?", n)[1:]
}

// rebindIdentity leaves `?` placeholders untouched (sqlite).
func rebindIdentity(q string) string { return q }

// sqliteOctetLength measures a TEXT column IN BYTES on sqlite.
//
// The cast is the whole point. sqlite's length() over a TEXT value counts
// CHARACTERS, so a tools/list full of non-ASCII tool descriptions would have
// measured well under a cap it had already blown — a size check that reads
// smaller the more multi-byte content there is. Over a BLOB, length() counts
// bytes, which is the unit the cap is written in.
func sqliteOctetLength(column string) string { return "length(CAST(" + column + " AS BLOB))" }

// pgOctetLength measures a TEXT column IN BYTES on postgres, where length()
// counts characters exactly as sqlite's does and octet_length() is the answer.
func pgOctetLength(column string) string { return "octet_length(" + column + ")" }

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
		doc       string
		drifted   bool
		validated string
	)
	// `drifted` is store-owned (set when the call produced a finding, including
	// repeat occurrences), so it lives in its column and reads patch it back in
	// — the same shape as findings' occurrence_count/last_seen. `validated` is
	// the drift processor's stamp, kept in its own column so it is queryable and
	// so a row from before the column existed reads as EMPTY (the column's
	// default) rather than whatever its frozen doc happens to say.
	err := b.db.QueryRow(b.rebind(`SELECT doc, drifted, validated FROM calls WHERE id=?`), id).Scan(&doc, &drifted, &validated)
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
	c.Validated = validated
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
	rows, err := b.db.Query(b.rebind(`SELECT doc, drifted, validated FROM calls ORDER BY seq DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.RedactedCall, 0, limit)
	for rows.Next() {
		var (
			doc       string
			drifted   bool
			validated string
		)
		if err := rows.Scan(&doc, &drifted, &validated); err != nil {
			return nil, err
		}
		var c model.RedactedCall
		if err := json.Unmarshal([]byte(doc), &c); err != nil {
			return nil, err
		}
		c.Drifted = drifted
		// The processor's verdict, column-authoritative (see GetCall).
		c.Validated = validated
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

// callPeerHostBatch caps how many ids go into one IN list. A read-API page asks
// about far fewer, but a bounded batch keeps a larger caller comfortably inside
// postgres's parameter limit.
const callPeerHostBatch = 500

// CallPeerHosts resolves call ids to their peer host, in one query per batch.
//
// It exists for the read API's finding→contract join, which pairs a finding
// with the provider card it belongs to by HOST — a finding reaches its host
// only through its source call. Doing that with GetCall per finding would
// decode a whole call document per row on a poll that repeats every few
// seconds; peer_host is its own column (it is the edge key), so this reads
// exactly that and nothing else.
//
// Ids with no stored call, and calls stored without a host, are simply missing
// from the map: callers distinguish "no host" by lookup, never by empty value.
func (b *base) CallPeerHosts(ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	seen := make(map[string]struct{}, len(ids))
	batch := make([]any, 0, callPeerHostBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		rows, err := b.db.Query(b.rebind(
			`SELECT id, peer_host FROM calls WHERE id IN (?`+strings.Repeat(",?", len(batch)-1)+`)`), batch...)
		if err != nil {
			return fmt.Errorf("call peer hosts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id   string
				host sql.NullString
			)
			if err := rows.Scan(&id, &host); err != nil {
				return fmt.Errorf("call peer hosts: %w", err)
			}
			if host.Valid && host.String != "" {
				out[id] = host.String
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("call peer hosts: %w", err)
		}
		batch = batch[:0]
		return nil
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		batch = append(batch, id)
		if len(batch) == callPeerHostBatch {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
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

// specSourceOf is the provenance PutSpecInfo records when the writer left
// Source empty. Both writers name their own (SpecSourceConfig for the self
// contract, SpecSourceObserved for an MCP snapshot); this covers the record a
// front on an image older than that sends across the tiered hop, so the store
// pod never files an observed contract as a config one again. The rule is the
// one the open-time repair applies to rows already stored that way: format
// "mcp" means it was observed on the wire, anything else PutSpecInfo writes
// came from config. Uploads never pass through here (PutUploadedSpec).
func specSourceOf(info model.SpecInfo) string {
	if info.Source != "" {
		return info.Source
	}
	if info.Format == model.SpecFormatMCP {
		return model.SpecSourceObserved
	}
	return model.SpecSourceConfig
}

// ListSpecInfos returns the loaded provider contracts (metadata only, no doc).
// `source` is read as stored: the COALESCE is a migration default for a
// database whose column predates the NOT NULL DEFAULT, never a classifier —
// the value is the writer's (see specSourceOf and each backend's open-time
// repair of pre-2026-09-07 mcp rows).
//
// The document's SIZE is measured here and returned as DocBytes. Metadata-only
// stays true — the size is an aggregate the backend computes over the column,
// not the column — and it is what lets both ends of the tiered contract channel
// recognise an over-cap row from the listing instead of by attempting the
// transfer and reading the refusal (model.MaxContractDocBytes, collector#50).
func (b *base) ListSpecInfos() ([]model.SpecInfo, error) {
	rows, err := b.db.Query(
		`SELECT integration, role, COALESCE(peer_host,''), COALESCE(edge_class,''), format, COALESCE(title,''),
		        COALESCE(version,''), COALESCE(docs_url,''), endpoints, loaded_at,
		        COALESCE(source,'config'), COALESCE(prev_version,''), COALESCE(prev_loaded_at,''),
		        ` + b.octetLength("doc") + `
		   FROM spec_infos ORDER BY role DESC, integration ASC`, // self first
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.SpecInfo, 0)
	for rows.Next() {
		var si model.SpecInfo
		if err := rows.Scan(&si.Integration, &si.Role, &si.PeerHost, &si.EdgeClass, &si.Format, &si.Title,
			&si.Version, &si.DocsURL, &si.Endpoints, &si.LoadedAt,
			&si.Source, &si.PrevVersion, &si.PrevLoadedAt, &si.DocBytes); err != nil {
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
