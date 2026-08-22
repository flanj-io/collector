# CLAUDE.md — Vinifera Collector

Guidance for Claude Code (and engineers) working in this repo.

## What this repo is

The **Vinifera Collector**: a single-binary OpenTelemetry Collector distribution (built with `ocb`) that
receives the SDK's OTLP, redacts defense-in-depth, **detects drift near source**, stores redacted calls in a
local store (rolling window; embedded SQLite on a PVC by default, or a shared postgres database for
multi-pod deployments — `docs/STORE.md`), and serves a **localhost Vue UI** + a flag action.
It ships and deploys as **one unit**. Public, **ELv2**.

This one repo intentionally holds three concerns that deploy together (per the build spec): the collector
pipeline, the local store, and the local UI.

## Role in the system

`SDK → OTLP :4318 → [otlp receiver → redaction processor → drift processor → store exporter] → store (sqlite | postgres)`.
The **UI extension** serves the embedded Vue SPA + a localhost read API
(`/api/edges|calls|findings|health|contracts`) and a `POST /api/flag` that promotes a redacted call to the control plane
(`POST /api/v1/flags`). Headless and **outbound-only** except the localhost UI. Nothing inbound off-host.

**No target list is configured.** Integration edges are auto-discovered from observed traffic, keyed by
(`peer.host`, `direction`), classified external vs internal (external-only surfaced on `/api/edges`). Drift
detection is an OPTIONAL enhancer matched to an edge by host. A drift is **per endpoint**: findings dedup by
`signature`, so one drift = one finding (with an `occurrence_count`) = one flag.

## Stack & commands

- Go (built in Docker — **no host Go required for the artifact**) + a Vue/Vite UI (built to static, embedded
  via `embed.FS`). Pure-Go store drivers, CGO off: `modernc.org/sqlite` + `jackc/pgx/v5`.
- **The ocb version triad is the #1 build hazard** — keep identical: ocb `v0.159.0`, beta components
  `v0.159.0`, stable components (`extension`, `config/confighttp`) `v1.65.0`. `otlpreceiver` is **core**, not contrib.
- `docker build -t vinifera-collector .` (multi-stage: node builds UI → go builds binary embedding it).
- `go test ./...` (unit + contract tests for the custom components). `cd ui && yarn dev` (UI dev server against a running collector).

## Layout (target)

```
builder-config.yaml                # the ocb manifest (pins the triad; binds core receiver + custom components)
Dockerfile                         # multi-stage: ui (node) -> build (go+ocb) -> distroless
processor/viniferaredaction/       # defense-in-depth redaction floor (Go; idempotent, add-only)
processor/viniferadrift/           # live-vs-spec (kin-openapi) + version-diff (oasdiff); emits Finding records
exporter/viniferastore/            # writes call + finding records into the store
extension/viniferastore/           # SINGLE store owner (sqlite default | postgres for multi-pod); shared via host.GetExtensions()
extension/viniferaui/              # localhost HTTP: embed.FS Vue SPA + read API + POST /api/flag -> CP
ui/                                # Vue/Vite SPA (Overview, Traffic live-tail, provider Contracts, "flag this")
contracts/                         # vendored fixtures (golden OTLP, specs, vectors, schemas)
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
5. **Outbound-only**, localhost UI only.

## Contract

Ingest wire = `contracts/CONTRACTS.md` §2 (`vinifera.*` OTLP). Ingesting `contracts/golden-otlp-call.json`
must deterministically produce the expected live-vs-spec Finding. Flag POST body must satisfy
`contracts/cp-flag-request.schema.json`. Runtime config keys are frozen in CONTRACTS.md §8.

## Docs & conventions

`docs/CONCEPTS.md` (public-safe overview). Deeper per-component context in each component's `CLAUDE.md`.
`git commit -s` (DCO — see CONTRIBUTING.md).
