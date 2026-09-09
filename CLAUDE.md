# CLAUDE.md — Flanj Collector

Guidance for Claude Code (and engineers) working in this repo.

## What this repo is

The **Flanj Collector**: a single-binary OpenTelemetry Collector distribution (built with `ocb`) that
receives the SDK's OTLP, redacts defense-in-depth, **detects drift near source**, stores redacted calls in a
local store (rolling window; embedded SQLite on a PVC by default, or a shared postgres database for
multi-pod deployments — `docs/STORE.md`), and serves a **localhost Vue UI** + a flag action.
It ships and deploys as **one unit**. Public, **ELv2**.

This one repo intentionally holds three concerns that deploy together: the collector
pipeline, the local store, and the local UI.

## Role in the system

`SDK → OTLP :4318 → [otlp receiver → redaction processor → drift processor → store exporter] → store (sqlite | postgres)`.
**Tiered topology** (same image, role by config — `docs/STORE.md` "Topologies"): N stateless **front**
collectors `[otlp → redaction → drift → otlphttp]` → ONE **store pod** `[otlp → redaction → store exporter] →
store + UI` (`config/config.front.example.yaml` / `config.store.example.yaml`, baked as `/etc/flanj/front.yaml`
/ `store.yaml`).
The **UI extension** serves the embedded Vue SPA + a localhost read API
(`/api/edges|calls|findings|health|contracts|contracts/spec`), the **agent-facing drift read surface**
(a READ-ONLY MCP server at `/mcp` on the same loopback listener, 2026-09-08 — `drift_summary`,
`list_edges`, `list_findings`, `get_finding`: the same finding rows the SPA renders, built by the same
`findingRows`/`edgeRows`, with NO body, header map or full URL on any tool and the redaction floor run
once more over `expected`/`actual`/`detail` on the way out; every answer carries the per-call validation
tally so an empty list can never read as an all-clear; no tool for anything behind the browser guard —
suggest-and-approve is v4 and is NOT this) and the **control-plane relay** (CONTRACTS §5, v0.1a):
**Connect** (`/api/connect` — registers the deployment once with `cp_deploy_token`, persists the per-deployment
**collector key** in the store settings KV, never logs it; the contact confirms their email with one click),
`POST /api/flag` (Create thread — requires a Connected collector with a confirmed contact, `412 not_connected |
contact_unconfirmed` otherwise; promotes the redacted call + finding with the collector key and returns the **thread
link**) and `/api/threads…` (state summaries, owner handoff `open`, `close` / `reopen`, `replace-link`). Every
mutating relay route needs `X-Flanj-UI: 1` + JSON and rejects a foreign `Origin`. The conversation itself lives on
the CP; the collector shows thread *state* only. Headless and **outbound-only** except the localhost UI. Nothing
inbound off-host.

**No target list is configured.** Integration edges are auto-discovered from observed traffic, keyed by
(`peer.host`, `direction`), classified external vs internal (external-only surfaced on `/api/edges`). Drift
detection is an OPTIONAL enhancer, and **provider contracts are UPLOADED in the UI, never configured**
(2026-08-31 — `spec_path`/`spec_v2_path`/`peer_host` removed from CONTRACTS §8). Each upload binds to exactly
ONE provider host, stays on this collector, and is read from the store at runtime by the drift processor's spec
cache — so it validates immediately, no restart: the store extension ANNOUNCES an upload, replace or remove and
the cache refreshes on the spot (`store.SpecPublisher`/`SpecSubscriber`). Announcements are in-process, so the
two topologies the announcement cannot cross — a tiered front, and the other pods of a shared-postgres
deployment — converge on the cache's own ticker instead, within ten seconds. A call to a host with no contract is captured, not
validated, and the UI says exactly that — off the **drift processor's own per-call verdict** (`flanj.validated` +
`flanj.validated.reason`, CONTRACTS §2, 2026-09-07): every call the processor sees is stamped `clean` / `drifted` /
`not-validated`, the stamp crosses the tiered hop with the record, the store keeps it (`calls.validated`), and the UI's
contract chip READS it instead of inferring "checked" from the contract list — which said CONFORMING over calls that
went through before the processor had loaded the upload (seconds on one pod, ten on a tiered front, forever on a front
with the wrong `store_pod_token`). A call with no verdict is `not checked`, never conforming. **MCP edges (v0.5) need no spec at all**: the SDK's observed `tools/list` arrives as a
`contract_snapshot` record — the self-delivering local spec — versioned by content hash in the drift processor
(previous snapshot kept for diffing; persisted as a `spec_infos` row, format `"mcp"` / source `"observed"`, so the
Contracts tab lists the server and restarts re-seed). MCP findings: `output_mismatch` + `definition_change` (flaggable at every class — DESCRIPTION included
since qfix2-2026-08-26; a human always presses the control) and the local-only `stale_client`; the flag relay REFUSES local-only kinds server-side
(`403 not_flaggable` — CONTRACTS §4). **Since v1p4-2026-09-08 a finding needs no call to be flagged**
(the message carries the ask — `400 finding_has_no_call` is gone from the relay), and `POST
/api/edges/thread` starts a MESSAGE-ONLY thread from an edge row: no call, no finding, `evidence_count: 0`. A drift is **per endpoint** (HTTP: method+route; MCP: the tool name): findings
dedup by `signature`, so one drift = one finding (with an `occurrence_count`) = one flag.

