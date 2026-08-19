# CLAUDE.md — internal/

The collector's business logic lives here as plain Go packages, **fully unit
tested** by root `go test ./...`. The OTel components under
`processor/`, `exporter/`, `extension/` are thin wrappers that delegate to these
packages — so the acceptance oracles run without a running collector.

| Package | Responsibility | Key test |
|---|---|---|
| `redact` | Redaction floor (idempotent, add-only). Governed by `contracts/redaction-vectors.json`. | `redact_test.go` (vectors + idempotency + add-only) |
| `drift` | live-vs-spec (kin-openapi) + version-diff (oasdiff). Technical adherence only. | `drift_test.go` (golden call + two breaking findings) |
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

- **`redact` is the security floor.** The vector file, not the code, is the
  contract. Never weaken idempotency or add-only.
- **`drift` is technical-only.** Never add business/economic checks.
- **`store` is the single owner.** Only the `viniferastore` extension constructs
  a `*store.Store`.
