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

## Invariants

- **Never open a second store handle.** The extension is the single in-process
  owner; the exporter only holds the shared `store.Store` interface (backend —
  embedded sqlite or shared postgres — is the extension's concern). Extensions
  all start before pipeline components, so the store is already open when
  `start` runs.
- Findings before their call is fine (`InsertFinding` pins by id even if the row
  arrives moments later in the same batch — the drift processor appends findings
  after the calls in the same `plog.Logs`).

## Tests

Store behaviour (ring buffer, pin, promote) is tested in `internal/store`. This
wrapper is exercised by the runtime smoke.
