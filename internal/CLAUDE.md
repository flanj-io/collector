# CLAUDE.md — internal/

The collector's business logic lives here as plain Go packages, **fully unit
tested** by root `go test ./...`. The OTel components under
`processor/`, `exporter/`, `extension/` are thin wrappers that delegate to these
packages — so the acceptance oracles run without a running collector.

| Package | Responsibility | Key test |
|---|---|---|
| `redact` | Redaction floor (idempotent, add-only): text path (`Redact`), structural path (`RedactValue` — maps/slices/structs, keys included, PAN-as-number), schema-aware `Enhance` (ADD-only layer above the floor). Both paths also emit **captured-value field records** (`props.go`): for every WHOLE-VALUE redaction, the RFC 6901 path + pattern + the original's non-reversible props (type, code-point length, char classes) — consumed by `drift`. Governed by `contracts/redaction-vectors.json` + `contracts/redaction-fixtures.json` (cross-language parity with the TS SDK). | `redact_test.go` (vectors) + `fixtures_test.go` (parity battery, both entry points, idempotency, never-subtract law) + `recognizers_test.go` + `netban_test.go` (zero-I/O lint ban) |
| `drift` | live-vs-spec (kin-openapi) + version-diff (oasdiff). Technical adherence only. **Token-aware:** runs after the redaction floor, so schema errors whose offending scalar carries a `⟦REDACTED:…⟧` token are SKIPPED (redacted = unknown, not violated; scalar-only so container errors still fire) — EXCEPT when the call carries a matching `redaction.fields` record: then the DECIDABLE constraints (type, min/maxLength) are judged against the captured props and real violations are reported with a props-built `actual`; pattern/format/enum stay undecidable. | `drift_test.go` (golden call + two breaking findings) + `drift_token_test.go` (redacted values never drift; genuine drift next to tokens still fires; props-judged type/maxLength skip-or-fire) |
| `store` | The store behind the `Store` interface, two backends: embedded sqlite (default; WAL, one pod per file) + shared postgres (N pods, one database — tx/advisory-lock coordination). Ring-buffer eviction, pin-on-finding + **late pin** (a finding may arrive before its call — `InsertCall` pins a new row a finding already references and repairs the edge drift attribution; postgres serialises the two writers per call id with an advisory xact lock, so call/finding order is never load-bearing), evict-after-promote, **edge auto-discovery** (edges table keyed by peer_host+direction), **per-signature finding dedup** (columns-authoritative counters; the stored doc stays the FIRST occurrence's JSON so the finding id is stable), the per-deployment **settings KV** (`GetSetting`/`PutSetting`, opaque string values, `connect.*` keys — lives in the store so it is shared by every pod of a deployment, never a per-pod file), and the one-shot sqlite→postgres import (`fromsqlite.go`; carries settings). | `store_test.go` (whole suite runs per backend; postgres gated on `VINIFERA_TEST_PG_DSN`) + `concurrency_pg_test.go` (multi-pod: dedup storm, replay idempotency, eviction convergence, cross-pod promote) + `fromsqlite_test.go` (import: pinned-only, stable ids, tombstone, conflicts) |
| `edge` | Edge classification heuristic (external vs internal, identical to the SDK) + role/orientation from direction. | `edge_test.go` (classification + role) |
| `promote` | The control-plane client (CONTRACTS §5, collector-facing subset): builds + POSTs the flag body (redacts the message; Bearer = the collector key), `Register` (first Connect — the only call that uses the deploy token) / `RegisterWithKey` (resend / change of contact with the collector key, CONTRACTS-CP §5.1), `Me` (incl. `confirmed_contact_email`), thread `Close` / `Reopen` / `Handoff` / `Summary`, `ReplaceLink` / `RevokeLinks` (`peeklink.go`); non-2xx answers are typed `CPError{Status, Code, Message}` so the relay can pass `412 not_connected | contact_unconfirmed` / `403 wrong_origin` through. Bearers never reach an error string. | `promote_test.go` (schema conformance + headers) + `cp_test.go` (every call: path, auth, body, error mapping) + `peeklink_test.go` |
| `model` | Cross-component record types mirroring the frozen JSON Schemas. | — |
| `otlpattr` | Maps `vinifera.*` OTLP attributes ↔ records; stamps the shared `vinifera.call.id`. | `otlpattr_test.go` (golden client + server records) |

## Why the split

`internal/*` is importable by every in-repo module (the component modules are
rooted under `github.com/vinifera-io/collector/...`), but Go's internal-visibility
rule keeps it private to this repo. Putting the logic here means:

1. `go test ./...` at the repo root runs **all five acceptance suites**.
2. The OTel wrappers stay trivial and are validated by the ocb `docker build`.

## Non-negotiables

- **`redact` is the security floor.** The fixture files (`redaction-vectors.json` +
  `redaction-fixtures.json`), not the code, are the contract. Never weaken
  idempotency or add-only.
- **`drift` is technical-only.** Never add business/economic checks.
- **`store` is the single owner.** Only the `viniferastore` extension constructs
  a `store.Store` (via `OpenSQLite`/`OpenPostgres`). sqlite = one pod per db
  file; postgres = N pods share one database, with every cross-pod race
  (dedup, pinning, eviction, DDL, migration) resolved inside this package.
