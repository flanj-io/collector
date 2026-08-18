# CLAUDE.md — extension/viniferastore

Store **extension** — the SINGLE OWNER of the embedded SQLite database. Own Go
module.

## Role

Opens **one** SQLite connection at Start (WAL, on a PVC), closes it at Shutdown,
and exposes it via the `store.Provider` interface (`Store() *store.Store`). The
store exporter (writer) and the UI extension (reader) both reach it through
`host.GetExtensions()`. No other component opens a connection.

## Files

- `extension.go` — lifecycle (`Start`/`Shutdown`), `Store()`. Compile-time
  assertions that it satisfies `extension.Extension` and `store.Provider`.
- `config.go` — frozen keys `db_path`, `window_max_rows`, `window_max_bytes`
  (CONTRACTS §8). `db_path` MUST be on a PVC.

## Invariants

- **Single owner.** Do not open a second connection anywhere.
- **PVC persistence.** Data must survive a restart — tested in
  `internal/store` (`TestPersistence_SurvivesReopen`).
- **Rolling window + pin-on-finding + evict-after-promote** live in
  `internal/store`; this extension just owns the handle.

## Store implementation

All storage logic (schema, ring-buffer eviction, pinning, promotion) is in
`internal/store` and tested there. This extension is a thin OTel lifecycle
wrapper.
