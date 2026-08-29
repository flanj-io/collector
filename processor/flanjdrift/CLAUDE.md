# CLAUDE.md — processor/flanjdrift

Technical-adherence drift **processor**. Own Go module, wired in via
`builder-config.yaml`.

## Role

For each captured `call` record: validate the recorded response against the
provider OpenAPI spec (**live-vs-spec**, kin-openapi). At load time: diff spec
v1→v2 for **breaking changes** (**version-diff**, oasdiff). Each violation is
emitted as a **Finding** log record (`flanj.record.type=finding`) flowing
downstream to the store exporter.

**MCP (v0.5 Step C).** The processor also consumes `contract_snapshot` records
(an observed MCP `tools/list` — the **self-delivering local spec**, no config
needed) and routes MCP `tools/call` records (`flanj.transport=mcp`) through
the MCP detector instead of the OpenAPI path. Per edge it keeps the CURRENT
snapshot (calls are validated against it) and the PREVIOUS one (kept for
diffing); a changed snapshot (content hash) yields `definition_change` findings
via the `contract/diff` classifier, a drifting call yields `output_mismatch`
(structuredContent vs `outputSchema`; NO finding for a tool without one), and a
call to an unlisted tool / with args violating the current `inputSchema` yields
the **local-only** `stale_client`. Flaggability lives in
`model.Finding.Flaggable()` and is enforced by the UI relay (`403
not_flaggable`). Every observed snapshot is also emitted as a `spec_info`
record (format `"mcp"`, raw doc = the snapshot JSON) and — when a store is
co-located — upserted directly, so the Contracts tab lists the server and a
restart re-seeds the diff baseline from the store (`MCPDetector.Seed`); a
front collector without a store simply re-baselines from the next observed
list.

**Technical adherence ONLY** — fields/types/shapes/enums. Never business/economic
correctness (pricing, quantities, business rules) — that would be a false-positive storm.

**Everything is OPTIONAL** — the collector auto-discovers edges from traffic and
never requires a target/integration/spec. With no `spec_path` the processor is a
pass-through (still stamps `flanj.call.id`); capture + edge discovery work
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
`flanj.record.type=spec_info` log records (metadata JSON attribute + raw
document in the Body) in the same trailing scope as findings — on the first
batch after Start, then at most every `specInfoRefresh` (10 min). That is how a
store pod behind an `otlphttp` hop (tiered topology) populates its Contracts
tab; in single-pod mode the double write is a harmless upsert. A front
collector without a store extension is therefore never "spec-blind".

## Detection lives in `internal/drift`

- `DetectLiveVsSpec` — `openapi3filter.ValidateResponse` with `MultiError:true`,
  reconstructing the request from the stored call.
- `MCPDetector` (`mcp.go`) — `LoadSnapshot` (contract_snapshot →
  `contract.FromToolsList`, versioned by content hash, previous kept, diff via
  `contract/diff`) + `DetectCall` (the three MCP findings; SAME kin-openapi
  validator, SAME token-aware + captured-props rules, SAME signature/dedup
  convention — endpoint = the tool name).
- `DetectVersionDiff` — oasdiff `CheckBackwardCompatibility`; `Level=ERR →
  severity=breaking`, change-id → `rule`.
  - **Severity override (contract-driven):** oasdiff ships
    `response-property-enum-value-removed` at INFO; we promote it (and
    `response-mediatype-enum-value-removed`) to ERR so a removed response enum
    value is a breaking finding, matching the frozen contract + spec-v2's own
    declaration. See the comment in `versiondiff.go`.

## Shared call id

`processor.go` calls `otlpattr.EnsureCallID(lr)` before reconstructing the call,
stamping `flanj.call.id`. The exporter reads the same attribute so the
finding's `source_call_id` matches the stored call's `id`. Don't remove this.

## Tests

`go test ./internal/drift` is the oracle: golden-call live-vs-spec on
`$.response.body.amount` (integer vs string), the two spec-v1→v2 breaking
findings, and the MCP battery (`mcp_test.go`): golden snapshot + golden MCP
call → exactly one `output_mismatch`; no-`outputSchema` and
extra-undeclared-field no-finding cases; token-aware props verdicts;
`stale_client`; the classifier classes with flaggability; snapshot versioning
and seeding. `processor_test.go` here drives the same loop at the record level
(snapshot in → spec_info + findings out; HTTP pass-through untouched).
