# CLAUDE.md — processor/viniferadrift

Technical-adherence drift **processor**. Own Go module, wired in via
`builder-config.yaml`.

## Role

For each captured `call` record: validate the recorded response against the
provider OpenAPI spec (**live-vs-spec**, kin-openapi). At load time: diff spec
v1→v2 for **breaking changes** (**version-diff**, oasdiff). Each violation is
emitted as a **Finding** log record (`vinifera.record.type=finding`) flowing
downstream to the store exporter.

**Technical adherence ONLY** — fields/types/shapes/enums. Never business/economic
correctness (pricing, quantities, business rules) — that would be a false-positive storm.

**Everything is OPTIONAL** — the collector auto-discovers edges from traffic and
never requires a target/integration/spec. With no `spec_path` the processor is a
pass-through (still stamps `vinifera.call.id`); capture + edge discovery work
regardless. If `peer_host` is set, the loaded spec is scoped to that one discovered edge;
otherwise every outbound call is validated against it.

**A drift is per endpoint, not per call.** Each drifting call emits a finding
record carrying a `signature` (integration|endpoint|kind|rule|field_path); the
store dedups on it — the first call creates the finding, later calls increment
`occurrence_count` + `last_seen`. One drift ⇒ one finding ⇒ one flag.

## Files

- `factory.go` — loads `spec_path` at construction (bad spec fails the build
  fast); precomputes version-diff findings once if `spec_v2_path` is set.
- `config.go` — frozen keys `integration_id`, `spec_path`, `spec_v2_path`, `peer_host`,
  `self_spec_path`, `self_integration_id` (CONTRACTS §8). `spec_path` validates
  OUTBOUND (client) calls; `self_spec_path` is the contract THIS org publishes
  and validates INBOUND (server) responses — self findings are relabeled to
  `self_integration_id` (default `self`, must differ from `integration_id`)
  with their signature recomputed, so self and provider drift never merge.
- `processor.go` — per-batch live-vs-spec detection + one-time version-diff
  injection + rate-limited spec_info emission; appends finding + spec_info
  records under a fresh trailing ResourceLogs/ScopeLogs (calls stay ahead).

The loaded contracts (provider + self) are precomputed once as `spec_info`
records (`specInfos`, stable `loaded_at`) and reach the store two ways:
directly at Start (`PutSpecInfo` via `store.Provider`, when a store extension is
co-located — single-pod topology), AND emitted INTO the pipeline as
`vinifera.record.type=spec_info` log records (metadata JSON attribute + raw
document in the Body) in the same trailing scope as findings — on the first
batch after Start, then at most every `specInfoRefresh` (10 min). That is how a
store pod behind an `otlphttp` hop (tiered topology) populates its Contracts
tab; in single-pod mode the double write is a harmless upsert. A front
collector without a store extension is therefore never "spec-blind".

## Detection lives in `internal/drift`

- `DetectLiveVsSpec` — `openapi3filter.ValidateResponse` with `MultiError:true`,
  reconstructing the request from the stored call.
- `DetectVersionDiff` — oasdiff `CheckBackwardCompatibility`; `Level=ERR →
  severity=breaking`, change-id → `rule`.
  - **Severity override (contract-driven):** oasdiff ships
    `response-property-enum-value-removed` at INFO; we promote it (and
    `response-mediatype-enum-value-removed`) to ERR so a removed response enum
    value is a breaking finding, matching the frozen contract + spec-v2's own
    declaration. See the comment in `versiondiff.go`.

## Shared call id

`processor.go` calls `otlpattr.EnsureCallID(lr)` before reconstructing the call,
stamping `vinifera.call.id`. The exporter reads the same attribute so the
finding's `source_call_id` matches the stored call's `id`. Don't remove this.

## Tests

`go test ./internal/drift` is the oracle: golden-call live-vs-spec on
`$.response.body.amount` (integer vs string) and the two spec-v1→v2 breaking
findings.
