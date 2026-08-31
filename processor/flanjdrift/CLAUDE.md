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

**Provider contracts come from the STORE, not config** (2026-08-31; `spec_path`,
`spec_v2_path` and `peer_host` were removed from CONTRACTS §8). They are uploaded
in the UI, bound to exactly ONE provider host, and read at runtime by
`speccache.go`: parsed documents keyed by peer host, refreshed on a 60s ticker
and early on first sight of an uncovered host. **The per-call path is a map read
and nothing else** — parsing is expensive and the store is a database; neither
belongs on the hot path. An upload therefore validates within a tick, with no
restart.

Where that cache is filled FROM is the `specSource` interface, with two
implementations:
- `storeSpecSource` — the co-located store handle. Single pod, and every pod of
  a shared-postgres deployment.
- `remoteSpecSource` (`remotesource.go`) — the store pod's read-only contract
  endpoint (`flanjstore.spec_endpoint`), for a FRONT of the tiered topology,
  which runs drift but owns no store. Without it a front detects no REST drift
  however many contracts are uploaded, and says so once at Start. Shaped like
  the deferred CP per-domain fetch on purpose: that lands as a third
  implementation, not a third channel.

**Everything is still OPTIONAL** — the collector auto-discovers edges from
traffic and never requires a target/integration/contract. With nothing uploaded
the processor is a pass-through that still stamps `flanj.call.id`; capture and
edge discovery work regardless, and those calls are captured, not validated.

Binding is MANDATORY at upload, which is what makes the host the whole lookup. A
contract bound to the wrong host validates nothing forever while its card claims
otherwise — the old optional `peer_host` had exactly that failure mode, silently,
for every install that left it unset.

**A drift is per endpoint, not per call.** Each drifting call emits a finding
record carrying a `signature` (integration|endpoint|kind|rule|field_path); the
store dedups on it — the first call creates the finding, later calls increment
`occurrence_count` + `last_seen`. One drift ⇒ one finding ⇒ one flag.

## Files

- `factory.go` — loads `self_spec_path` at construction (a bad self spec fails
  the build fast). Provider contracts are NOT loaded here.
- `config.go` — frozen keys `integration_id`, `self_spec_path`,
  `self_integration_id`, `store_pod_endpoint`, `store_pod_token` (CONTRACTS §8).
  `self_spec_path` is the contract THIS org publishes and validates INBOUND
  (server) responses — self findings are relabeled to `self_integration_id`
  (default `self`, must differ from `integration_id`) with their signature
  recomputed, so self and provider drift never merge.
- `speccache.go` — the `specSource` interface, the co-located store
  implementation, and the parsed-document cache keyed by peer host. The refresh
  is metadata-first: it compares `loaded_at` and downloads only what moved, so
  steady state on a fifty-provider front is one small request a minute. Read
  methods are nil-safe — no contract source degrades to pass-through, never to a
  panic on the hot path.
- `remotesource.go` — the tiered topology's front-side client.
- `processor.go` — per-batch live-vs-spec detection + the refresh loop +
  rate-limited spec_info emission; appends finding + spec_info records under a
  fresh trailing ResourceLogs/ScopeLogs (calls stay ahead).

**The version diff moved.** It used to be computed once at construction from
`spec_v2_path`. With contracts uploaded, the only place a v1→v2 diff can come
from is an upload REPLACING a bound contract, so it is computed on that path (in
`extension/flanjui`) against the previous document the store keeps.

The SELF contract is precomputed once as a `spec_info` record (`specInfos`,
stable `loaded_at`) and reaches the store two ways:
directly at Start (`PutSpecInfo` via `store.Provider`, when a store extension is
co-located — single-pod topology), AND emitted INTO the pipeline as
`flanj.record.type=spec_info` log records (metadata JSON attribute + raw
document in the Body) in the same trailing scope as findings — on the first
batch after Start, then at most every `specInfoRefresh` (10 min). That is how a
store pod behind an `otlphttp` hop (tiered topology) learns a front's self
contract; in single-pod mode the double write is a harmless upsert.

A front no longer announces PROVIDER contracts upward — the direction reversed.
Uploads land on the store pod, so it is already their source of truth, and the
front reads them from it.

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
