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

[flanj.io](https://flanj.io)

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

## Run it on Kubernetes

```bash
helm install flanj oci://registry-1.docker.io/flanj/flanj-collector \
  --namespace flanj --create-namespace \
  --set specToken.value="$(openssl rand -hex 32)"
```

`specToken` is a secret you generate: the store pod serves the contracts you upload to the front
collectors over an in-cluster port, and it will not open that port without one.

That installs the tiered shape — N stateless front collectors and one store pod, which is where the UI and
the rolling window live. The front collectors answer at a fixed Service name, so the address below is the
same on every cluster that ran the command above. Put it on **your own** workload, next to your app's
container:

```yaml
env:
  - name: FLANJ_OTLP_ENDPOINT
    value: http://flanj-collector.flanj:4318/v1/logs
```

The chart also renders `ConfigMap/flanj-endpoint` — one key, `FLANJ_OTLP_ENDPOINT`, the same value — if you
prefer `envFrom: [{configMapRef: {name: flanj-endpoint}}]`. A pod can only reference a ConfigMap in its own
namespace and the chart installs into `flanj`, so list your application's namespaces in
`endpointConfigMap.namespaces` and the chart writes one into each.

Then open the UI:

```bash
kubectl -n flanj port-forward sts/flanj-flanj-collector-store 5335:5335
```

Values, tiers (sqlite on an emptyDir / sqlite on a PVC / postgres) and what the chart refuses to install:
[`charts/flanj-collector`](charts/flanj-collector/README.md). The single-pod and shared-postgres shapes,
and the objects behind all three, are in [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

### Already running the OpenTelemetry Operator?

If your cluster runs the [OpenTelemetry
Operator](https://github.com/open-telemetry/opentelemetry-operator), an `Instrumentation` resource can carry
the variable for you, so no workload spec has to name it:

```yaml
apiVersion: opentelemetry.io/v1alpha1
kind: Instrumentation
metadata:
  name: flanj
spec:
  env:
    - name: FLANJ_OTLP_ENDPOINT
      value: http://flanj-collector.flanj:4318/v1/logs
```

Annotate the pods that should get it with `instrumentation.opentelemetry.io/inject-sdk: "true"` (or
`"<namespace>/<name>"` to name this resource across namespaces). The operator's `spec.env` is its *common*
env list: it is appended to the application container on every injection path, and it never overwrites a
variable the container already sets.

Two things this does **not** do. It injects environment only — `inject-sdk` adds no agent and no init
container, so the Flanj SDK still has to be installed in your image and preloaded exactly as it is without
the operator. And it is **not** `spec.exporter.endpoint`: that field is translated into the generic
`OTEL_EXPORTER_OTLP_ENDPOINT`, which every OpenTelemetry SDK in the pod reads, so setting it would redirect
all of them — and collide with an `Instrumentation` already pointed at your tracing vendor.

## Run it with Docker

```bash
curl -fsSLO https://raw.githubusercontent.com/flanj-io/collector/main/docker-compose.yml
docker compose up -d
```

Then open <http://localhost:5335>; health is at <http://localhost:5335/api/health>. OTLP ingest is on
`:4318`, and there is nothing to point anywhere: `http://localhost:4318/v1/logs` is already both SDKs'
default. Stop it with `docker compose down`, or `docker compose down -v` to discard the captured window
too.

(If you would rather not keep the file: `curl -fsSL https://raw.githubusercontent.com/flanj-io/collector/main/docker-compose.yml | docker compose -f - up -d`. You then need the same pipe for every later
`docker compose` command, which is why the two-line form is the one above.)

The image is `linux/amd64` and `linux/arm64`. `:latest` tracks the newest release; the compose file pins a
version tag, which is what you want for anything you keep. To build it yourself instead:
`docker build -t flanj-collector .` and edit `image:`.

**The UI binds container loopback, by design, and that is not a setting to relax.** It serves every captured
call and it has no credential — the loopback bind *is* its access control. The collector is outbound-only;
nothing it serves is reachable off-host. A container also cannot publish a port it reaches over another
container's loopback, so the compose file runs a small `alpine/socat` bridge inside the collector's network
namespace and publishes **that** — on `127.0.0.1` only. Dropping the host address from that publish binds
`0.0.0.0` *and* `[::]`, which is why `scripts/check-ui-loopback.sh` fails the build on any publish of it
that does.

Two things the compose file handles that are easy to get wrong by hand. A fresh Docker *named* volume is
root-owned and the image runs as `nonroot`, so the store cannot create its file and the collector exits with
`unable to open database file (14)`; a one-shot init service fixes the ownership before the collector
starts. And restarting the collector gives it a new network namespace, stranding a bridge that cannot tell —
so the bridge watches the collector's loopback and exits when it can no longer reach it, and its restart
policy brings it back. Recreating the collector *alone* is the one case left: a new container id cannot be
rejoined at all, and a plain `docker compose up -d` afterwards repairs it.

Without compose, one container and one bridge:

```bash
mkdir -p ./flanj-data
docker run -d --name flanj \
  --user "$(id -u):$(id -g)" \
  -v "$PWD/flanj-data:/data" \
  -p 4318:4318 -p 127.0.0.1:5335:5336 \
  flanj/collector:v0.3.0

docker run -d --name flanj-ui --network container:flanj \
  alpine/socat TCP-LISTEN:5336,fork,reuseaddr TCP:127.0.0.1:5335
```

A bind mount owned by you avoids the named-volume problem; the UI port is published on the collector because
the bridge shares its namespace and cannot publish its own. Restarting the collector orphans the bridge here
— recreate it rather than `docker start` it. Tear the whole thing down with `docker rm -f flanj flanj-ui`.

On Linux, `--network host` works instead of the bridge (`localhost:5335` is then the same loopback); on
Docker Desktop it is not equivalent, so use the bridge. Either way the bind stays loopback: tunnel to it,
never rebind it.

## MCP needs no spec. REST does.

REST drift detection needs a spec somebody published and kept accurate. MCP servers publish their
contract on every single call — `tools/list` **is** the spec. So this collector has the baseline from
the first call your agent makes, for every MCP server it touches, with nothing to configure and
nothing to upload: the SDK forwards the observed `tools/list` as a contract snapshot and the drift
processor versions it by content hash. A REST provider needs a document instead — drop its OpenAPI
spec into the **Contracts** tab, where it binds to exactly one host and stays on this collector.

That is not a convenience difference. "Nobody publishes an accurate OpenAPI spec" is the strongest
practical objection to the REST half of this, and it does not apply to MCP at all.

## Point your app at the collector

With the [SDK](https://github.com/flanj-io/sdk):

```bash
npm install @flanj/sdk
```

```bash
node -r @flanj/sdk/register app.js
```

On Docker there is nothing to set: `http://localhost:4318/v1/logs` is the SDK's own default, and the
compose file publishes `:4318` there. On Kubernetes, give your workload the `FLANJ_OTLP_ENDPOINT` shown
above — the variable goes on the container that sends, never on the collector.

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
other organizations see on your threads — shows in the UI once it is set. With compose, add the file to
the `collector` service:

```yaml
    volumes:
      - flanj-data:/data
      - ./config.yaml:/etc/flanj/config.yaml:ro
```

On Kubernetes the chart renders both role configs from its values instead — set `controlPlane.baseUrl`
(and `controlPlane.publicUrl`, the address *your browser* can open).

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