## Stack & commands

- Go (built in Docker — **no host Go required for the artifact**) + a Vue/Vite UI (built to static, embedded
  via `embed.FS`). Pure-Go store drivers, CGO off: `modernc.org/sqlite` + `jackc/pgx/v5`.
- **The ocb version triad is the #1 build hazard** — keep identical: ocb `v0.159.0`, beta components
  `v0.159.0`, stable components (`component`, `extension`, `pdata`) `v1.65.0`; `config/confighttp` is beta (`v0.159.0`). `otlpreceiver` is **core**, not contrib.
- `docker build -t flanj-collector .` (multi-stage: node builds UI → go builds binary embedding it).
- `go test ./...` (unit + contract tests for the custom components; the component modules — e.g. `extension/flanjui` — are their own Go modules, run `go test ./...` inside them too). `cd ui && npm run dev` (UI dev server against a running collector); `npm test` (vitest, pure helpers);
  `npm run typecheck` (vue-tsc — the only check that TYPE-checks the `.vue` templates; vitest only
  loads the `.ts` files); **`npm run build` — required, not optional**: vue-tsc does NOT validate
  template STRUCTURE, so an unbalanced tag passes typecheck clean and fails only here. Skip it
  locally and a broken template surfaces at the Docker stage instead, which is the slowest place
  to find it (measured 2026-08-31). Both must pass before you push a `ui/` change.

## Layout

```
builder-config.yaml                # the ocb manifest (pins the triad; binds core receiver + custom components)
Dockerfile                         # multi-stage: ui (node) -> build (go+ocb) -> distroless
processor/flanjredaction/       # defense-in-depth redaction floor (Go; idempotent, add-only; also re-scans MCP contract snapshots)
processor/flanjdrift/           # live-vs-spec (kin-openapi) + version-diff (oasdiff) + the v0.5 MCP path
                                   # (contract_snapshot loader → output_mismatch / definition_change / stale_client); emits Finding records
exporter/flanjstore/            # writes call + finding records into the store (queued + retried, idempotent — its CLAUDE.md "Durability")
extension/flanjstore/           # SINGLE store owner (sqlite default | postgres for multi-pod); shared via host.GetExtensions()
extension/flanjui/              # localhost HTTP: embed.FS Vue SPA + read API + CP relay (connect / flag / threads)
                                   # + the AGENT-FACING read surface: a read-only MCP server at /mcp on the same
                                   # loopback listener (mcp.go — drift_summary / list_edges / list_findings / get_finding)
ui/                                # Vue/Vite SPA (Overview incl. MCP server health + local notices, Traffic live-tail incl.
                                   # MCP TOOL rows/facets, Contracts + Flag sheet — HTTP and MCP, Threads, Settings/Connect;
                                   # ui/src/mcp.ts = the v0.5 MCP deck copy, pure + vitest-covered)
contract/                          # PUBLIC transport-neutral Contract model + MCP tools/list loader;
                                   # contract/openapi — the OpenAPI loader (kin-openapi stays OUT of package contract, so an
                                   # MCP-only importer links none of it); contract/diff — the definition-diff classifier
                                   # (BREAKING/NON_BREAKING/DESCRIPTION).
                                   # v0.5 Step A; deliberately NOT internal/ — imported by mcp-drift-watch (one classifier, ever)
internal/                          # redact | drift | store | edge | promote | model | otlpattr — the unit-tested logic (internal/CLAUDE.md)
config/config.example.yaml         # annotated example config (every key frozen in CONTRACTS §8)
config/config.default.yaml         # what the IMAGE bakes at /etc/flanj/config.yaml — NEUTRAL: no identity,
                                   # no cp_base_url, so a stranger's first run claims nobody's org and Connect
                                   # says cp_not_configured instead of failing like a network fault (issue #55)
charts/flanj-collector/            # the Helm chart — the TIERED shape only (N fronts + 1 store pod), published to the
                                   # same OCI registry as the image. It renders both role configs from values (mounted at
                                   # /etc/flanj/chart/, NOT the baked /etc/flanj/*.yaml), hands both roles ONE Secret key
                                   # for FLANJ_SPEC_TOKEN, and REFUSES in values.schema.json the installs that come up
                                   # broken-looking rather than broken: no spec token, sqlite with replicas>1, a PVC on
                                   # postgres. Proven by scripts/helm-smoke.sh (kind, both backends, in CI) — `helm lint`
                                   # cannot see either of this shape's silent failures
docs/                              # CONCEPTS.md + STORE.md (backends/topologies) + DEPLOYMENT.md (shapes, flows, k8s sketches, the chart)
contracts/                         # vendored contract: CONTRACTS.md + fixtures, specs, vectors, schemas — see contracts/README.md
```

