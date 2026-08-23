# CLAUDE.md — exporter/viniferastore

Store **exporter** — the WRITER side of the store. Own Go module.

## Role

Terminal stage of the logs pipeline. Writes `call` records as `RedactedCall`
rows and `finding` records as `Finding` rows. It does **not** open the database —
it discovers the single-owner `viniferastore` **extension** via
`host.GetExtensions()` at Start and writes through it.

`… → viniferadrift → [viniferastore exporter] → store: sqlite | postgres (owned by the extension)`

## Files

- `factory.go` — `NewFactory()`, type `viniferastore` (same type name as the
  extension; that is fine — different component kinds). Logs exporter via
  `exporterhelper.NewLogs` with `WithStart`.
- `exporter.go` — `start` resolves the store (`store.Provider`); `consumeLogs`
  dispatches by `vinifera.record.type`. `InsertFinding` pins the source call
  (pin-on-finding).
- `config.go` — empty `Config` (nothing to tune; the extension owns the store).

## Invariants

- **Never open a second store handle.** The extension is the single in-process
  owner; the exporter only holds the shared `store.Store` interface (backend —
  embedded sqlite or shared postgres — is the extension's concern). Extensions
  all start before pipeline components, so the store is already open when
  `start` runs.
- **Record order is not load-bearing.** Within one batch the drift processor
  appends findings AFTER the calls (trailing `ResourceLogs`), so calls insert
  first; but across the front→store hop a finding can still outrun its call
  (re-delivered partial batch, cross-request reordering, a call re-sent after
  eviction). The store handles that itself: `InsertFinding` pins the source
  call if it exists, and `InsertCall` late-pins a new row that a finding already
  references (+ repairs the edge drift attribution) — see `internal/store`
  `latePin`. The exporter never reorders or buffers to compensate.

## Tests

Store behaviour (ring buffer, pin, promote) is tested in `internal/store`. This
wrapper is exercised by the runtime smoke.
