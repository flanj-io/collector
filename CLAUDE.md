# CLAUDE.md — Vinifera Collector

Guidance for Claude Code (and engineers) working in this repo.

## What this repo is

The **Vinifera Collector**: a single-binary OpenTelemetry Collector distribution (built with `ocb`) that
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
store + UI` (`config/config.front.example.yaml` / `config.store.example.yaml`, baked as `/etc/vinifera/front.yaml`
/ `store.yaml`).
The **UI extension** serves the embedded Vue SPA + a localhost read API
(`/api/edges|calls|findings|health|contracts|contracts/spec`) and the **control-plane relay** (CONTRACTS §5, v0.1a):
**Connect** (`/api/connect` — registers the deployment once with `cp_deploy_token`, persists the per-deployment
**collector key** in the store settings KV, never logs it; the contact confirms their email with one click),
`POST /api/flag` (Create thread — requires a Connected collector with a confirmed contact, `412 not_connected |
contact_unconfirmed` otherwise; promotes the redacted call + finding with the collector key and returns the **thread
link**) and `/api/threads…` (state summaries, owner handoff `open`, `close` / `reopen`, `replace-link`). Every
mutating relay route needs `X-Vinifera-UI: 1` + JSON and rejects a foreign `Origin`. The conversation itself lives on
the CP; the collector shows thread *state* only. Headless and **outbound-only** except the localhost UI. Nothing
inbound off-host.

**No target list is configured.** Integration edges are auto-discovered from observed traffic, keyed by
(`peer.host`, `direction`), classified external vs internal (external-only surfaced on `/api/edges`). Drift
detection is an OPTIONAL enhancer (`peer_host` scopes a loaded spec to one edge; unset, every outbound call is
validated against it). **MCP edges (v0.5) need no spec at all**: the SDK's observed `tools/list` arrives as a
`contract_snapshot` record — the self-delivering local spec — versioned by content hash in the drift processor
(previous snapshot kept for diffing; persisted as a `spec_infos` row, format `"mcp"`, so the Contracts tab lists
the server and restarts re-seed). MCP findings: `output_mismatch` + `definition_change` (flaggable — DESCRIPTION-only
changes are a local warning) and the local-only `stale_client`; the flag relay REFUSES local-only kinds server-side
(`403 not_flaggable` — CONTRACTS §4). A drift is **per endpoint** (HTTP: method+route; MCP: the tool name): findings
dedup by `signature`, so one drift = one finding (with an `occurrence_count`) = one flag.

## Stack & commands

- Go (built in Docker — **no host Go required for the artifact**) + a Vue/Vite UI (built to static, embedded
  via `embed.FS`). Pure-Go store drivers, CGO off: `modernc.org/sqlite` + `jackc/pgx/v5`.
- **The ocb version triad is the #1 build hazard** — keep identical: ocb `v0.159.0`, beta components
  `v0.159.0`, stable components (`component`, `extension`, `pdata`) `v1.65.0`; `config/confighttp` is beta (`v0.159.0`). `otlpreceiver` is **core**, not contrib.
- `docker build -t vinifera-collector .` (multi-stage: node builds UI → go builds binary embedding it).
- `go test ./...` (unit + contract tests for the custom components; the component modules — e.g. `extension/viniferaui` — are their own Go modules, run `go test ./...` inside them too). `cd ui && npm run dev` (UI dev server against a running collector); `npm test` (vitest, pure helpers); `npm run build`.

## Layout

```
builder-config.yaml                # the ocb manifest (pins the triad; binds core receiver + custom components)
Dockerfile                         # multi-stage: ui (node) -> build (go+ocb) -> distroless
processor/viniferaredaction/       # defense-in-depth redaction floor (Go; idempotent, add-only; also re-scans MCP contract snapshots)
processor/viniferadrift/           # live-vs-spec (kin-openapi) + version-diff (oasdiff) + the v0.5 MCP path
                                   # (contract_snapshot loader → output_mismatch / definition_change / stale_client); emits Finding records
exporter/viniferastore/            # writes call + finding records into the store
extension/viniferastore/           # SINGLE store owner (sqlite default | postgres for multi-pod); shared via host.GetExtensions()
extension/viniferaui/              # localhost HTTP: embed.FS Vue SPA + read API + CP relay (connect / flag / threads)
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
docs/                              # CONCEPTS.md + STORE.md (backends/topologies) + DEPLOYMENT.md (shapes, flows, k8s sketches)
contracts/                         # vendored contract: CONTRACTS.md + fixtures, specs, vectors, schemas — see contracts/README.md
```

## Non-negotiables (do not regress)

1. **Single store owner.** The `viniferastore` extension owns the one store handle (`store.Store` —
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
6. **Drift runs exactly once per call, on the front.** The store pod of the tiered topology never runs
   `viniferadrift` (it would double `occurrence_count`); fronts always run it (call-id stamping). The
   store is order-independent for call/finding pairs (late pin) — nothing upstream may rely on or
   compensate for record order.

## Contract

Ingest wire = `contracts/CONTRACTS.md` §2 (`vinifera.*` OTLP). Ingesting `contracts/golden-otlp-call.json`
must deterministically produce the expected live-vs-spec Finding; ingesting `golden-otlp-mcp-snapshot.json`
then `golden-otlp-mcp-call.json` must produce exactly one `output_mismatch` (v0.5). Flag POST body must satisfy
`contracts/cp-flag-request.schema.json`. Runtime config keys are frozen in CONTRACTS.md §8.

## Docs & conventions

`docs/CONCEPTS.md` (public-safe overview). Deeper per-component context in each component's `CLAUDE.md`.
`git commit -s` (DCO — see CONTRIBUTING.md).
