# Deploying the collector — shapes, flows, Kubernetes sketches

> **BREAKING — Vinifera → Flanj rename.** If you deployed under the old brand, every
> brand-carrying identifier changed and existing manifests/configs will not start until updated:
>
> - **Env vars:** `VINIFERA_PG_DSN` → `FLANJ_PG_DSN`, `VINIFERA_STORE_ENDPOINT` → `FLANJ_STORE_ENDPOINT`.
> - **Config component keys:** `viniferastore|viniferaui|viniferadrift|viniferaredaction` →
>   `flanjstore|flanjui|flanjdrift|flanjredaction` — the collector refuses to load an old config.
> - **Baked config paths:** `/etc/vinifera/{config,front,store}.yaml` → `/etc/flanj/…` (fix `--config` args).
> - **Image / Service names:** `vinifera-collector` → `flanj-collector`, `vinifera-store` → `flanj-store`
>   (the fronts' default store endpoint follows the new Service name).
> - **Wire:** OTLP attributes `vinifera.*` → `flanj.*` (upgrade the SDK in the same window) and
>   headers `X-Vinifera-*` → `X-Flanj-*`.
> - **SQLite path examples** moved `/data/vinifera.db` → `/data/flanj.db`. The filename is your
>   config, not a contract: keep `db_path` pointing at your existing PVC file — do NOT lose the store
>   by switching the path on an existing deployment.

Companion to [STORE.md](STORE.md) (backends, window, migration, topology
invariants). This page is the *operator's* view: which shape to run, what flows
through it, and the Kubernetes objects each shape needs.

One image, three shapes, role chosen by `--config`:

| Shape | Run when | Pods | State | Installed by |
|---|---|---|---|---|
| **Single pod** | evaluation, small production | 1 | sqlite on one PVC (default) or postgres | the objects below |
| **N pods + shared postgres** | you already run postgres; BYO-DB | N identical | postgres, no PVC | the objects below |
| **Tiered: N fronts → 1 store pod** | many collectors, zero-ops store | N fronts (stateless) + 1 store | store pod: sqlite on one PVC (or postgres) | **the Helm chart** |

## Installing: the chart, or these objects

The tiered shape has a chart —
[`charts/flanj-collector`](../charts/flanj-collector/README.md), published to
the same registry as the image:

```bash
helm install flanj oci://registry-1.docker.io/flanj/flanj-collector \
  --namespace flanj --create-namespace \
  --set specToken.value="$(openssl rand -hex 32)" \
  --set integration.id=acme-payments
```

It renders both role configs from your values, gives **both roles the same
`FLANJ_SPEC_TOKEN` from one Secret key**, and refuses — in its values schema,
before anything is created — an install that would come up broken rather than
broken-looking: no spec token, `store.replicas > 1` on sqlite, a PVC on the
postgres tier. Its three tiers are `store.persistence.enabled=false` (eval),
the defaults (sqlite on a PVC), and `store.backend=postgres` (scale).

**The rest of this page stays the reference, and not only as background.** The
chart covers the tiered shape only: the single-pod and shared-postgres shapes
are one object each and are described below in full, the chart's own objects are
the ones described below, and anything the values do not reach — a hand-written
config, a different scheduler story, an operator who does not run Helm — is
still built from here.

## The flow

Every shape moves the same records through the same stages; only *where* each
stage runs differs.

```
 SDK (egress + ingress capture, redaction-at-source)
  │  OTLP/HTTP  flanj.* log records, record.type=call
  ▼
 [otlp receiver] → [flanjredaction] → [flanjdrift] ──┐
   defense-in-depth floor     stamps flanj.call.id,      │ calls + findings (+ spec_info
   (idempotent, add-only)     emits finding records,        │ for SELF/MCP only)
                              emits spec_info records       │ as OTLP log records
                                                            ▼
                              single pod: [flanjstore exporter] ─► store (sqlite | postgres)
                              tiered:     [otlphttp exporter] ─► store pod :4318
                                            └─► [otlp receiver] → [flanjredaction] → [flanjstore exporter] ─► store
                                                            │
                                    flanjui (127.0.0.1:5335): Overview / Traffic / Contracts / Threads / Settings
                                                            │  relay: connect · flag · threads — outbound only
                                                            ▼
                                                control plane  register (once) · POST /api/v1/flags → thread + thread link · thread state
```

Record types on the wire (CONTRACTS §2): `call` (from the SDK), `finding`
(drift, appended after the calls of the batch that produced it), `spec_info`
(the SELF contract, plus observed MCP `tools/list` snapshots — first batch
after start, then every 10 min; **uploaded provider contracts do not travel
this way**, see "Contracts flow store → front" below). The store is
**order-independent** for call/finding pairs (late pin), so nothing upstream
buffers or reorders.

What the store does per record: `call` → insert (idempotent on id), edge
upsert, late pin if a finding already references it, evict; `finding` → dedup by
signature (first occurrence pins its source call and attributes drift to the
edge; repeats bump `occurrence_count`); `spec_info` → upsert by integration.

## Single pod

- Image: **`flanj/collector:v0.1.0`** on Docker Hub (`linux/amd64` +
  `linux/arm64`; `:latest` tracks the newest release — pin the version tag in a
  manifest). Container: `flanj-collector`, default
  `CMD --config /etc/flanj/config.yaml` (mount your own over it, or a
  ConfigMap). `EXPOSE 4318` (OTLP from the SDK). That baked file is
  `config/config.default.yaml` and is **neutral** — no `integration_id`, no
  display names, no `cp_base_url` — so an unconfigured pod captures, redacts and
  serves its UI but reports `cp_configured: false` and refuses Connect with
  `cp_not_configured` rather than claiming an identity nobody gave it. The two
  ROLE configs beside it (`front.yaml`, `store.yaml`) still carry example
  identity; the chart renders its own over them.
- State: `/data` on a PVC (sqlite). `user` is `nonroot` (uid 65532) — pre-chown
  the volume or use an fsGroup.
- UI: loopback only by design — `kubectl port-forward <pod> 5335:5335`.
- The UI's one link out — the Connected pill's dashboard door — opens in
  **your browser**, on the far side of that port-forward. It is minted from
  `cp_public_url`, not from `cp_base_url`, which is where the *collector's*
  requests go and may be an address only the cluster resolves. Set
  `cp_public_url` whenever the two differ; left unset, the link falls back to
  `cp_base_url` only when that host is not obviously non-public, and is
  otherwise omitted (the pill stays a Settings button — never a dead link).
  In-cluster names are all recognised as non-public, including the **short
  form a Helm chart renders by default** — `http://cp-api.flanj:3001`, Service
  plus namespace, with no `.svc` and no cluster domain. So is any name carrying
  an `svc` or `cluster` label, and any two-label name whose last label is not a
  common public TLD. A public control plane on an unusual TLD may fall on the
  wrong side of that last rule: set `cp_public_url` and the guess is bypassed
  entirely.
- Kubernetes: a `StatefulSet` (replicas **1**) with a `volumeClaimTemplate` for
  `/data`, a `Service` on 4318 for the SDK, a `Secret` for `CP_DEPLOY_TOKEN`
  (`cp_deploy_token: ${env:CP_DEPLOY_TOKEN}`).
- Probes: the UI is loopback-bound, so kubelet cannot `httpGet` it; use
  `tcpSocket: 4318` for readiness/liveness (the OTLP receiver).
- **Agent MCP surface**: the same loopback port serves a read-only MCP server at
  `/mcp` (`http://localhost:5335/mcp` behind the port-forward above). It is a
  route on the UI listener, not a second one — there is no key to configure, no
  port to open, and nothing to relax. Point a local agent at it to query drift
  findings; it can read and it cannot write. See the collector README, "Point an
  agent at it (MCP)".

## N pods + shared postgres

- Same container and config as single pod with `backend: postgres` and
  `dsn: ${env:FLANJ_PG_DSN}` (Secret). No PVC. `Deployment` with any replica
  count; every pod identical config. The SDK `Service` load-balances across pods;
  cross-pod dedup/pin/evict is the store's job (STORE.md "Multi-pod semantics").
- UI on any pod shows the whole picture (shared data); port-forward any one.
- Moving from a single sqlite pod: keep `db_path` → one-shot import at the next
  start (STORE.md "Migrating").

## Tiered: N fronts → 1 store pod

[The Helm chart](../charts/flanj-collector/README.md) installs this shape, and
every object in the table below is one it renders. What follows is what it
renders and why — read it whether or not you use the chart, because the two
failure modes in this section are the ones that look healthy.

Role configs are baked into the image — no ConfigMap needed for a first run
(the chart renders its own from your values instead, mounted at
`/etc/flanj/chart/`, because the baked ones carry example identity and the
example Service names):

| Role | Args | Env | Objects |
|---|---|---|---|
| front | `--config /etc/flanj/front.yaml` | `FLANJ_STORE_ENDPOINT=http://flanj-store:4318`, **`FLANJ_SPEC_TOKEN`** (Secret), optionally `FLANJ_STORE_SPEC_ENDPOINT` (default `http://flanj-store:5337`) | `Deployment` (replicas or `HorizontalPodAutoscaler` on cpu/memory), `Service flanj-collector:4318` ← the SDK's target |
| store | `--config /etc/flanj/store.yaml` | `CP_DEPLOY_TOKEN` (Secret), **`FLANJ_SPEC_TOKEN`** (Secret); postgres: `FLANJ_PG_DSN` | `StatefulSet` replicas **1** + PVC (sqlite) — or `Deployment` with postgres; `Service flanj-store:4318` (ClusterIP) ← the fronts' target; `Service flanj-store:5337` (ClusterIP) ← the fronts' contract reads |

**`FLANJ_SPEC_TOKEN` is required on this shape, on BOTH roles, with the same
value.** The baked `store.yaml` binds the contract endpoint
(`spec_endpoint: 0.0.0.0:5337`) and reads its token from
`${env:FLANJ_SPEC_TOKEN}`; the baked `front.yaml` presents the same variable as
`flanjdrift.store_pod_token`. An unset env var expands to the empty string, and
the store deliberately **fails closed** rather than bind a cluster-reachable
port with no auth — so a store pod started without it exits at startup with:

```
Error: invalid configuration: extensions::flanjstore: flanjstore extension:
spec_endpoint requires spec_token (this listener is not loopback — set
FLANJ_SPEC_TOKEN, or remove spec_endpoint)
```

**Set the token — do not take the second branch of that message.** Deleting
`spec_endpoint` does make the store pod boot, and it silently turns off REST
drift detection for every front: the fronts have nothing to read contracts
from, so an uploaded contract validates nothing, on a deployment that otherwise
looks healthy. Same outcome from the other end: **leave `store_pod_endpoint`
unset on a front and that front detects no REST drift at all**, however many
contracts have been uploaded. It is not an error — a spec-less front is a legal
configuration — so it only says so once, in a warning at start:

```
no contract source: this collector has no store and no store_pod_endpoint,
so uploaded contracts cannot reach it and REST drift detection will not run
```

Note the roles fail **differently**, on purpose. The store pod fails closed and
will not boot. A front does not — a wrong or empty `FLANJ_SPEC_TOKEN` still
starts it, and the contract endpoint answers `401`, which surfaces once per
refresh tick as `contract refresh failed … store pod returned 401
Unauthorized`. So after a tiered rollout, check a front's logs, not just that
its pods are Ready.

The chart takes the "identical value on both roles" half of this out of your
hands — both roles read one Secret key — and refuses to render at all without a
token, rather than installing a store pod that exits at startup. It cannot take
the *other* half out of your hands: with `specToken.existingSecret`, a value
that changes underneath a running deployment produces exactly the 401 above.

Mount your own `front.yaml`/`store.yaml` when you need your own
`integration_id`, display names, `cp_base_url` / `cp_public_url`, or window sizes — the baked
files are the annotated templates (`config/config.front.example.yaml`,
`config/config.store.example.yaml`). **Provider contracts are not among those
knobs**: they are uploaded in the UI (Contracts → Add contract), never
configured, and there are no spec paths to point at (CONTRACTS §8, 2026-08-31).

Flow specifics:
- **Redaction + drift run on the fronts**, near the traffic; the store pod runs
  redaction again (idempotent) and **never drift** (it would double-count).
- Fronts forward with the core `otlphttp` exporter: in-memory queue + retries
  ride out a store-pod restart; a front crash loses only its queue.
- The store pod's write is queued + retried the same way (`flanjstore`
  exporter, on by default — 64 MiB of proto-encoded batches, so budget several
  × that in the pod's memory limit; 15 minutes of backoff; rejecting when full
  so the front's queue takes over), and the store is idempotent on
  call id and finding id: a database outage shorter than that loses nothing
  the store pod accepted, and a re-sent batch duplicates nothing. This holds on
  the single-pod and shared-postgres shapes too — there it is the SDK's own
  retry that the full queue hands back to. Tune or disable it under
  `exporters.flanjstore` (`config/config.example.yaml`).
- **Contracts flow store → front** — the reverse of every other record here.
  They are uploaded in the UI, which lives on the store pod, so the store pod
  is their source of truth; each front READS them back from the store pod's
  `spec_endpoint` on a ten-second ticker (and early on first sight of a host it
  has no contract for), keeping the parsed documents in memory. An upload
  therefore starts validating within about ten seconds, with no restart and no
  front rollout. The ticker is what a front has: the store pod announces a
  contract change in-process, and a front is a different process, so it cannot
  hear it. The reads are metadata-only and download nothing when nothing moved. What still travels front → store as `spec_info` is a front's
  own SELF contract (`self_spec_path`) and its observed MCP `tools/list`
  snapshots — never a provider contract.
- **MCP snapshots travel both ways.** Each front forwards the `tools/list` it
  observes, and the store pod serves every front's snapshots back over the same
  `spec_endpoint` (since 2026-09-07), so a front's MCP baseline is the store's:
  a tool renamed while one front was watching is a `definition_change` — and
  the stale client calling the old name through another front a `stale_client`
  — whichever front sees what, and a restarted front resumes from the store
  rather than from the next list it happens to observe. On each front the newer
  observation wins, silently; the front that observed a change reports it.
- **One contract document is capped at 8 MiB on that channel**, at both ends:
  the store pod answers `413` rather than serving a larger one, and a front
  refuses to read one rather than reading a prefix. Neither end truncates — a
  document cut off at the cap still parses, as garbage, so the front would blame
  the parser for a size problem and quietly detect nothing on that edge. An
  UPLOADED contract can never be this: the uploader refuses one over the cap
  before it is stored. An observed MCP `tools/list` can, since nothing caps a
  server's catalogue on its way in.

  **Where you see it.** On the store pod's **Contracts tab**: the row stays
  listed — withholding it would make an unreadable edge look like an edge with
  no contract — and carries a `too large to serve` state naming the size and the
  overage. That state renders only where the cap applies: this pod serves fronts
  (`spec_endpoint` is set). A single pod reads the same document in-process,
  crosses no boundary, applies no cap, and validates against it normally, so
  nothing is said there. In the **logs** it is a transition, not a heartbeat:
  each pod names the refusal when it starts and again when it clears, once —
  `contract refresh: the store pod holds a document past the cap …` on a front,
  `contract endpoint: a stored document is past the cap …` on the store pod,
  both carrying the integration and the byte count. It is NOT repeated on every
  ten-second refresh; a log with one line has not stopped noticing.

  **What to do.** The fix is on the MCP server — a smaller catalogue, or split
  across servers. Meanwhile each front keeps validating that edge against
  whatever baseline it already had; a front that has none validates nothing
  there, and every call on it is stamped `not-validated` / `no-contract`, which
  is what the card is telling you. Note the shape this cannot help: a front
  reading from a store pod older than 2026-09-08 gets the old silent truncation,
  and a prefix of exactly 8 MiB is indistinguishable from a document that fits.
  Roll the store pod first.
- The flag action runs on the store pod (it holds the evidence); its outbound
  calls to the control plane (Connect, flag, thread state, and the three
  background sync legs below) are the only off-cluster egress. **Connect** is
  per deployment, not per pod: the collector
  key it returns lives in the store's settings KV (sqlite file / shared
  postgres) next to the evidence — nothing to mount or copy, and a replaced pod
  is still Connected. `cp_deploy_token` is only used for that first
  registration.
