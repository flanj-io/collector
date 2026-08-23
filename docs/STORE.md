# The collector store — backends, sizing, migration, multi-pod

The store is the collector's **rolling evidence window**: recent redacted calls
(ring buffer), drift findings (deduped per endpoint), auto-discovered edges, and
the loaded contract metadata. It is *not* a system of record — evidence worth
keeping is flagged to the control plane. Two backends implement it; pick per
deployment in the `viniferastore` extension config.

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
  viniferastore:
    db_path: /data/vinifera.db   # MUST be on a persistent volume
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
  viniferastore:
    backend: postgres
    dsn: ${env:VINIFERA_PG_DSN}  # e.g. postgres://user:pass@host:5432/vinifera?sslmode=require
    window_max_rows: 200000      # size the window to the DB you provisioned
    window_max_bytes: 8589934592 # 8 GiB
```

- Keep credentials out of the config file with `${env:…}` interpolation; the
  collector only ever logs the DSN redacted.
- Use **one database per collector deployment** (the store owns its tables and
  uses database-scoped advisory locks for coordination).
- Every pod sharing the database must run **identical `viniferastore` config**
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

The same image runs in three shapes; pick per deployment. The store's semantics
(dedup, pinning, eviction, the UI, the flag) are identical in all three.

| | Single pod | N pods + shared postgres | Tiered: N fronts → 1 store |
|---|---|---|---|
| Pipeline | one collector: otlp → redaction → drift → store + UI | N identical collectors, each the full pipeline, `backend: postgres` | **fronts**: otlp → redaction → drift → `otlphttp`; **store pod**: otlp → redaction → store + UI |
| Config | `/etc/vinifera/config.yaml` | same, `backend: postgres` | `/etc/vinifera/front.yaml` + `/etc/vinifera/store.yaml` (`config/config.*.example.yaml`) |
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
- **Contract metadata crosses the hop too**: each front emits its loaded specs
  as `spec_info` records (first batch after start, then every 10 minutes) so
  the store pod's Contracts tab is populated; a freshly wiped store converges
  within one interval.

### Tiered: invariants

1. **The store pod never runs `viniferadrift`** — it would re-detect every
   forwarded call and double the findings' `occurrence_count`. Drift runs
   exactly once per call, on the front.
2. **Fronts always run `viniferadrift`**, even with no spec: it stamps the
   canonical `vinifera.call.id`, which makes front→store retries idempotent and
   ties each finding to its call across the hop.
3. The store pod keeps `viniferaredaction` on (idempotent, add-only, skips
   non-call records): the floor on the last hop before persistence.
4. All SDK traffic enters via fronts; the store pod's `:4318` is for fronts
   (ClusterIP, intra-cluster). The UI stays loopback on the store pod —
   `kubectl port-forward` to it.
5. **Upgrade the store pod first**, then fronts (an older store drops a newer
   front's `spec_info` records harmlessly; an older front simply sends none).

The e2e harness proves this shape end-to-end: `make gate-tiered` /
`make stress-tiered` run the unchanged gate, the contracts check and the
exact-count stress through two fronts into one store pod.

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
  viniferastore:
    backend: postgres            # was: (sqlite, implicit)
    dsn: ${env:VINIFERA_PG_DSN}  # new
    db_path: /data/vinifera.db   # keep pointing at the old file
```

At the next start, the collector runs a **one-shot import** of the file's
durable evidence into postgres — pinned calls (evidence findings reference),
all findings (stable ids + occurrence counts, so flag idempotency survives),
and discovered edges — then renames the file to `/data/vinifera.db.migrated`.
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

1. The `viniferastore` extension is the **single in-process owner** of the
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
