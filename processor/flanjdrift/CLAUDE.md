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
co-located — upserted directly, so the Contracts tab lists the server.

**The MCP baseline is the STORE's, not this process's** (2026-09-07). Every
refresh offers the detector the store's `spec_infos` rows of format `"mcp"`
(`mcpbaseline.go` → `MCPDetector.Seed`), from whichever `specSource` this
processor has — the co-located store, or the store pod's contract channel on a
tiered FRONT, which now serves MCP rows too. So a restart re-seeds the diff
baseline, a shared-postgres pod learns what a sibling pod observed, and a
tiered front learns what a SIBLING FRONT observed: until this landed a front's
baseline was whatever that one process had witnessed, so a tool renamed while
front-a was watching raised nothing when the stale client called through
front-b, and restarting a front forgot the baseline outright. The conflict
rule is **newer observation wins**, by `observed_at`: a store row newer than
the live snapshot is adopted, the live one rotates to previous, and the
definition diff between them is REPORTED exactly as observing it would be —
it used to be silent, on the theory that the observing front had reported
it, but a front with no baseline observes a change and reports nothing, so a
rename first listed by a fresh front was reported by nobody (findings dedup
by signature; one occurrence per front is the honest count). The same
content converges the edge on the EARLIER stamp: two fronts' first sightings
of one list otherwise flip-flopped the store row forever. And a live snapshot
newer than the store's stays, reaching the store as the forwarded `spec_info`
record exactly as before. Findings the refresh produces have no batch to
ride, so the processor holds them for the next one (`holdFindings`). An MCP
call to an edge with no baseline kicks an early refresh, like an uncovered
REST host. A re-observed IDENTICAL list reports the row with the FIRST
observation's stamp, and the store keeps an MCP row's `loaded_at` for an
unchanged document whichever front wrote it, so `loaded_at` — the UI's
"since this snapshot" anchor and the channel's change token — moves only
when the contract does.

**A `local-process` (stdio) row seeds nobody, from either source**
(2026-09-08 — `mcpbaseline.go`, "Local-process (stdio) rows seed nobody").
Its `peer_host` is the server's `serverInfo.name`, not a host identity, so
every pod running its own subprocess of a same-named stdio server shares ONE
`spec_infos` row. #41 skipped such rows only over the store pod's channel
(`mcpSeeds.remote`), on the theory that a pod's own store holds only its own:
`storeSpecSource` reads the shared database, so on a shared-postgres
deployment it does not, and two pods observing different lists under one name
seeded each other in turn and ping-ponged `definition_change` forever over a
difference that was never drift. Each pod now keeps its own in-process stdio
baseline, which is all it can honestly judge — for stdio the observing
process is always the judging process, so there is no sibling sighting to
miss. The cost is one narrow gap: a restart no longer re-seeds a stdio
baseline, so a list that changed while the collector was down is adopted with
nothing to diff and the next client session re-establishes it. Closing it
would need the row to name the observing pod (a `spec_infos` column, a wire
field, a co-located filter) — deferred, because a missed diff across a
restart is quieter than a standing stream of false ones. The row is still
written and still listed: the Contracts tab shows the stdio server either
way.

**Technical adherence ONLY** — fields/types/shapes/enums. Never business/economic
correctness (pricing, quantities, business rules) — that would be a false-positive storm.

**Provider contracts come from the STORE, not config** (2026-08-31; `spec_path`,
`spec_v2_path` and `peer_host` were removed from CONTRACTS §8). They are uploaded
in the UI, bound to exactly ONE provider host, and read at runtime by
`speccache.go`: parsed documents keyed by peer host, refreshed on a 10s ticker,
early on first sight of an uncovered host, and — the case that matters —
**early whenever the contract set CHANGES**. **The per-call path is a map read
and nothing else** — parsing is expensive and the store is a database; neither
belongs on the hot path. An upload therefore validates at once, with no restart.

**A change is ANNOUNCED, never polled for.** The store extension carries
`store.SpecPublisher`; the UI's upload and remove handlers call it, and this
processor subscribes (`store.SpecSubscriber` → `kickRefresh`) at Start. That
matters because an upload REPLACING a bound contract, and a REMOVE, are both
cache HITS: `specs.lookup` finds the superseded (or deleted) document, so the
first-sight kick never fires and nothing else on the per-call path notices. Until
2026-09-02 those two waited out the full ticker while the UI said "Validating
from now on" and the card showed the new version as live — calls scored in that
window were stamped against the old document permanently, since captured calls
are never re-checked.

The announcement is in-process by construction. A tiered FRONT runs this
processor in a different process from the store pod the operator uploads to, and
each pod of a shared-postgres deployment caches on its own; both converge on the
ticker, which is why it is ten seconds and not sixty. A kick that arrives inside
`specRefreshFloor` is DEFERRED to the end of it, never dropped — traffic kicks
repeat every batch, but an announcement is one-shot.

Where that cache is filled FROM is the `specSource` interface, with two
implementations:
- `storeSpecSource` — the co-located store handle. Single pod, and every pod of
  a shared-postgres deployment.
- `remoteSpecSource` (`remotesource.go`) — the store pod's read-only contract
  endpoint (`flanjstore.spec_endpoint`), for a FRONT of the tiered topology,
  which runs drift but owns no store. Without it a front detects no REST drift
  however many contracts are uploaded — and judges MCP calls only against the
  lists it observed itself — and says so once at Start. Shaped like
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
  steady state on a fifty-provider front is six small requests a minute. Read
  methods are nil-safe — no contract source degrades to pass-through, never to a
  panic on the hot path.
