# CLAUDE.md — internal/

The collector's business logic lives here as plain Go packages, **fully unit
tested** by root `go test ./...`. The OTel components under
`processor/`, `exporter/`, `extension/` are thin wrappers that delegate to these
packages — so the acceptance oracles run without a running collector.

| Package | Responsibility | Key test |
|---|---|---|
| `redact` | Redaction floor (idempotent, add-only): text path (`Redact`), structural path (`RedactValue` — maps/slices/structs, keys included, PAN-as-number), schema-aware `Enhance` (ADD-only layer above the floor). Both paths also emit **captured-value field records** (`props.go`): for every WHOLE-VALUE redaction, the RFC 6901 path + pattern + the original's non-reversible props (type, code-point length, char classes) — consumed by `drift`. Governed by `contracts/redaction-vectors.json` + `contracts/redaction-fixtures.json` (cross-language parity with the TS SDK). | `redact_test.go` (vectors) + `fixtures_test.go` (parity battery, both entry points, idempotency, never-subtract law) + `recognizers_test.go` + `netban_test.go` (zero-I/O lint ban) |
| `drift` | live-vs-spec (kin-openapi) + version-diff (oasdiff). Technical adherence only. **Token-aware:** runs after the redaction floor, so schema errors whose offending scalar carries a `⟦REDACTED:…⟧` token are SKIPPED (redacted = unknown, not violated; scalar-only so container errors still fire) — EXCEPT when the call carries a matching `redaction.fields` record: then the DECIDABLE constraints (type, min/maxLength) are judged against the captured props and real violations are reported with a props-built `actual`; pattern/format/enum stay undecidable. | `drift_test.go` (golden call + two breaking findings) + `drift_token_test.go` (redacted values never drift; genuine drift next to tokens still fires; props-judged type/maxLength skip-or-fire) |
| `store` | Single SQLite store: schema, WAL, ring-buffer eviction, pin-on-finding, evict-after-promote, **edge auto-discovery** (edges table keyed by peer_host+direction) + **per-signature finding dedup**. | `store_test.go` (stable fill, pinned survive, persistence, edge discovery, drift dedup) |
| `edge` | Edge classification heuristic (external vs internal, identical to the SDK) + role/orientation from direction. | `edge_test.go` (classification + role) |
| `promote` | Builds + POSTs the CP flag body (redacts the message). | `promote_test.go` (schema conformance + headers) |
| `model` | Cross-component record types mirroring the frozen JSON Schemas. | — |
| `otlpattr` | Maps `vinifera.*` OTLP attributes ↔ records; stamps the shared `vinifera.call.id`. | — |

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
  a `*store.Store`.
