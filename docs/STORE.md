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

> **Never share a DSN across environments.** Give staging and production
> separate databases. Sharing one is not "two deployments in one database" —
> the store has no environment column and the `settings` table is keyed by name
> alone, so **the second environment silently adopts the first's identity**: it
> reads the same `connect.collector_key`, merges its findings into the same
> deduplicated set, and syncs them to the control plane **as the first**. Every
> layer behaves exactly as designed and nothing errors, which is what makes it
> hard to notice — staging drift arrives on the production dashboard, staging
> evidence backs a production thread, and occurrence counts are the sum of both.
> The same applies to a `db_path` shared by two sqlite deployments.

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
| MCP baseline (observed `tools/list`) | the process's own, re-seeded from the store at restart | every pod seeds from the shared rows, so one pod's observation is every pod's baseline | each front forwards its snapshots up as `spec_info`; every front reads the store pod's back over `spec_endpoint`, so a rename observed through one front is judged on all of them |
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
- **The store pod's own write is queued and retried too** (2026-09-07): the
  `flanjstore` exporter carries the same `sending_queue` + `retry_on_failure`
  sections, on by default — 64 MiB of proto-encoded batches (the resident
  pdata is a multiple: budget several × that in the pod's memory limit),
  rejecting when full, backoff up to 15 minutes. A front's 5-minute retry
  therefore covers the hop, and the store pod covers the database: a batch it
  has accepted survives postgres (or a locked sqlite file) being away for a
  quarter of an hour, and a full queue hands the batch back to the front (503)
  rather than blocking. Same
  at-most-once across a store-pod crash as the fronts have. The writes are
  idempotent on call id AND finding id (the store's occurrence ledger), so a
  front re-sending a batch whose ACK it lost duplicates nothing — on either
  backend, including a re-send that lands on another postgres pod.
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
  A front polls the store pod's `spec_endpoint` on a ten-second ticker (and
  early on first sight of a host it has no contract for), keeps the parsed
  documents in memory, and re-downloads only what changed. Without
  `store_pod_endpoint` set on a front, an uploaded contract reaches it never and
  REST drift detection simply does not run there; the front says so once at
  start. A front's SELF contract (`self_spec_path`, still config) still crosses
  upward as a `spec_info` record, first batch after start then every 10 minutes.
- **MCP baselines flow BOTH ways** (2026-09-07). An observed `tools/list` goes
  up from the front that saw it as a `spec_info` record (format `mcp`), and the
  store pod serves those rows back down over the same `spec_endpoint`, so every
  front's per-edge baseline is the store's — the org-wide one — rather than
  what that one process happened to witness. Before this, a tool renamed while
  front-a was watching raised nothing when a stale client called through
  front-b, and restarting a front forgot the baseline. The newer observation
  wins on each front, and adopting a newer list over a live baseline reports
  the diff between them — the front that first listed a change may have had
  no baseline to diff it against; findings dedup by signature, one occurrence
  per front. An identical list converges every front on its EARLIEST sighting
  and the store keeps `loaded_at` for an unchanged MCP document, so the row —
  the "since this snapshot" anchor, the channel's change token — moves only
  when the list does, never between two fronts' stamps (2026-09-08). A
  `local-process` (stdio) row never seeds a front over the channel: its
  `peer_host` is the server's own name, not a host identity, so two tenants'
  builds of a same-named stdio server would seed each other's fronts in turn;
  a pod's own store still seeds its own. An MCP call to an edge a front has no
  baseline for asks for an early refresh, like an uncovered host.

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
   `:4318`, requiring the shared `spec_token`. It serves a provider's contract
   bound to an edge, in either format the store holds (uploaded OpenAPI,
   observed MCP snapshot); it exposes no calls, no findings, no settings and
   never the self contract, and it mutates nothing. The UI stays loopback
   (invariant 4), which is what lets this exist without weakening it.

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
durable evidence into postgres, then renames the file to
`/data/flanj.db.migrated`. It carries five things:

| Carried | Why it must survive |
|---|---|
| **Pinned calls** | the evidence the findings reference |
| **Findings** | stable ids + occurrence counts, so flag idempotency survives |
| **Edges** | the discovered dependency graph and its counts |
| **Settings** | the per-deployment KV — the Connect collector key and confirmed contact. Lose it and the deployment is no longer Connected |
| **Contracts** | the uploaded provider contracts, metadata **and** document — they exist nowhere else |

Unpinned window traffic is deliberately *not* copied: it is a rolling buffer
and refills within minutes.

**Contracts are carried, not rebuilt.** Before uploads existed, contracts came
from config (`spec_path`) and so "re-recorded themselves at start" from the file
on disk — that is no longer true and has not been since 2026-08-31. An uploaded
contract lives ONLY in the store. If the import did not carry it, it would be
gone, and every REST drift detection with it.

**The summary log line under-reports.** It prints only three of the five:

```
legacy sqlite store migrated into postgres  source=/data/flanj.db
  pinned_calls=… findings=… edges=…
```

Settings and contracts are imported in the same transaction but are not in that
line (`extension/flanjstore/extension.go`). Absence from the log is not absence
from the import — confirm them in the UI instead: Settings still shows
Connected, and the Contracts tab still lists every uploaded contract.

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