## Non-negotiables (do not regress)

1. **Single store owner.** The `flanjstore` extension owns the one store handle (`store.Store` —
   embedded SQLite by default, shared postgres for multi-pod; see `docs/STORE.md`); the exporter (writer)
   and the UI extension (reader) get it via `host.GetExtensions()`. Do not open a second handle. With
   `backend: sqlite` exactly ONE pod may own a given db file; with `backend: postgres` N pods share one
   database and every cross-pod race is resolved inside `internal/store`, never by callers.
2. **Rolling window.** Post-insert FIFO-evict oldest `pinned=0` rows over the row/byte caps → stable fill.
   **Pin on finding** (keeps the failing call reproducible); **evict-after-promote** (unpin + set `promoted_at`
   after a successful CP flag POST).
3. **Redaction is defense-in-depth**: idempotent, add-only, never double-wraps the SDK's `⟦REDACTED:…⟧` tokens
   (conform to `contracts/redaction-vectors.json` AND the cross-language parity battery
   `contracts/redaction-fixtures.json` — both language suites must produce those exact results).
4. **Technical adherence only** in detection — types/shapes/enums; never business/economic correctness.
5. **Outbound-only**, localhost UI only (the store pod's `:4318` is an intra-cluster ingest for fronts).
   The agent MCP surface is a ROUTE on that same loopback listener, never a second listener, and it is
   **read-only**: no MCP tool may write, and none may emit a raw body, a header map or a full URL.
6. **Drift runs exactly once per call, on the front.** The store pod of the tiered topology never runs
   `flanjdrift` (it would double `occurrence_count`); fronts always run it (call-id stamping). The
   store is order-independent for call/finding pairs (late pin) — nothing upstream may rely on or
   compensate for record order.
7. **Contracts flow store → front, calls flow front → store.** A front owns no store, so it reads
   contracts from the store pod's read-only `spec_endpoint` (`flanjstore`), authenticated by a shared
   token: uploaded OpenAPI contracts, and — since 2026-09-07 — the observed MCP `tools/list` snapshots
   every front forwards up, so a front's MCP baseline is the org-wide one and not what that one process
   witnessed — except a **stdio** (`local-process`) server's, which seeds nobody from any source, because
   its `peer_host` is a `serverInfo.name` and one row covers every pod's own subprocess
   (`processor/flanjdrift/mcpbaseline.go`). That listener serves contracts and nothing else — no calls, no findings, no settings —
   and it is NOT the UI: the UI stays loopback (#5). Leave `store_pod_endpoint` unset on a tiered front
   and it detects no REST drift at all, whatever has been uploaded, and judges MCP calls only against
   the lists it observed itself. **One document is capped at 8 MiB on that channel**
   (`model.MaxContractDocBytes` — the same constant the upload path enforces), and **neither end
   truncates to it**: the store pod answers `413`, the front refuses to read a prefix. A document cut
   at the cap still parses — as garbage — so the front would report a PARSE error for a SIZE problem
   and silently stop detecting on that edge. Since 2026-09-08 the row is also **measured at list
   time** (`SpecInfo.DocBytes`), which is what makes the refusal legible instead of merely correct:
   a front skips the row from the LISTING (no request, no 413), the Contracts card gives it an
   explicit `too large to serve` state naming the overage — gated on `/api/health.serves_fronts`,
   because the cap belongs to the front hop and a single pod validates the same document fine —
   and both pods log the refusal **on transition** (`internal/condition`) instead of once per
   ten-second tick, per edge, per front, forever.

## Contract

Ingest wire = `contracts/CONTRACTS.md` §2 (`flanj.*` OTLP). Ingesting `contracts/golden-otlp-call.json`
must deterministically produce the expected live-vs-spec Finding; ingesting `golden-otlp-mcp-snapshot.json`
then `golden-otlp-mcp-call.json` must produce exactly one `output_mismatch` (v0.5). Flag POST body must satisfy
`contracts/cp-flag-request.schema.json`. Runtime config keys are frozen in CONTRACTS.md §8.

## Docs & conventions

`docs/CONCEPTS.md` (public-safe overview). Deeper per-component context in each component's `CLAUDE.md`.
`git commit -s` (DCO — see CONTRIBUTING.md).
