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
- `specserver.go` — the tiered topology's contract channel (`spec_endpoint` +
  `spec_token`, CONTRACTS §8): a read-only, token-gated listener on the
  cluster interface serving provider contracts bound to an edge — uploaded
  OpenAPI documents and, since 2026-09-07, observed MCP `tools/list` snapshots
  (the org-wide MCP baseline a front seeds from). `servableContract` is the ONE
  admission rule, applied to the list and the doc route alike; the self
  contract and unbound rows never cross. One document is capped at
  `model.MaxContractDocBytes` (8 MiB — the SAME constant the upload path and
  the front's reader use, so the ends of the channel agree by construction),
  and over it is a **`413`, never a truncation**: a document cut at the cap
  goes out as a 200 the front cannot tell from a whole one, parses as garbage,
  and costs that edge its detection under a PARSE error. The oversized row
  stays LISTED, and since 2026-09-08 it is listed WITH ITS SIZE
  (`SpecInfo.DocBytes`, measured by `ListSpecInfos`): a front skips it from the
  listing instead of re-requesting a document it will be refused every ten
  seconds, the doc route refuses from the metadata before reading the document
  into this pod's heap, and the Contracts card can finally say why an edge with
  a contract present validates nothing. The refusal is logged **on transition**
  (`condition.Standing`, shared by the list sweep and the doc route) —
  `msgSpecOverCap` when it starts, `msgSpecOverCapCleared` when the document
  comes back under the cap — instead of once per request, per front, forever.
- `ServesContracts()` (`store.ContractServer`) answers whether `spec_endpoint`
  is configured. The UI reads it through `/api/health.serves_fronts`, because
  the document cap belongs to THIS hop and to no other: a single pod's drift
  processor reads the same rows in-process with no cap, so an oversized
  document there is bound and validating, and a card calling it "too large to
  serve" would warn about something that works.

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
