# Deploying the collector — shapes, flows, Kubernetes sketches

Companion to [STORE.md](STORE.md) (backends, window, migration, topology
invariants). This page is the *operator's* view: which shape to run, what flows
through it, and the Kubernetes objects each shape needs. A Helm chart that
renders exactly these objects is the next step (tracked in the workspace TODO);
until then this is the reference.

One image, three shapes, role chosen by `--config`:

| Shape | Run when | Pods | State |
|---|---|---|---|
| **Single pod** | evaluation, small production | 1 | sqlite on one PVC (default) or postgres |
| **N pods + shared postgres** | you already run postgres; BYO-DB | N identical | postgres, no PVC |
| **Tiered: N fronts → 1 store pod** | many collectors, zero-ops store | N fronts (stateless) + 1 store | store pod: sqlite on one PVC (or postgres) |

## The flow

Every shape moves the same records through the same stages; only *where* each
stage runs differs.

```
 SDK (egress + ingress capture, redaction-at-source)
  │  OTLP/HTTP  vinifera.* log records, record.type=call
  ▼
 [otlp receiver] → [viniferaredaction] → [viniferadrift] ──┐
   defense-in-depth floor     stamps vinifera.call.id,      │ calls + findings (+ spec_info)
   (idempotent, add-only)     emits finding records,        │ as OTLP log records
                              emits spec_info records       │
                                                            ▼
                              single pod: [viniferastore exporter] ─► store (sqlite | postgres)
                              tiered:     [otlphttp exporter] ─► store pod :4318
                                            └─► [otlp receiver] → [viniferaredaction] → [viniferastore exporter] ─► store
                                                            │
                                    viniferaui (127.0.0.1:5335): Overview / Traffic / Contracts / Threads / Settings
                                                            │  relay: connect · flag · threads — outbound only
                                                            ▼
                                                control plane  register (once) · POST /api/v1/flags → thread + thread link · thread state
```

Record types on the wire (CONTRACTS §2): `call` (from the SDK), `finding`
(drift, appended after the calls of the batch that produced it), `spec_info`
(the loaded contracts — first batch after start, then every 10 min). The store
is **order-independent** for call/finding pairs (late pin), so nothing upstream
buffers or reorders.

What the store does per record: `call` → insert (idempotent on id), edge
upsert, late pin if a finding already references it, evict; `finding` → dedup by
signature (first occurrence pins its source call and attributes drift to the
edge; repeats bump `occurrence_count`); `spec_info` → upsert by integration.

## Single pod

- Container: `vinifera-collector`, default `CMD --config /etc/vinifera/config.yaml`
  (mount your own over it, or a ConfigMap). `EXPOSE 4318` (OTLP from the SDK).
- State: `/data` on a PVC (sqlite). `user` is `nonroot` (uid 65532) — pre-chown
  the volume or use an fsGroup.
- UI: loopback only by design — `kubectl port-forward <pod> 5335:5335`.
- Kubernetes: a `StatefulSet` (replicas **1**) with a `volumeClaimTemplate` for
  `/data`, a `Service` on 4318 for the SDK, a `Secret` for `CP_DEPLOY_TOKEN`
  (`cp_deploy_token: ${env:CP_DEPLOY_TOKEN}`).
- Probes: the UI is loopback-bound, so kubelet cannot `httpGet` it; use
  `tcpSocket: 4318` for readiness/liveness (the OTLP receiver).

## N pods + shared postgres

- Same container and config as single pod with `backend: postgres` and
  `dsn: ${env:VINIFERA_PG_DSN}` (Secret). No PVC. `Deployment` with any replica
  count; every pod identical config. The SDK `Service` load-balances across pods;
  cross-pod dedup/pin/evict is the store's job (STORE.md "Multi-pod semantics").
- UI on any pod shows the whole picture (shared data); port-forward any one.
- Moving from a single sqlite pod: keep `db_path` → one-shot import at the next
  start (STORE.md "Migrating").

## Tiered: N fronts → 1 store pod

Role configs are baked into the image — no ConfigMap needed for a first run:

| Role | Args | Env | Objects |
|---|---|---|---|
| front | `--config /etc/vinifera/front.yaml` | `VINIFERA_STORE_ENDPOINT=http://vinifera-store:4318` | `Deployment` (replicas or `HorizontalPodAutoscaler` on cpu/memory), `Service vinifera-collector:4318` ← the SDK's target |
| store | `--config /etc/vinifera/store.yaml` | `CP_DEPLOY_TOKEN` (Secret); postgres: `VINIFERA_PG_DSN` | `StatefulSet` replicas **1** + PVC (sqlite) — or `Deployment` with postgres; `Service vinifera-store:4318` (ClusterIP) ← the fronts' target |

Mount your own `front.yaml`/`store.yaml` when you need your spec paths,
`integration_id`, display names, `cp_base_url` — the baked files are the
annotated templates (`config/config.front.example.yaml`,
`config/config.store.example.yaml`).

Flow specifics:
- **Redaction + drift run on the fronts**, near the traffic; the store pod runs
  redaction again (idempotent) and **never drift** (it would double-count).
- Fronts forward with the core `otlphttp` exporter: in-memory queue + retries
  ride out a store-pod restart; a front crash loses only its queue.
- Contracts: fronts emit `spec_info`; the store pod's Contracts tab fills within
  the first batch after a front starts (and every 10 min after).
- The flag action runs on the store pod (it holds the evidence); its outbound
  calls to the control plane (Connect, flag, thread state) are the only
  off-cluster egress. **Connect** is per deployment, not per pod: the collector
  key it returns lives in the store's settings KV (sqlite file / shared
  postgres) next to the evidence — nothing to mount or copy, and a replaced pod
  is still Connected. `cp_deploy_token` is only used for that first
  registration.
- UI: `kubectl port-forward sts/vinifera-store 5335:5335`.
- Store pod `:4318` is intra-cluster ingest; keep it ClusterIP (optionally a
  NetworkPolicy allowing only the front pods).
- Probes: `tcpSocket: 4318` on both roles.
- **Upgrades:** store pod first, then fronts.

Sizing: fronts are CPU-bound (redaction + schema validation) and stateless —
scale them; the store pod is IO-bound (one sqlite writer) — give it the PVC's
IOPS and size the window (`window_max_rows` / `window_max_bytes`) per STORE.md.
At the point a single store writer is the limit, switch the store role to
`backend: postgres` (and the store tier may scale).

## Environment variables the configs read

| Variable | Used by | Meaning |
|---|---|---|
| `CP_DEPLOY_TOKEN` | single pod, store pod | bearer token for the control plane (the only outbound auth) |
| `VINIFERA_STORE_ENDPOINT` | front | the store pod's base URL (default `http://vinifera-store:4318`) |
| `VINIFERA_PG_DSN` | single pod / store pod with `backend: postgres` | postgres connection string (logged redacted only) |

Any key in the YAML can use `${env:NAME}` / `${env:NAME:-default}` (OpenTelemetry
collector confmap).

## Proven by the e2e harness

The three shapes are exercised end-to-end in `vinifera-io/e2e`: `make gate` /
`make stress` (single pod), `make gate-postgres` / `make stress-postgres` (N
pods, shared postgres), `make gate-tiered` / `make stress-tiered` (2 fronts → 1
store pod) — the same gate-2 loop, the contracts check and the exact-count
capture stress on every shape.
