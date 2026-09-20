<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/flanj-io/collector/main/docs/brand/flanj-lockup-dark.svg">
  <img alt="Flanj" src="https://raw.githubusercontent.com/flanj-io/collector/main/docs/brand/flanj-lockup.svg" width="166" height="48">
</picture>

# Flanj collector

**Your integration didn't break. It started being wrong.**

Every call succeeded. That's why nothing caught it.

[![License: Elastic License 2.0](https://img.shields.io/badge/license-Elastic%202.0-1f2933)](LICENSE)
[![Docker Hub: flanj/collector](https://img.shields.io/docker/v/flanj/collector?sort=semver&label=docker%20hub&color=1f2933)](https://hub.docker.com/r/flanj/collector)
[![Helm chart: oci://registry-1.docker.io/flanj/flanj-collector](https://img.shields.io/badge/helm%20chart-oci%3A%2F%2Fregistry--1.docker.io%2Fflanj%2Fflanj--collector-1f2933)](charts/flanj-collector/README.md)
[![CI](https://github.com/flanj-io/collector/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/flanj-io/collector/actions/workflows/ci.yml)

The self-hosted side of Flanj: an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/)
distribution that receives the calls the [SDK](https://github.com/flanj-io/sdk) already redacted at source,
redacts them once more, validates them against the provider's contract to detect drift, keeps a rolling
window of redacted calls on your own host and serves a local UI. Raw calls never leave your environment.

Headless and outbound-only apart from the localhost UI. Ships and deploys as one unit: collector, store and
UI in one binary.

- OTLP receiver → redaction processor → drift detection → local store (a rolling window; embedded SQLite on a
  PVC by default, or a shared Postgres database — see [docs/STORE.md](docs/STORE.md)).
- Scales as N stateless front collectors forwarding (OTLP) to one store pod — the same image in two roles,
  chosen by config ([docs/STORE.md](docs/STORE.md) "Topologies"; deploy shapes and Kubernetes sketches in
  [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)).
- Local Vue UI: **Overview** (health and drift findings with the correlation keys that make a finding
  actionable), **Traffic** (live tail of redacted calls), **Contracts** (the loaded specs; each finding has
  **Flag this** → one sheet → **Create thread** → a **thread link** you paste into the channel the two teams
  already share), **Threads** (the state of every thread you created: turn label, opens, Open / Close /
  Reopen / Replace link) and **Settings** (**Connect** — a collector name and a one-click-confirmed contact email;
  required to create thread links, never to view your own data).

A detection becomes something you can act on with the provider. The SDK is Apache-2.0; this collector is
Elastic License 2.0 (source-available); the network layer that carries threads between the two teams is
hosted.

## Run it on a laptop

```bash
docker pull flanj/collector:v0.1.0
```

```bash
mkdir -p ./flanj-data
docker run -d --name flanj \
  --user "$(id -u):$(id -g)" \
  -v "$PWD/flanj-data:/data" \
  -p 4318:4318 -p 5335:5336 \
  flanj/collector:v0.1.0
```

The image is `linux/amd64` and `linux/arm64`. `:latest` tracks the newest release; pin the version tag for
anything you deploy. To build it yourself instead: `docker build -t flanj-collector .`, then substitute
that name below.

`:4318` is the OTLP ingest your app points at. `/data` holds the embedded SQLite store — bind-mount it and
run as yourself, or the store cannot open its file (the image runs as `nonroot`, and a fresh Docker *named*
volume is root-owned: that combination fails at start with `unable to open database file (14)`).

**The UI binds container loopback, by design, and that is not a setting to relax.** The collector is
outbound-only; nothing it serves is reachable off-host. `-p 5335:5335` therefore publishes nothing. Bridge
it from *inside* the network namespace instead, which is what Flanj's own integration harness does.
The port is published on the collector above because a container
sharing another's network namespace cannot publish its own:

```bash
docker run -d --name flanj-ui --network container:flanj \
  alpine/socat TCP-LISTEN:5336,fork,reuseaddr TCP:127.0.0.1:5335
```

Then open <http://localhost:5335>; health is at <http://localhost:5335/api/health>. On Linux,
`--network host` works instead of the sidecar (`localhost:5335` is then the same loopback); on Docker
Desktop it is not equivalent, so use the sidecar. In Kubernetes it is `kubectl port-forward <pod>
5335:5335`. Either way the bind stays loopback: tunnel to it, never rebind it.

### MCP needs no spec. REST does.

REST drift detection needs a spec somebody published and kept accurate. MCP servers publish their
contract on every single call — `tools/list` **is** the spec. So this collector has the baseline from
the first call your agent makes, for every MCP server it touches, with nothing to configure and
nothing to upload: the SDK forwards the observed `tools/list` as a contract snapshot and the drift
processor versions it by content hash. A REST provider needs a document instead — drop its OpenAPI
spec into the **Contracts** tab, where it binds to exactly one host and stays on this collector.

That is not a convenience difference. "Nobody publishes an accurate OpenAPI spec" is the strongest
practical objection to the REST half of this, and it does not apply to MCP at all.

## Run it on Kubernetes

```bash
helm install flanj oci://registry-1.docker.io/flanj/flanj-collector \
  --namespace flanj --create-namespace \
  --set specToken.value="$(openssl rand -hex 32)"
```

That is the tiered shape: N stateless front collectors and one store pod, which is where the UI and the
rolling window live. Values, tiers (sqlite on an emptyDir / sqlite on a PVC / postgres) and what the chart
refuses to install: [`charts/flanj-collector`](charts/flanj-collector/README.md). The single-pod and
shared-postgres shapes, and the objects behind all three, are in [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

Point your app at the collector with the [SDK](https://github.com/flanj-io/sdk):

```bash
npm install @flanj/sdk
```

```bash
export FLANJ_OTLP_ENDPOINT=http://localhost:4318/v1/logs   # this is the default
node -r @flanj/sdk/register app.js
```

Also read: `OTEL_SERVICE_NAME`, `FLANJ_BODY_CAP_BYTES` (default 16384), `FLANJ_IGNORE_URLS`
(comma-separated; the SDK always ignores its own OTLP host). Make some calls, then watch **Traffic** fill.
Every body the SDK exports was redacted in your process before it left; the collector re-applies the same
floor on arrival.

**Before you press Connect, give it a config.** The image bakes `config/config.default.yaml` at
`/etc/flanj/config.yaml`, and that file carries **no identity and no control plane** on purpose: nothing in
the image knows who installed it, so it claims nothing. Traffic capture, drift detection, edge discovery and
the UI all work on the first run with no configuration at all — looking around without connecting is the
point. **Connect** is the one thing that needs you first, and until it has a control plane it says so:
`The control plane is not configured on this collector (set cp_base_url).`

Copy `config/config.example.yaml`, which documents every key, set `cp_base_url`, and mount it over the
baked path. No token is needed to Connect: the panel asks for a collector name and a contact email, and
the contact's confirmation click is what adds the collector to their Flanj workspace
(`cp_deploy_token` is optional — for an operator's or per-account token). The panel asks for no
organization name: the contact names the workspace on the confirmation page, and that name — the one
other organizations see on your threads — shows in the UI once it is set:

```bash
docker run -d --name flanj \
  --user "$(id -u):$(id -g)" \
  -v "$PWD/flanj-data:/data" \
  -v "$PWD/config.yaml:/etc/flanj/config.yaml:ro" \
  -p 4318:4318 -p 5335:5336 \
  flanj/collector:v0.1.0
```

**What leaves your network: nothing, until you Connect.** Unconnected, the collector makes no outbound
calls at all; the sync loop returns early with no collector key. After Connect it talks only to
`cp_base_url`: finding *shapes* (id, signature, kind, severity, endpoint, counts — never the observed
expected/actual/detail values), a directory name-table fetch that sends nothing about your edges, and the
threads you explicitly create by pressing Flag. Raw calls never leave, on any path.

Measured on this build (Docker Desktop, Apple silicon, single pod, idle; one run, rounded):

| | |
|---|---|
| Image | 49 MB |
| Resident memory | 13 MiB |
| `/api/health` | 20 ms from the host through the sidecar hop (1 ms in-container) |
| Restart to serving | 0.1 s (container start to "Everything is ready") |

Restarting the collector orphans the sidecar (it borrows the collector's network namespace): recreate it
rather than `docker start` it. Tear the whole thing down with `docker rm -f flanj flanj-ui`.

## Point an agent at it (MCP)

The collector serves a small read-only MCP server on the same loopback listener as the UI, at `/mcp`, so a
coding agent running in this environment can ask "what changed on the dependencies I call?" and be answered
by the collector already watching them.

```json
{
  "mcpServers": {
    "flanj": { "type": "http", "url": "http://localhost:5335/mcp" }
  }
}
```

Four tools, all read-only: `drift_summary` (start here), `list_edges`, `list_findings` (filter by `edge`,
`kind`, `severity`) and `get_finding`.

- **Same posture as the UI.** It is a route on the loopback listener, not a second one. Nothing new binds
  and nothing is reachable off-host.
- **Same truth as the UI.** Finding rows are the rows `GET /api/findings` serves the browser, built by the
  same code: an agent and a person see one story.
- **No raw body, ever.** No tool returns a body, a header map or a full URL, and the free-value fields of a
  finding pass the redaction floor once more on the way out.
- **Read-only, deliberately.** There is no tool that flags, acknowledges or uploads. Raising a thread with a
  provider is a person's act, in the UI.
- **An empty answer is never an all-clear on its own.** Every answer carries how many calls were actually
  validated against a contract, and says so in prose when the answer is "nothing has been checked yet".

## Renamed: Vinifera → Flanj (breaking)

This project was renamed from **Vinifera** to **Flanj** before launch. Every brand-carrying identifier
changed with it; there are no compatibility aliases:

- **Env vars:** `VINIFERA_*` → `FLANJ_*` (`FLANJ_PG_DSN`, `FLANJ_STORE_ENDPOINT`, `FLANJ_TEST_PG_DSN`,
  `FLANJ_API_PROXY`).
- **Collector config keys:** component types `viniferastore|viniferaui|viniferadrift|viniferaredaction` →
  `flanjstore|flanjui|flanjdrift|flanjredaction` — an existing `config.yaml` fails to load until updated.
- **Baked config paths:** `/etc/vinifera/*.yaml` → `/etc/flanj/*.yaml` (update `--config` args in your
  manifests).
- **OTLP wire attributes:** `vinifera.*` → `flanj.*` — the SDK and collector must be upgraded together.
- **HTTP headers:** `X-Vinifera-*` → `X-Flanj-*`. **npm scope:** `@vinifera/*` → `@flanj/*`.
- **Image, binary and Service names:** `vinifera-collector` → `flanj-collector`, `vinifera-store` →
  `flanj-store`.
- **Go module path:** `github.com/vinifera-io/collector` → `github.com/flanj-io/collector`.

See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for the operator checklist.

## Upgrading: the collector derives the integration key

The collector works out which integration a record belongs to when the record arrives, instead of
reading an id from the SDK. An outbound HTTP call and every MCP record take the key from the peer
host (`api.acme.com` → `api-acme-com`); an inbound call takes the service name it reached. The SDKs no
longer send an id, and one that still does is ignored, so old and new SDKs land on the same key.

What that means when you upgrade:

- **Records captured before the upgrade keep their old keys, and there is no backfill.** They are
  never merged with the new ones. Old calls age out with the rolling retention window; nothing has to
  be migrated by hand.
- **An open finding reopens once.** A finding's identity starts with its integration, so the next
  occurrence of an already-open outbound or MCP finding opens a new finding under the derived key, and
  the old one stops accruing. Findings from your own published contract are unaffected.
- **MCP catalogues re-key themselves once, at startup.** A tool list stored under an SDK-sent id moves
  to the derived key; when two collapse onto one key the newest wins; starting again moves nothing.
- **Nothing to configure.** There is no id to set any more, in the collector or in an SDK.

## Status

Pre-release (v0). See [docs/CONCEPTS.md](docs/CONCEPTS.md) and [CLAUDE.md](CLAUDE.md).

**Supported:** REST/HTTP integrations — the live request and response validated against the provider's
OpenAPI document.
**Supported:** MCP tools — tool-definition drift and result-vs-`outputSchema` mismatch, flagged to the
server operator with evidence. (The transport-neutral `Contract` model and the definition-diff classifier
live in the public [`contract`](contract/) package.)
**Roadmap:** webhooks (received-webhook contract drift; missing-webhook detection under design).

**Languages.** Node / TypeScript — **supported** (the [SDK](https://github.com/flanj-io/sdk):
HTTP egress and ingress, plus the MCP client). Python — **early**, MCP client only, with no HTTP body
capture. The rule is the same one the transports above follow: a language is called *supported* only
once the whole loop runs on it end to end in our own integration harness, with that suite's assertions green.
Until then it says early — here, and on every other surface.

**Where this stops, said out loud.** A REST provider needs a spec and this collector never fetches
one on your behalf; an MCP server needs none. A call to a host with no contract is captured and
**not validated**, and the UI says exactly that rather than showing a green light — `not checked`
never reads as `conforming`, anywhere, by design.

## License

[Elastic License 2.0](LICENSE) (source-available). Contributions require a DCO sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md).
