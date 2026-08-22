package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// MigrationSummary reports what the one-shot sqlite → postgres import did.
type MigrationSummary struct {
	Ran         bool // false = no legacy file found (the steady state)
	PinnedCalls int
	Findings    int
	Edges       int
}

// MigrateFromSQLite copies the durable-value rows of a legacy embedded sqlite
// store into the postgres store, then renames the file to "<path>.migrated" so
// the import runs exactly once. It is the backend-switch upgrade path: an
// operator changes backend to postgres, keeps db_path pointing at the old
// file, and no evidence is silently abandoned.
//
// What is copied, in original seq order (relative age preserved under the new
// identities): pinned calls (the evidence findings reference), ALL findings
// (dedup state: stable ids, occurrence counts — the flag idempotency key
// derives from the finding id), and edges (discovery history). spec_infos are
// skipped — the drift processor re-records loaded contracts at every Start.
//
// Every insert is ON CONFLICT DO NOTHING and the rename happens only after
// commit, so the whole operation is retry-safe: any failure aborts the
// collector start (visible, not silent evidence loss) and the next start
// simply runs it again.
func MigrateFromSQLite(dst Store, sqlitePath string) (MigrationSummary, error) {
	var sum MigrationSummary
	pg, ok := dst.(*postgresStore)
	if !ok {
		return sum, errors.New("migrate-from-sqlite: destination store is not the postgres backend")
	}
	if _, err := os.Stat(sqlitePath); errors.Is(err, os.ErrNotExist) {
		return sum, nil // already migrated (renamed) or never existed
	} else if err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: stat %s: %w", sqlitePath, err)
	}
	sum.Ran = true

	src, err := sql.Open("sqlite", "file:"+sqlitePath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: open source: %w", err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)
	if err := src.Ping(); err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: source %s is not a readable sqlite database: %w", sqlitePath, err)
	}

	tx, err := pg.db.Begin()
	if err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Serialise accidental concurrent migrators (each pod has its own file, but
	// two pods importing simultaneously should queue, not interleave).
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, pgLockMigrate); err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: lock: %w", err)
	}

	if sum.PinnedCalls, err = copyPinnedCalls(src, tx); err != nil {
		return sum, err
	}
	if sum.Findings, err = copyFindings(src, tx); err != nil {
		return sum, err
	}
	if sum.Edges, err = copyEdges(src, tx); err != nil {
		return sum, err
	}
	if err := tx.Commit(); err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: commit: %w", err)
	}
	_ = src.Close()

	// Rename only after the commit: a crash in between re-runs the import,
	// which the ON CONFLICT DO NOTHING inserts absorb. The WAL sidecars are
	// renamed too so no orphans linger next to the tombstone.
	if err := os.Rename(sqlitePath, sqlitePath+".migrated"); err != nil {
		return sum, fmt.Errorf("migrate-from-sqlite: rename source: %w", err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(sqlitePath + sidecar); err == nil {
			_ = os.Rename(sqlitePath+sidecar, sqlitePath+sidecar+".migrated")
		}
	}
	return sum, nil
}

func copyPinnedCalls(src *sql.DB, tx *sql.Tx) (int, error) {
	rows, err := src.Query(
		`SELECT id, captured_at, integration, peer_host, direction, edge_class, method, route,
		        status_code, request_id, idem_key, trace_id, byte_size, pinned, promoted_at, doc
		   FROM calls WHERE pinned=1 ORDER BY seq ASC`)
	if err != nil {
		return 0, fmt.Errorf("migrate-from-sqlite: read pinned calls: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id, capturedAt, integration, method, route, doc      string
			peerHost, direction, edgeClass                       sql.NullString
			requestID, idemKey, traceID, promotedAt              sql.NullString
			statusCode, pinned                                   int
			byteSize                                             int64
		)
		if err := rows.Scan(&id, &capturedAt, &integration, &peerHost, &direction, &edgeClass, &method, &route,
			&statusCode, &requestID, &idemKey, &traceID, &byteSize, &pinned, &promotedAt, &doc); err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: scan call: %w", err)
		}
		res, err := tx.Exec(
			`INSERT INTO calls
			  (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, promoted_at, doc)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			 ON CONFLICT (id) DO NOTHING`,
			id, capturedAt, integration, peerHost, direction, edgeClass, method, route,
			statusCode, requestID, idemKey, traceID, byteSize, pinned, promotedAt, doc,
		)
		if err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: insert call %s: %w", id, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, rows.Err()
}

func copyFindings(src *sql.DB, tx *sql.Tx) (int, error) {
	rows, err := src.Query(
		`SELECT id, signature, kind, severity, integration, endpoint, rule, source_call_id,
		        occurrence_count, first_seen, last_seen, detected_at, doc
		   FROM findings ORDER BY seq ASC`)
	if err != nil {
		return 0, fmt.Errorf("migrate-from-sqlite: read findings: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id, signature, kind, severity, integration, endpoint, rule string
			firstSeen, lastSeen, detectedAt, doc                       string
			sourceCallID                                               sql.NullString
			occ                                                        int
		)
		if err := rows.Scan(&id, &signature, &kind, &severity, &integration, &endpoint, &rule, &sourceCallID,
			&occ, &firstSeen, &lastSeen, &detectedAt, &doc); err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: scan finding: %w", err)
		}
		res, err := tx.Exec(
			`INSERT INTO findings
			  (id, signature, kind, severity, integration, endpoint, rule, source_call_id, occurrence_count, first_seen, last_seen, detected_at, doc)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 ON CONFLICT (signature) DO NOTHING`,
			id, signature, kind, severity, integration, endpoint, rule, sourceCallID,
			occ, firstSeen, lastSeen, detectedAt, doc,
		)
		if err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: insert finding %s: %w", id, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, rows.Err()
}

func copyEdges(src *sql.DB, tx *sql.Tx) (int, error) {
	rows, err := src.Query(
		`SELECT peer_host, direction, role, class, first_seen, last_seen, call_count, drift_count FROM edges`)
	if err != nil {
		return 0, fmt.Errorf("migrate-from-sqlite: read edges: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			peerHost, direction, role, class, firstSeen, lastSeen string
			callCount, driftCount                                 int64
		)
		if err := rows.Scan(&peerHost, &direction, &role, &class, &firstSeen, &lastSeen, &callCount, &driftCount); err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: scan edge: %w", err)
		}
		res, err := tx.Exec(
			`INSERT INTO edges (peer_host, direction, role, class, first_seen, last_seen, call_count, drift_count)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (peer_host, direction) DO NOTHING`,
			peerHost, direction, role, class, firstSeen, lastSeen, callCount, driftCount,
		)
		if err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: insert edge %s/%s: %w", peerHost, direction, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, rows.Err()
}
