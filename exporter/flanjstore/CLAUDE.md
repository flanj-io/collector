# CLAUDE.md — exporter/flanjstore

Store **exporter** — the WRITER side of the store. Own Go module.

## Role

Terminal stage of the logs pipeline. Writes `call` records as `RedactedCall`
rows and `finding` records as `Finding` rows. It does **not** open the database —
it discovers the single-owner `flanjstore` **extension** via
`host.GetExtensions()` at Start and writes through it.

`contract_snapshot` records (v0.5) are consumed UPSTREAM by the drift
processor, which re-emits them as `spec_info` records (format `"mcp"`) —
this exporter routes those to `PutSpecInfo` like any spec_info; a raw
`contract_snapshot` reaching the default branch is dropped by `validCall`
(no method/route), per the §2 unknown-record rule.

`… → flanjdrift → [flanjstore exporter] → store: sqlite | postgres (owned by the extension)`

## Files

- `factory.go` — `NewFactory()`, type `flanjstore` (same type name as the
  extension; that is fine — different component kinds). Logs exporter via
  `exporterhelper.NewLogs` with `WithStart`, `WithRetry` and `WithQueue`
  (see "Durability").
- `exporter.go` — `start` resolves the store (`store.Provider`); `consumeLogs`
  dispatches by `flanj.record.type`. `InsertFinding` pins the source call
  (pin-on-finding).
- `config.go` — `Config` carries ONLY upstream's two failure sections,
  `sending_queue` + `retry_on_failure` (the same keys as a front's `otlphttp`),
  both on by default with defaults tuned for the last hop before persistence.
  Nothing says where to write: the extension owns the store.
- `exporter_test.go` — the launch-week-5 regressions over the REAL sqlite store
  behind a fail-on-command double: an outage and a mid-batch failure both land
  exactly once; a full queue refuses retryably; the default config keeps the
  queue + retry on.

## Invariants

- **Never open a second store handle.** The extension is the single in-process
  owner; the exporter only holds the shared `store.Store` interface (backend —
  embedded sqlite or shared postgres — is the extension's concern). Extensions
  all start before pipeline components, so the store is already open when
  `start` runs.
- **Record order is not load-bearing.** Within one batch the drift processor
  appends findings AFTER the calls (trailing `ResourceLogs`), so calls insert
  first; but across the front→store hop a finding can still outrun its call
  (re-delivered partial batch, cross-request reordering, a call re-sent after
  eviction). The store handles that itself: `InsertFinding` pins the source
  call if it exists, and `InsertCall` late-pins a new row that a finding already
  references (+ repairs the edge drift attribution) — see `internal/store`
  `latePin`. The exporter never reorders or buffers to compensate.

## Durability (launch-week item 5, 2026-09-07)

Until 2026-09-07 this exporter built `exporterhelper.NewLogs` with no queue, no
retry: `consumeLogs`' error went straight back to the pipeline as a permanent
failure, and the batch — the calls AND the findings that pinned them — was
gone. On the single-pod shape the receiver refuses the batch synchronously
(503) and the SDK's OTLP exporter retries, so a short outage was already
covered UPSTREAM (measured: postgres stopped 13 s, a call driven mid-outage
landed after recovery, 19 × 503). Two paths were not:

- a write that fails AFTER the batch was accepted by the pipeline (the receiver
  has answered; nothing upstream will re-send);
- the tiered front→store hop, covered only up to the front's `otlphttp`
  `max_elapsed_time` (5 min).

Now the sender chain is **queue → retry → write**, with the store idempotent
on both record ids:

- **`sending_queue`** — a bounded IN-MEMORY queue, sized in **bytes (64 MiB
  default)**, four consumers, **rejecting when full**. The receiver ACKs on
  enqueue; a full queue hands the batch back as a retryable error (a 503), so
  the SDK's / a front's own retry stays the backstop instead of the pipeline
  blocking. No batching inside the queue: a received request is written whole,
  so a batch's findings stay behind the calls they pin.
- **`retry_on_failure`** — exponential backoff on a failed write, 1 s → 30 s,
  giving up after **15 minutes** (three times a front's 5, because this is the
  durable sink; `0` never gives up — memory is bounded by the queue either way).
  Every error from the store is treated as transient; `consumeLogs` DROPS
  malformed records itself (logged) so no batch can be permanently poisoned.
- **Idempotent writes** — `InsertCall` is `ON CONFLICT (id) DO NOTHING` (edge
  `call_count` bumps only for a new row); `PutSpecInfo` is an upsert; and
  `InsertFinding` keeps an **occurrence ledger** keyed by the finding record's
  own id (`internal/store` `recordOccurrence`, 2026-09-07), so a re-delivered
  finding counts once — a repeat is a NEW detection with a NEW id. A retried
  batch, whole or partial, therefore cannot duplicate rows or double an
  `occurrence_count`. The exporter never dedups itself: the ledger is in the
  store, where a front re-sending to another postgres pod is also caught.

**Why the queue does not add a second writer.** Non-negotiable #1 is one
OWNER (the extension holds the one handle) and one WRITER (this exporter is the
only component that inserts). The queue's consumers are goroutines INSIDE this
exporter, writing through the same handle; there is no second handle, no second
process, no second component. Before the queue, writers were already
concurrent — one per in-flight receiver request — so four consumers is a
tighter bound than before, not a looser one. The sqlite backend serialises
them behind its mutex; postgres resolves them per call id (advisory locks), as
it already had to for N pods.

**The trade-off, stated plainly.** The ACK point moves from "written" to
"queued". What is in the queue when the PROCESS dies is lost — at most 64 MiB,
the same at-most-once-across-a-crash a front's queue already has (`docs/STORE.md`).
Before this change the same crash lost nothing queued, because nothing was:
the SDK still held the batch. Against that: a store outage while the process
lives no longer drops a single accepted batch for 15 minutes, on every shape.
A persistent (file-backed) queue would remove the crash window and is the
next step if it ever matters; it needs a storage extension and a volume on the
store pod, so it is not the default.

**Known limit.** The store API takes no `context`, so the exporterhelper's
per-attempt timeout (default 5 s) cannot interrupt a write that hangs inside
the driver; it only bounds the sender's bookkeeping. A black-holed postgres
socket is bounded by the OS / driver timeouts, not by `timeout`. That is why
`timeout` is deliberately NOT exposed on this exporter — it would promise
something the write path cannot honour.

## Tests

Store behaviour (ring buffer, pin, promote, the occurrence ledger) is tested in
`internal/store`; this exporter's queue/retry/idempotency contract in
`exporter_test.go` (above). The e2e postgres lane drives the real thing
(`e2e/tests/store-durability.spec.ts`: DB stopped for longer than the SDK's own
retry budget, a call and its finding driven meanwhile, both present exactly once
after recovery).
