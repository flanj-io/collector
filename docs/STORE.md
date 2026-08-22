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
