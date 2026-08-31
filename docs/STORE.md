# The collector store — backends, sizing, migration, multi-pod

The store is the collector's **rolling evidence window**: recent redacted calls
(ring buffer), drift findings (deduped per endpoint), auto-discovered edges, and
the loaded contract metadata. It is *not* a system of record — evidence worth
keeping is flagged to the control plane. Two backends implement it; pick per
deployment in the `flanjstore` extension config.

## Choosing a backend

| | `backend: sqlite` (default) | `backend: postgres` |
|---|---|---|
| What it is | Embedded pure-Go SQLite, one WAL file | A PostgreSQL database you provide |
| Pods | **Exactly one pod per db file** | **Any number of pods share one database** |
| Ops footprint | Zero — just a persistent volume (PVC) | An existing/managed Postgres (RDS, Cloud SQL, …) |
| Fits | Evaluation; single-pod production | High traffic (multiple collector pods); clusters that keep state in managed services, not PVCs |
| Config | `db_path` (on a PVC) | `dsn` |

Start on sqlite. Move to postgres when one pod stops being enough, or when your
platform policy forbids in-cluster state.

### sqlite

```yaml
extensions:
  flanjstore:
    db_path: /data/flanj.db   # MUST be on a persistent volume
    window_max_rows: 10000
    window_max_bytes: 268435456  # 256 MiB
```

`backend: sqlite` is the default and may be omitted. An embedded store without a
persistent volume dies on restart — a PVC is the floor for any real retention.
Never point two collector pods at one sqlite file; cross-process access is not
supported.

### postgres

```yaml
extensions:
  flanjstore:
    backend: postgres
    dsn: ${env:FLANJ_PG_DSN}  # e.g. postgres://user:pass@host:5432/flanj?sslmode=require
    window_max_rows: 200000      # size the window to the DB you provisioned
    window_max_bytes: 8589934592 # 8 GiB
```

- Keep credentials out of the config file with `${env:…}` interpolation; the
  collector only ever logs the DSN redacted.
- Use **one database per collector deployment** (the store owns its tables and
  uses database-scoped advisory locks for coordination).
- Every pod sharing the database must run **identical `flanjstore` config**
  (same windows, same DSN).
- No PVC is needed in this mode.

## Multi-pod semantics (postgres)

Cross-pod correctness lives in the store, not in the pods — point N identical
collectors at one database and:

- **Finding dedup is global.** The first pod to report a drift signature creates
  the finding and pins its evidence call; every other report (any pod)
  increments `occurrence_count`. The finding id — and thus the flag idempotency
  key — stays stable.
- **Call inserts are idempotent** on call id; edge `call_count` counts each
  distinct captured call exactly once, no matter which pod (or how many,
  racing) delivered it.
- **Eviction is best-effort per pod.** One pod at a time holds the eviction
  advisory lock; others skip and retry on their next insert. The window can
  transiently overshoot its caps under burst, and converges as traffic flows.
- **The UI on any pod shows the same data** (it reads the shared store). The UI
  stays loopback-only on every pod; flag from whichever pod you port-forward.

## Topologies

The same image runs in three shapes; pick per deployment (operator view — flows per
shape and the Kubernetes objects each needs — in [DEPLOYMENT.md](DEPLOYMENT.md)). The store's semantics
(dedup, pinning, eviction, the UI, the flag) are identical in all three.

| | Single pod | N pods + shared postgres | Tiered: N fronts → 1 store |
|---|---|---|---|
| Pipeline | one collector: otlp → redaction → drift → store + UI | N identical collectors, each the full pipeline, `backend: postgres` | **fronts**: otlp → redaction → drift → `otlphttp`; **store pod**: otlp → redaction → store + UI |
| Uploaded contracts | read from the co-located store, in-process | same — every pod holds a store handle onto the shared database | uploaded on the **store pod**; fronts read them back over `spec_endpoint` (set it, or drift never runs on a front) |
| Config | `/etc/flanj/config.yaml` | same, `backend: postgres` | `/etc/flanj/front.yaml` + `/etc/flanj/store.yaml` (`config/config.*.example.yaml`) |
| State | sqlite on a PVC (or postgres) | postgres only | store pod: sqlite on ONE PVC (or postgres); fronts: none |
| Scale | 1 | N writers (postgres) | N stateless fronts (HPA on cpu/memory); store = 1 on sqlite, may scale on postgres |
| UI | the pod | any pod (shared data) | the store pod |
| Fits | eval, small prod | BYO-DB, scale | "one helm install, many collectors, one store" with the zero-ops sqlite default |

### Tiered: how it works

Front collectors do everything that must happen near the traffic — redaction,
drift detection, edge/call-id stamping — and forward the resulting records
(calls, findings, contract metadata) to the store pod with the core
OpenTelemetry `otlphttp` exporter. The store pod is the single writer, so
dedup/pin/evict need no cross-pod coordination at all; it serves the UI and
the flag from the one window. Because the records are plain OTLP log records,
no custom protocol exists between the tiers.

- **Fronts are stateless** — no store extension, no UI, no PVC — so they scale
  with replicas or an HPA. Their `otlphttp` sending queue (in-memory) rides out
  a store-pod restart; a front crash loses what was in its queue (at-most-once
  across a front crash, which is fine for a rolling evidence window).
- **The store pod is one process** on `backend: sqlite` (one PVC, total). On
  `backend: postgres` the store tier may itself scale, since the postgres
  backend is multi-writer safe.