- `mcpbaseline.go` — the MCP half of the same listing: offers the store's
  `"mcp"` rows to the detector, tracked by `loaded_at` so nothing is
  re-downloaded until a row moves. Logs `mcp baseline seeded from the store`
  on adoption — the tiered e2e lane waits on that line, since a front has no
  other observable surface.
- `remotesource.go` — the tiered topology's front-side client. Its reader
  DETECTS the 8 MiB cap (`model.MaxContractDocBytes`) instead of truncating to
  it: it reads one byte past and refuses a body that fills it, naming the
  contract and the cap. `io.LimitReader` alone stopped at the cap silently, so
  an oversized document reached the parser as a fragment and the front logged a
  PARSE failure for a SIZE problem — then detected nothing on that edge for as
  long as the document stayed big. Both reconcilers treat that like every other
  per-document failure: report it, skip the row, keep validating against what is
  already cached or seeded. Reachable only via an observed MCP `tools/list`
  (the upload path refuses a larger document before it is stored, and the self
  contract never crosses this hop), which nothing caps on its way in.

  **The refusal is recognised from the LISTING and logged on TRANSITION**
  (2026-09-08). `SpecInfo.DocBytes` carries each stored document's size, so
  `remoteSpecSource.overCap` skips an over-cap row with no request at all —
  before this, both reconcilers re-asked for a document they would be refused on
  every ten-second tick, and each attempt wrote a Warn line on this pod AND on
  the store pod, forever. `overCap` is on the SOURCE and not on the row because
  the cap belongs to the HOP: `storeSpecSource` always answers nil, so on a
  single pod (and on every pod of a shared-postgres deployment) an oversized
  document is read in-process, bound, and validating exactly as before. The
  condition is a typed `*overCapError`, which is what lets `refreshSpecs` split
  it out of the two error lists and hand it to `condition.Standing` — logged
  when it starts (`msgSpecOverCap`) and when it clears
  (`msgSpecOverCapCleared`), while every transient failure beside it still
  reports on every tick. The store pod's 413 is still honoured, as the fallback
  for a store pod on an image that predates `doc_bytes`.
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

## The per-call verdict (2026-09-07)

**Every call record that passes through this processor leaves with a stamp**
(`otlpattr.StampValidated` → `flanj.validated` + `flanj.validated.reason`,
CONTRACTS §2): `clean` / `drifted` when the call was compared to a contract,
`not-validated` + the first gate that stopped it otherwise — on EVERY branch of
the per-call path, the pass-through ones included. `no-contract` is the one that
matters: nothing in the cache for the edge *at that moment*, which is what an
upload the store already lists looks like from here until the refresh lands
(one `specRefreshFloor` on a single pod, one ticker on a tiered front, forever
on a front whose `store_pod_token` is wrong). The UI used to infer "checked"
from the store's contract list over exactly those calls and render CONFORMING;
it reads the stamp now (`ui/src/coverage.ts`), and the store keeps it
(`calls.validated`) and sets `drifted` from it on insert. `drifted` iff a
per-call finding kind (`model.PerCallDriftKinds` — one list for the stamp AND
the store's mark). REST verdicts come from `drift.JudgeLiveVsSpec`, MCP from
`MCPDetector.JudgeCall`; the `Detect*` names stay as finding-only wrappers.
Never stamp `clean` for a call nothing compared: kin-openapi's refusals that
carry no SchemaError (an undeclared status or media type, an undecodable body)
are `not-validated` / `status-undeclared` / `media-type-undeclared` / `body-not-decodable`, not
"no findings". Tests: `validated_stamp_test.go` (every branch), and
`internal/drift`'s `verdict_test.go` / `mcp_verdict_test.go` (every gate).

## Detection lives in `internal/drift`

- `DetectLiveVsSpec` — `openapi3filter.ValidateResponse` with `MultiError:true`,
  reconstructing the request from the stored call. The response media type is
  resolved against the contract RFC 6839-aware — verbatim, then parsed, then the
  base its `+json` suffix denotes (`application/problem+json` → the contract's
  `application/json` entry; the body still decodes and validates as JSON). A
  media type the contract declares under none of those names, on a status the
  contract DECLARES by its EXACT code (not a `4XX`/`5XX` range, not `default`, not absent), is a
  `content-type-mismatch` finding (one per endpoint); a gateway's `502
  text/html` on an undeclared status is not the provider's breach. Anything
  else kin-openapi refuses to judge — undeclared status, empty/truncated/
  non-JSON body — yields no finding; the per-call verdict stamp
  (`feat/per-call-validated-stamp`) records those as NOT validated, and it must
  never let them read clean. `JudgeLiveVsSpec` is `DetectLiveVsSpec` plus the
  per-call verdict (above); the processor stamps off that.
- `MCPDetector` (`mcp.go`) — `LoadSnapshot` (contract_snapshot →
  `contract.FromToolsList`, versioned by content hash, previous kept, diff via
  `contract/diff`) + `DetectCall` (the three MCP findings; SAME kin-openapi
  validator, SAME token-aware + captured-props rules, SAME signature/dedup
  convention — endpoint = the tool name) + `JudgeCall` (the same plus the
  verdict, naming the gate in `DetectCall`'s own order).
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
and seeding (the newer-wins rule). `processor_test.go` here drives the same
loop at the record level (snapshot in → spec_info + findings out; HTTP
pass-through untouched); `mcpbaseline_test.go` is the tiered defect at the
processor level (a source-only processor judges its FIRST call against the
store's baseline; live-ahead is forwarded, not overridden; restart against a
real store) — all verified red with seeding disabled.
