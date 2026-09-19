package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flanj-io/collector/internal/model"
)

// MigrationSummary reports what the one-shot sqlite → postgres import did.
type MigrationSummary struct {
	Ran         bool // false = no legacy file found (the steady state)
	PinnedCalls int
	Findings    int
	Edges       int
	Settings    int
	Contracts   int
	// MCPCatalogues counts observed MCP catalogues imported — from the legacy
	// file's mcp_catalogues table, and from mcp rows a file older than that
	// table still keeps in spec_infos.
	MCPCatalogues int
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
// derives from the finding id), edges (discovery history), and settings (the
// per-deployment KV — e.g. the Connect key — which must not be lost on a backend
// switch), and spec_infos (the UPLOADED provider contracts).
//
// spec_infos used to be skipped, justified by "the drift processor re-records
// loaded contracts at every Start". That held only while provider contracts
// came from `flanjdrift.spec_path`. Since that key was removed an uploaded
// contract exists ONLY as a spec_infos row, and this function renames the
// source file to `<path>.migrated` — so skipping the table destroyed the only
// copy and tombstoned its source in one step, silently returning every provider
// to "captured, not validated".
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
	if sum.Settings, err = copySettings(src, tx); err != nil {
		return sum, err
	}
	// The catalogue table first: in a file written by a build that has it,
	// it is where the catalogues live; copySpecInfos then adds any mcp row a
	// file older than it (or a rollback) left in spec_infos, newer-wins.
	if sum.MCPCatalogues, err = copyMCPCatalogues(src, tx); err != nil {
		return sum, err
	}
	var movedMCP int
	if sum.Contracts, movedMCP, err = copySpecInfos(src, tx); err != nil {
		return sum, err
	}
	sum.MCPCatalogues += movedMCP
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
	// `validated` arrived after `drifted`, and the legacy file is opened
	// read-only and never migrated — so ask before selecting it. A file from
	// before verdicts existed carries '' for every row, which is exactly what
	// the column's default would have given them.
	validatedCol := "''"
	if has, err := sqliteHasColumn(src, "calls", "validated"); err != nil {
		return 0, fmt.Errorf("migrate-from-sqlite: inspect calls columns: %w", err)
	} else if has {
		validatedCol = "validated"
	}
	rows, err := src.Query(
		`SELECT id, captured_at, integration, peer_host, direction, edge_class, method, route,
		        status_code, request_id, idem_key, trace_id, byte_size, pinned, drifted, ` + validatedCol + `, promoted_at, doc
		   FROM calls WHERE pinned=1 ORDER BY seq ASC`)
	if err != nil {
		return 0, fmt.Errorf("migrate-from-sqlite: read pinned calls: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id, capturedAt, integration, method, route, doc string
			validated                                       string
			peerHost, direction, edgeClass                  sql.NullString
			requestID, idemKey, traceID, promotedAt         sql.NullString
			statusCode, pinned, drifted                     int
			byteSize                                        int64
		)
		if err := rows.Scan(&id, &capturedAt, &integration, &peerHost, &direction, &edgeClass, &method, &route,
			&statusCode, &requestID, &idemKey, &traceID, &byteSize, &pinned, &drifted, &validated, &promotedAt, &doc); err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: scan call: %w", err)
		}
		res, err := tx.Exec(
			`INSERT INTO calls
			  (id, captured_at, integration, peer_host, direction, edge_class, method, route, status_code, request_id, idem_key, trace_id, byte_size, pinned, drifted, validated, promoted_at, doc)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			 ON CONFLICT (id) DO NOTHING`,
			id, capturedAt, integration, peerHost, direction, edgeClass, method, route,
			statusCode, requestID, idemKey, traceID, byteSize, pinned, drifted, validated, promotedAt, doc,
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

// copySettings carries the per-deployment KV. DO NOTHING on conflict: a value
// already set on the postgres side (e.g. by a pod that Connected after the
// switch) wins over the legacy file's.
func copySettings(src *sql.DB, tx *sql.Tx) (int, error) {
	rows, err := src.Query(`SELECT key, value, updated_at FROM settings`)
	if err != nil {
		// A legacy file from before the settings table existed has nothing to carry.
		if isNoSuchTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("migrate-from-sqlite: read settings: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var key, value, updatedAt string
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: scan setting: %w", err)
		}
		res, err := tx.Exec(
			`INSERT INTO settings (key, value, updated_at) VALUES ($1,$2,$3) ON CONFLICT (key) DO NOTHING`,
			key, value, updatedAt,
		)
		if err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: insert setting %q: %w", key, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, rows.Err()
}

// isNoSuchTable reports sqlite's "no such table" error (older legacy files).
func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// sqliteHasColumn reports whether a legacy sqlite table carries a column — the
// import reads the source as-is, so a column added after the file was written
// cannot be assumed.
func sqliteHasColumn(src *sql.DB, table, column string) (bool, error) {
	var n int
	if err := src.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// copySpecInfos carries the uploaded provider contracts — metadata AND the
// documents themselves. A front reads the document over the store pod's spec
// endpoint, so a metadata-only copy would list contracts that validate nothing.
// prev_doc/prev_version/prev_loaded_at come too: they are the evidence behind
// the version-diff findings that copyFindings just carried over.
//
// An mcp row in the legacy spec_infos (every file written before the MCP
// catalogue table existed) goes to mcp_catalogues, never spec_infos: the
// destination's spec_infos holds contracts only. Returns the two counts apart.
func copySpecInfos(src *sql.DB, tx *sql.Tx) (contracts, catalogues int, err error) {
	// source_url is read only when the legacy file HAS it — the same
	// probe-then-select shape the rest of this import uses, for the same
	// reason. The source is opened mode=ro, so the additive widening every
	// other reader leans on (specInfoAddedColumns, applied at open) never runs
	// here, and the upgrade this migration exists to serve is exactly the one
	// where nothing else ran it either: a new binary, a backend flipped to
	// postgres, and a db file last written by the build before this one.
	// Naming the column unconditionally would turn that into
	// "no such column: source_url" — which ABORTS THE START, because a failed
	// migration must never be silent — for one provenance field, on the path
	// whose whole purpose is not abandoning evidence.
	//
	// A probe that errors reads as absent: the cost is one empty URL on one
	// card, and the alternative is losing the migration.
	hasSourceURL, _ := sqliteHasColumn(src, "spec_infos", "source_url")
	sourceURLCol := `'' AS source_url`
	if hasSourceURL {
		sourceURLCol = `COALESCE(source_url,'')`
	}
	// server_command (2026-09-18) — the same probe, for the same reason: a file
	// last written by a build before this column must still import, with the
	// launch line simply empty (the next observed tools/list writes it again).
	hasServerCommand, _ := sqliteHasColumn(src, "spec_infos", "server_command")
	serverCommandCol := `'' AS server_command`
	if hasServerCommand {
		serverCommandCol = `COALESCE(server_command,'')`
	}
	rows, err := src.Query(
		`SELECT integration, role, peer_host, edge_class, format, title, version, docs_url,
		        endpoints, loaded_at, doc, source, ` + sourceURLCol + `, ` + serverCommandCol + `,
		        prev_doc, prev_version, prev_loaded_at
		   FROM spec_infos`)
	if err != nil {
		return 0, 0, fmt.Errorf("migrate-from-sqlite: read contracts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			integration, format, loadedAt, doc, source   string
			role                                         string
			sourceURL, serverCommand                     string
			peerHost, edgeClass, title, version, docsURL sql.NullString
			prevDoc, prevVersion, prevLoadedAt           sql.NullString
			endpoints                                    int
		)
		if err := rows.Scan(&integration, &role, &peerHost, &edgeClass, &format, &title, &version,
			&docsURL, &endpoints, &loadedAt, &doc, &source, &sourceURL, &serverCommand, &prevDoc, &prevVersion, &prevLoadedAt); err != nil {
			return contracts, catalogues, fmt.Errorf("migrate-from-sqlite: scan contract: %w", err)
		}
		if format == model.SpecFormatMCP {
			// The 2026-09-07 repair, applied on the way: a file older than it
			// filed observed snapshots under the column default 'config'.
			if source == "" || source == model.SpecSourceConfig {
				source = model.SpecSourceObserved
			}
			ok, err := importMCPCatalogue(tx, integration, role, peerHost, edgeClass, title, version, docsURL,
				endpoints, loadedAt, doc, source, serverCommand)
			if err != nil {
				return contracts, catalogues, err
			}
			if ok {
				catalogues++
			}
			continue
		}
		res, err := tx.Exec(
			`INSERT INTO spec_infos
			  (integration, role, peer_host, edge_class, format, title, version, docs_url,
			   endpoints, loaded_at, doc, source, source_url, server_command, prev_doc, prev_version, prev_loaded_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			 ON CONFLICT (integration) DO NOTHING`,
			integration, role, peerHost, edgeClass, format, title, version, docsURL,
			endpoints, loadedAt, doc, source, nullStr(sourceURL), nullStr(serverCommand), prevDoc, prevVersion, prevLoadedAt,
		)
		if err != nil {
			return contracts, catalogues, fmt.Errorf("migrate-from-sqlite: insert contract %s: %w", integration, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			contracts++
		}
	}
	return contracts, catalogues, rows.Err()
}

// sqliteHasTable reports whether a legacy sqlite file has a table — a file
// written before mcp_catalogues existed does not, and naming it would abort
// the start (the source is opened mode=ro, so nothing ever creates it there).
func sqliteHasTable(src *sql.DB, table string) (bool, error) {
	var n int
	if err := src.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// copyMCPCatalogues carries the legacy file's mcp_catalogues table, when it
// has one. A probe that errors reads as absent, as the column probes do.
func copyMCPCatalogues(src *sql.DB, tx *sql.Tx) (int, error) {
	if has, _ := sqliteHasTable(src, "mcp_catalogues"); !has {
		return 0, nil
	}
	rows, err := src.Query(
		`SELECT integration, role, peer_host, edge_class, title, version, docs_url,
		        endpoints, loaded_at, doc, source, COALESCE(server_command,'')
		   FROM mcp_catalogues`)
	if err != nil {
		return 0, fmt.Errorf("migrate-from-sqlite: read mcp catalogues: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			integration, role, loadedAt, doc, source, serverCommand string
			peerHost, edgeClass, title, version, docsURL            sql.NullString
			endpoints                                               int
		)
		if err := rows.Scan(&integration, &role, &peerHost, &edgeClass, &title, &version, &docsURL,
			&endpoints, &loadedAt, &doc, &source, &serverCommand); err != nil {
			return n, fmt.Errorf("migrate-from-sqlite: scan mcp catalogue: %w", err)
		}
		ok, err := importMCPCatalogue(tx, integration, role, peerHost, edgeClass, title, version, docsURL,
			endpoints, loadedAt, doc, source, serverCommand)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, rows.Err()
}

// importMCPCatalogue writes one imported catalogue. Newer observation wins
// (the same rule the open-time move applies), which keeps the import
// retry-safe: a re-run finds every row already at its own loaded_at and
// changes nothing.
func importMCPCatalogue(tx *sql.Tx, integration, role string, peerHost, edgeClass, title, version, docsURL sql.NullString,
	endpoints int, loadedAt, doc, source, serverCommand string) (bool, error) {
	res, err := tx.Exec(
		`INSERT INTO mcp_catalogues (`+mcpCatalogueColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (integration) DO UPDATE SET
		   role=excluded.role, peer_host=excluded.peer_host, edge_class=excluded.edge_class, title=excluded.title,
		   version=excluded.version, docs_url=excluded.docs_url, endpoints=excluded.endpoints,
		   loaded_at=excluded.loaded_at, doc=excluded.doc, source=excluded.source, server_command=excluded.server_command
		 WHERE excluded.loaded_at > mcp_catalogues.loaded_at`,
		integration, role, peerHost, edgeClass, title, version, docsURL,
		endpoints, loadedAt, doc, source, nullStr(serverCommand),
	)
	if err != nil {
		return false, fmt.Errorf("migrate-from-sqlite: insert mcp catalogue %s: %w", integration, err)
	}
	c, _ := res.RowsAffected()
	return c > 0, nil
}