- **Order across the hop is not load-bearing.** Within a request calls precede
  the findings they produced; if a finding ever arrives first (re-delivered
  partial batch, cross-request reordering, a call re-sent after eviction) the
  store's *late pin* pins the call when it lands and repairs the edge drift
  attribution. An insert's own eviction also never evicts the row it just wrote.
- **Contracts flow the OTHER way** (2026-08-31). Provider contracts are uploaded
  in the UI, which lives on the store pod, so the store pod is their source of
  truth and the fronts READ them — the reverse of every other record here.
  A front polls the store pod's `spec_endpoint` on a one-minute ticker (and
  early on first sight of a host it has no contract for), keeps the parsed
  documents in memory, and re-downloads only what changed. Without
  `store_pod_endpoint` set on a front, an uploaded contract reaches it never and
  REST drift detection simply does not run there; the front says so once at
  start. A front's SELF contract (`self_spec_path`, still config) still crosses
  upward as a `spec_info` record, first batch after start then every 10 minutes.

### Tiered: invariants

1. **The store pod never runs `flanjdrift`** — it would re-detect every
   forwarded call and double the findings' `occurrence_count`. Drift runs
   exactly once per call, on the front.
2. **Fronts always run `flanjdrift`**, even with no spec: it stamps the
   canonical `flanj.call.id`, which makes front→store retries idempotent and
   ties each finding to its call across the hop.
3. The store pod keeps `flanjredaction` on (idempotent, add-only, skips
   non-call records): the floor on the last hop before persistence.
4. All SDK traffic enters via fronts; the store pod's `:4318` is for fronts
   (ClusterIP, intra-cluster). The UI stays loopback on the store pod —
   `kubectl port-forward` to it.
5. **Upgrade the store pod first**, then fronts (an older store drops a newer
   front's `spec_info` records harmlessly; an older front simply sends none).
   This matters more now that contracts flow downward: a front upgraded first
   would poll a `spec_endpoint` the old store pod does not serve, and detect
   nothing until the store pod caught up.
6. **The contract endpoint is not the UI.** `spec_endpoint` is a separate,
   read-only, contracts-only listener on the cluster interface — a sibling of
   `:4318`, requiring the shared `spec_token`. It exposes no calls, no findings
   and no settings, and it mutates nothing. The UI stays loopback (invariant 4),
   which is what lets this exist without weakening it.

The e2e harness proves this shape end-to-end: `make gate-tiered` /
`make stress-tiered` run the unchanged gate, the contracts check and the
exact-count stress through two fronts into one store pod.

## Settings (per-deployment key/value)

The store also holds a tiny `settings` table — `GetSetting(key)` /
`PutSetting(key, value)` on the `Store` interface — for collector-level state
that must outlive a pod and be shared by every pod of a deployment (the Connect
registration key and confirmed contact, for example). It lives **in the store,
never in a per-pod file**, so it exists exactly where the evidence does: the
single sqlite file, the shared postgres database, or the tiered store pod. Values
are opaque strings (JSON-encode structs); keys are namespaced by convention
(`connect.*`). The sqlite→postgres import carries it (existing postgres values
win on conflict). Treat secret-bearing values like the DSN: never log them.

## Sizing the window

`window_max_rows` / `window_max_bytes` cap the calls table; unpinned rows evict
FIFO past either cap (`<=0` disables that cap). Pinned evidence (calls
referenced by findings) is never evicted and may hold the window above its caps;
promoted calls re-enter the eviction pool. Findings are never evicted. Size the
window to how much *recent traffic context* is useful when investigating a
finding — at high traffic on postgres that can be millions of rows; the store
only ever needs what your database can hold.

## Migrating sqlite → postgres

Switching an existing deployment is a two-line config change — keep `db_path`:

```yaml
extensions:
  flanjstore:
    backend: postgres            # was: (sqlite, implicit)
    dsn: ${env:FLANJ_PG_DSN}  # new
    db_path: /data/flanj.db   # keep pointing at the old file
```

At the next start, the collector runs a **one-shot import** of the file's
durable evidence into postgres — pinned calls (evidence findings reference),
all findings (stable ids + occurrence counts, so flag idempotency survives),
discovered edges, and the per-deployment settings — then renames the file to
`/data/flanj.db.migrated`.
Unpinned window traffic is deliberately *not* copied: it is a rolling buffer
and refills within minutes. Loaded contracts re-record themselves at start.

- **Failure aborts start** (a crash loop is visible; silently starting empty is
  not). The import is retry-safe: every insert is conflict-tolerant and the
  rename happens only after commit, so just restart after fixing the cause.
- To *skip* migration intentionally, remove `db_path` from the config (or move
  the file away).
- The tombstone (`*.migrated`) makes re-runs no-ops; delete it (and the PVC)
  once you're satisfied.

## Invariants (do not regress)

1. The `flanjstore` extension is the **single in-process owner** of the
   store handle; every component reaches it via `store.Provider`.
2. sqlite: one pod per file. postgres: N pods per database, and every cross-pod
   race (dedup, pinning, eviction, DDL, migration) is resolved inside
   `internal/store` — callers never coordinate.
3. Rolling window, pin-on-finding, evict-after-promote — identical semantics on
   both backends, enforced by the shared test suite
   (`internal/store/store_test.go` runs per backend; `concurrency_pg_test.go`
   proves the multi-pod races).
4. **Order-independent call/finding pairs.** A finding that arrives before its
   call still ends up with pinned evidence (late pin on insert; postgres
   serialises the two writers per call id with an advisory lock), and an
   insert's own eviction never evicts the row it just wrote. Callers — the
   exporter, a front collector, the OTLP hop — never reorder or buffer to
   compensate.