- **What the background ticker sends, and how to turn each leg off.** Once
  Connected, one 15s ticker on the store pod runs three independently gated
  legs, each with its own key in the `flanjui` block (all default `true`):
  `finding_sync` posts the SHAPE of current findings (never `expected` /
  `actual` / `detail`); `directory_sync` is a pure FETCH of the display-name
  table (nothing about your edges is sent); and — since v1 phase 2 —
  `edge_sync` registers each **external** edge as `{registrable_domain,
  direction, first_seen, last_seen}` and nothing more. No calls, no bodies, no
  payloads, no call counts, and **no internal edge, ever** — the classification
  that keeps internal same-team traffic off the Edges view keeps it off the
  wire too. Setting one to `false` disables only that leg; all three off and no
  ticker starts at all. The Connect panel states the registration before the
  operator Connects, and nothing is sent by a collector that never did.
- UI: `kubectl port-forward sts/flanj-store 5335:5335`. The Connected pill's
  dashboard link is built from the store pod's `cp_public_url` (see "Single
  pod"): a `cp_base_url` naming a Service or a VPC-private ingress is right for
  the pod's own calls and unreachable from the laptop behind the port-forward.
- Store pod `:4318` is intra-cluster ingest; keep it ClusterIP (optionally a
  NetworkPolicy allowing only the front pods).
