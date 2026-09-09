# Flanj Collector

A single-binary [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) distribution for the
**local, self-hosted** side of Flanj. It receives captured calls from the [SDK](https://github.com/flanj-io/sdk),
re-applies redaction (defense-in-depth), validates live traffic against a provider's OpenAPI spec to
detect **drift**, stores redacted calls in an embedded rolling-window store, and serves a **local UI** on
`localhost` — all inside your own environment. **Raw calls never leave.**

Headless and **outbound-only** apart from the localhost UI. Ships and deploys as one unit (collector + store +
UI embedded in the binary).

- OTLP receiver → redaction processor → drift detection → local store (rolling window; embedded SQLite on a PVC by default, or a shared Postgres database — see docs/STORE.md).
- Scales as N stateless front collectors forwarding (OTLP) to one store pod — the same image in two roles, chosen by config (docs/STORE.md "Topologies"; deploy shapes + Kubernetes sketches in docs/DEPLOYMENT.md).
- Local Vue UI: **Overview** (health + drift findings with the correlation keys that make a finding actionable),
  **Traffic** (live tail of redacted calls), **Contracts** (the loaded specs, each finding with **Flag this** → one
  sheet → **Create thread** → a **thread link** you copy into the channel the two teams already share), **Threads**
  (state of every thread you created: turn label, opens, Open / Close / Reopen / Replace link) and **Settings**
  (**Connect** — org name + a one-click-confirmed contact email; required to create thread links, never to view
  your own data).

**We turn a detection into something you can act on with your vendor.**
Open-source SDK (Apache-2.0) and source-available collector (ELv2); hosted network layer.

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

The image is `linux/amd64` and `linux/arm64`. `:latest` tracks the newest
release; pin the version tag for anything you deploy. To build it yourself
instead — `docker build -t flanj-collector .`, then substitute that name below.

`:4318` is the OTLP ingest your app points at. `/data` holds the embedded SQLite
store — bind-mount it and run as yourself, or the store cannot open its file
(the image runs as `nonroot`, and a fresh Docker *named* volume is root-owned:
that combination fails at start with `unable to open database file (14)`).

**The UI binds container loopback, by design — and that is not a setting to
relax.** The collector is outbound-only; nothing it serves is reachable off-host.
`-p 5335:5335` therefore publishes nothing. Bridge it from *inside* the network
namespace instead, which is what the e2e harness does
(`e2e/compose/docker-compose.yml`) — note the port is published on the collector
above, because a container sharing another's netns cannot publish its own:

```bash
docker run -d --name flanj-ui --network container:flanj \
  alpine/socat TCP-LISTEN:5336,fork,reuseaddr TCP:127.0.0.1:5335
```

Then open **<http://localhost:5335>** — health is at
<http://localhost:5335/api/health>. On Linux, `--network host` works instead of
the sidecar (`localhost:5335` is then the same loopback); on Docker Desktop it
is not equivalent, so use the sidecar. In Kubernetes it is
`kubectl port-forward <pod> 5335:5335`. Either way the bind stays loopback —
tunnel to it, never rebind it.

## Run it on Kubernetes

```bash
helm install flanj oci://registry-1.docker.io/flanj/flanj-collector \
  --namespace flanj --create-namespace \
  --set specToken.value="$(openssl rand -hex 32)" \
  --set integration.id=acme-payments
```

That is the tiered shape: N stateless front collectors and one store pod, which
is where the UI and the rolling window live. Values, tiers (sqlite on an
emptyDir / sqlite on a PVC / postgres) and what the chart refuses to install:
[`charts/flanj-collector`](charts/flanj-collector/README.md). The single-pod and
shared-postgres shapes, and the objects behind all three, are in
[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

Point your app at the collector with the [SDK](https://github.com/flanj-io/sdk):

```bash
npm install @flanj/sdk
```

```bash
export FLANJ_INTEGRATION_ID=acme-payments        # labels this integration
export FLANJ_OTLP_ENDPOINT=http://localhost:4318/v1/logs   # this is the default
node -r @flanj/sdk/register app.js
```

Also read: `OTEL_SERVICE_NAME`, `FLANJ_BODY_CAP_BYTES` (default 16384),
`FLANJ_IGNORE_URLS` (comma-separated; the SDK always ignores its own OTLP host).
Make some calls, then watch **Traffic** fill.

**Before you press Connect, give it a config.** The image bakes
`config/config.default.yaml` at `/etc/flanj/config.yaml`, and that file carries
**no identity and no control plane** on purpose: nothing in the image knows who
installed it, so it claims nothing. Traffic capture, drift detection, edge
discovery and the UI all work on the first run with no configuration at all —
looking around without connecting is the point. **Connect** is the one thing that
needs you first, and until it has a control plane it says so: `The control plane
is not configured on this collector (set cp_base_url and cp_deploy_token).`

Copy `config/config.example.yaml`, which documents every key, set
`integration_id`, `consumer_display_name`, `cp_base_url` and `cp_deploy_token`,
and mount it over the baked path:

```bash
docker run -d --name flanj \
  --user "$(id -u):$(id -g)" \
  -v "$PWD/flanj-data:/data" \
  -v "$PWD/config.yaml:/etc/flanj/config.yaml:ro" \
  -p 4318:4318 -p 5335:5336 \
  flanj/collector:v0.1.0
```

**What leaves your network: nothing, until you Connect.** Unconnected, the
collector makes no outbound calls at all — the sync loop returns early with no
collector key. After Connect it talks only to `cp_base_url`: finding *shapes*
(id, signature, kind, severity, endpoint, counts — never the observed
expected/actual/detail values), a directory name-table fetch that sends nothing
about your edges, and the threads you explicitly create by pressing Flag.
**Raw calls never leave**, on any path.

Measured on this build (Docker Desktop, Apple silicon, single pod, idle):

| | |
|---|---|
| Image | **49 MB** |
| Resident memory | **~13 MiB** |
| `/api/health` | **~20 ms** from the host through the sidecar hop (~1 ms in-container) |
| Restart → serving | **~0.1 s** (container start to "Everything is ready") |

Restarting the collector orphans the sidecar (it borrows the collector's
network namespace) — recreate it rather than `docker start` it. Tear the whole
thing down with `docker rm -f flanj flanj-ui`.

## Point an agent at it (MCP)

The collector serves a small **read-only MCP server** on the same loopback
listener as the UI, at **`/mcp`**, so a coding agent running in this environment
can ask *"what changed on the dependencies I call?"* and be answered by the
collector already watching them.

```json
{
  "mcpServers": {
    "flanj": { "type": "http", "url": "http://localhost:5335/mcp" }
  }
}
```

Four tools, all read-only: `drift_summary` (start here), `list_edges`,
`list_findings` (filter by `edge`, `kind`, `severity`) and `get_finding`.

- **Same posture as the UI.** It is a route on the loopback listener, not a
  second one. Nothing new binds and nothing is reachable off-host.
- **Same truth as the UI.** Finding rows are the rows `GET /api/findings` serves
  the browser, built by the same code — an agent and a person see one story.
- **No raw body, ever.** No tool returns a body, a header map or a full URL, and
  the free-value fields of a finding pass the redaction floor once more on the
  way out.
- **Read-only, deliberately.** There is no tool that flags, acknowledges or
  uploads. Raising a thread with a provider is a person's act, in the UI.
- **An empty answer is never an all-clear on its own.** Every answer carries how
  many calls were actually validated against a contract, and says so in prose
  when the answer is "nothing has been checked yet".

## Renamed: Vinifera → Flanj (BREAKING)

This project was renamed from **Vinifera** to **Flanj** before launch. Every brand-carrying
identifier changed with it — there are no compatibility aliases:

- **Env vars:** `VINIFERA_*` → `FLANJ_*` (`FLANJ_PG_DSN`, `FLANJ_STORE_ENDPOINT`, `FLANJ_TEST_PG_DSN`, `FLANJ_API_PROXY`).
- **Collector config keys:** component types `viniferastore|viniferaui|viniferadrift|viniferaredaction` →
  `flanjstore|flanjui|flanjdrift|flanjredaction` — an existing `config.yaml` fails to load until updated.
- **Baked config paths:** `/etc/vinifera/*.yaml` → `/etc/flanj/*.yaml` (update `--config` args in your manifests).
- **OTLP wire attributes:** `vinifera.*` → `flanj.*` — the SDK and collector must be upgraded together.
- **HTTP headers:** `X-Vinifera-*` → `X-Flanj-*`. **npm scope:** `@vinifera/*` → `@flanj/*`.
- **Image/binary/Service names:** `vinifera-collector` → `flanj-collector`, `vinifera-store` → `flanj-store`.
- **Go module path:** `github.com/vinifera-io/collector` → `github.com/flanj-io/collector`.

See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for the operator checklist.

## Status

Pre-release (v0). See [docs/CONCEPTS.md](docs/CONCEPTS.md) and [CLAUDE.md](CLAUDE.md).

**Supported:** REST/HTTP integrations — live request/response validated against the provider's OpenAPI.
**Supported:** MCP tools — tool-definition drift and result-vs-`outputSchema` mismatch, flagged to
the server operator with evidence. (The transport-neutral `Contract` model and definition-diff
classifier live in the public [`contract`](contract/) package.)
**Roadmap:** webhooks (received-webhook contract drift; missing-webhook detection under design).

## License

[Elastic License 2.0](LICENSE) (source-available). Contributions require a DCO sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md).
