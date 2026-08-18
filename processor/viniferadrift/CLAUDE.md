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
correctness (FX/fees/spreads) — that would be a false-positive storm.

## Files

- `factory.go` — loads `spec_path` at construction (bad spec fails the build
  fast); precomputes version-diff findings once if `spec_v2_path` is set.
- `config.go` — frozen keys `integration_id`, `spec_path`, `spec_v2_path`
  (CONTRACTS §8).
- `processor.go` — per-batch live-vs-spec detection + one-time version-diff
  injection; appends finding records under a fresh ResourceLogs/ScopeLogs.

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