- Probes: `tcpSocket: 4318` on both roles.
- **Upgrades:** store pod first, then fronts. Since 2026-09-07 this is load-bearing for the contract chip: a front stamps every call with its verdict (`flanj.validated`, CONTRACTS §2), and a store pod from before the stamp drops the attribute at decode — so an upgraded front behind an old store pod silently falls back to the old inferred coverage, on exactly the topology where the inference was wrong. A new store pod behind old fronts reads their calls as `unknown` → `not checked` until they are upgraded: safe, and transient.

Sizing: fronts are CPU-bound (redaction + schema validation) and stateless —
scale them; the store pod is IO-bound (one sqlite writer) — give it the PVC's
IOPS and size the window (`window_max_rows` / `window_max_bytes`) per STORE.md.
At the point a single store writer is the limit, switch the store role to
`backend: postgres` (and the store tier may scale).

## Environment variables the configs read

This table is the complete contract: every `FLANJ_*` variable the shipped
`config/*.example.yaml` files reference appears here, and CI fails the build if
one is missing (`scripts/check-env-documented.sh`). The Helm chart renders its
own role configs rather than using the baked ones, which makes it a second way a
variable can enter the operator contract — so `charts/*/templates` is scanned by
the same guard, against this same table.

| Variable | Used by | Required? | Meaning |
|---|---|---|---|
| `CP_DEPLOY_TOKEN` | single pod, store pod | to Connect | bearer token for the control plane, used ONCE at registration (the only outbound auth). Without it the collector runs and captures normally; it just cannot create thread links |
| `FLANJ_STORE_ENDPOINT` | front | no | the store pod's OTLP ingest base URL for forwarded calls and findings (default `http://flanj-store:4318`) |
| `FLANJ_SPEC_TOKEN` | **store pod AND every front** | **yes, on the tiered shape** | shared bearer token for the contract endpoint. **Must be the identical value on the store pod (`flanjstore.spec_token`) and on every front (`flanjdrift.store_pod_token`)** — a front presenting a different one gets `401` and reads no contracts. The store pod refuses to start without it (see "Tiered" above); never logged, on either side. Unused on the single-pod and shared-postgres shapes |
| `FLANJ_STORE_SPEC_ENDPOINT` | front | no | base URL of the store pod's read-only contract endpoint (default `http://flanj-store:5337`). Override only if you renamed the Service or moved the port. **Unset on a front means `store_pod_endpoint` is still set to the default — it is deleting the key from `front.yaml` that disables REST drift there** |
| `FLANJ_PG_DSN` | single pod / store pod with `backend: postgres` | with postgres | postgres connection string (logged redacted only). **One database per environment** — see STORE.md, "Never share a DSN across environments" |

