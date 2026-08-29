# CLAUDE.md — extension/flanjstore

Store **extension** — the SINGLE in-process OWNER of the store handle. Own Go
module.

## Role

Opens the configured backend at Start, closes it at Shutdown, and exposes it via
the `store.Provider` interface (`Store() store.Store`). The store exporter
(writer), the UI extension (reader), and the drift processor (spec metadata) all
reach it through `host.GetExtensions()`. No other component opens a handle.

Two backends (`docs/STORE.md` is the user-facing guide):

- `backend: sqlite` (default) — embedded, one WAL file on a PVC, exactly ONE
  pod per file.
- `backend: postgres` — a shared external database; N collector pods write to
  it concurrently. When `db_path` is also set and the file exists, Start first
  runs the ONE-SHOT sqlite import (`store.MigrateFromSQLite`: pinned calls +
  findings + edges, then rename to `*.migrated`); an import failure ABORTS
  Start — visible crash loop over silent evidence loss, and the import is
  retry-safe.

## Files

- `extension.go` — lifecycle (`Start`/`Shutdown`), the backend switch, the
  migration hook, redacted-DSN logging, `Store()`. Compile-time assertions that
  it satisfies `extension.Extension` and `store.Provider`.
- `config.go` — frozen keys `backend`, `dsn`, `db_path`, `window_max_rows`,
  `window_max_bytes` (CONTRACTS §8). Validation is backend-conditional:
  sqlite ⇒ `db_path` required (PVC); postgres ⇒ `dsn` required.

## Invariants

- **Single in-process owner.** Do not open a second handle anywhere.
- **sqlite: one pod per db file. postgres: N pods per database** — every
  cross-pod race is resolved inside `internal/store`, never by callers.
- **Never log the raw DSN** — only `redactDSN`'s output.
- **PVC persistence (sqlite).** Data must survive a restart — tested in
  `internal/store` (`TestPersistence_SurvivesReopen`, both backends).
- **Rolling window + pin-on-finding + evict-after-promote** live in
  `internal/store`; this extension just owns the handle.

## Store implementation

All storage logic (schema, ring-buffer eviction, pinning, promotion, cross-pod
coordination, the sqlite import) is in `internal/store` and tested there. This
extension is a thin OTel lifecycle wrapper.