Any key in the YAML can use `${env:NAME}` / `${env:NAME:-default}` (OpenTelemetry
collector confmap). An **unset** variable expands to the empty string rather
than failing — which is why `FLANJ_SPEC_TOKEN` is validated explicitly rather
than trusted to a default.

## Proven by the kind smoke

The chart is not trusted to `helm lint`: linting proves it renders, not that
what it renders boots, and both of this shape's bad outcomes render perfectly.
`scripts/helm-smoke.sh` installs the tiered lane into a kind cluster on every
pull request (`.github/workflows/ci.yml`), on **both** store backends, and
asserts behaviour — the UI answers through a port-forward to the store pod's
loopback bind, `serves_fronts` is true (so `spec_endpoint` really is bound), a
call POSTed to the *front* Service arrives in the *store's* window, and no front
is logging a contract-refresh `401`. It also asserts the refusals: an install
with no spec token, a sqlite store with `replicas > 1`, and a PVC on the
postgres tier all have to fail.

Run it against a locally built image with
`bash scripts/helm-smoke.sh flanj-collector:<tag>`.

## Proven by the e2e harness

The three shapes are exercised end-to-end in `flanj-io/e2e`: `make gate` /
`make stress` (single pod), `make gate-postgres` / `make stress-postgres` (N
pods, shared postgres), `make gate-tiered` / `make stress-tiered` (2 fronts → 1
store pod) — the same gate-2 loop, the contracts check and the exact-count
capture stress on every shape.
