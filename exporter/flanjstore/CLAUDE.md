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
  walks the batch keeping a flat record index, and classifies a failed write:
  `store.ErrRejected` drops THAT record (logged, batch goes on, batch ends
  `consumererror.NewPermanent`), everything else hands back the unapplied tail
  as `consumererror.NewLogs(err, remainingFrom(ld, idx))` — "Durability" below.
  `writeOne` applies one record, dispatching by `flanj.record.type` and
  stamping `flanj.call.id` onto an unstamped call record
  (`otlpattr.EnsureCallID`, BEFORE decoding); `remainingFrom` copies the
  records from a flat index onward, keeping their resource/scope grouping.
  `InsertFinding` pins the source call (pin-on-finding). `consumerCaps`
  declares `MutatesData` for the stamp — this is the terminal, sole consumer,
  so the fanout clones nothing.
- `config.go` — `Config` carries ONLY upstream's two failure sections,
  `sending_queue` + `retry_on_failure` (the same keys as a front's `otlphttp`),
  both on by default with defaults tuned for the last hop before persistence.
  Nothing says where to write: the extension owns the store.
- `exporter_test.go` — the launch-week-5 regressions over the REAL sqlite store
  behind a fail-on-command double: an outage and a mid-batch failure both land
  exactly once; a full queue refuses retryably; the default config keeps the
  queue + retry on. Review of #46 (2026-09-08): an UNSTAMPED call (the golden
  record as the SDK sends it) retried mid-batch lands as two rows, not three;
  a poison batch (id-less finding + a store-rejected record) attempts the
  rejected record ONCE and logs it while the records BEHIND it still land, and
  a transient failure still retries and lands. Partial retry (follow-up of #46,
  same day) is measured PER RECORD — `flakyStore.attemptsPerRecord`, since the
  batch-wide counters cannot tell a tail re-delivery from a whole-batch one:
  the applied head is attempted once, and a two-resource-group batch keeps its
  grouping (and its call-id stamps) across the retry.

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
  of proto-encoded batches by default — the `bytes` sizer counts the OTLP
  proto encoding, and the resident pdata is a multiple of it, so budget
  several × that in the pod's memory limit)**, four consumers, **rejecting
  when full**. The receiver ACKs on
  enqueue; a full queue hands the batch back as a retryable error (a 503), so
  the SDK's / a front's own retry stays the backstop instead of the pipeline
  blocking. No batching inside the queue: a received request is written whole,
  so a batch's findings stay behind the calls they pin.
- **`retry_on_failure`** — exponential backoff on a failed write, 1 s → 30 s,
  giving up after **15 minutes** (three times a front's 5, because this is the
  durable sink; `0` never gives up — memory is bounded by the queue either way).
  **What is dropped and what is retried** (review of #46, 2026-09-08;
  partial retry the same day): `consumeLogs` drops — per record, logged, the
  rest of the batch going on — a finding or `spec_info` that does not decode,
  and a finding with no `id` (the occurrence ledger cannot track it, so a retry
  would count it again). A record the STORE refuses (`store.ErrRejected`:
  sqlite `SQLITE_CONSTRAINT`, postgres SQLSTATE class 23/22 — deterministic on
  the record, so every attempt would fail the same way) is dropped the same
  way, logged with its id, and the records BEHIND it still get their write;
  the batch then ends `consumererror.NewPermanent`, so the retry sender stops
  after this ONE attempt. (That marks the whole request dropped in the
  helper's own accounting, which over-counts — only the named record was
  refused, and the precise line is the one this exporter logs.) Everything
  else (connection, lock, timeout) is retried for the full `max_elapsed_time`,
  and only the records that have NOT been applied — the failing one and
  everything after it — go back: `consumererror.NewLogs(err, remainingFrom(ld,
  idx))`, which `exporterhelper`'s `logsRequest.OnError` swaps in as the next
  attempt's request. The store was already idempotent, so this is about WORK,
  not correctness: before it, a 15-minute outage re-executed the whole
  already-applied head on every one of ~50 attempts, and on postgres each of
  those no-ops is still a transaction and an advisory lock. Without the
  rejection split, a poison batch pinned a queue consumer for 15 minutes on
  every re-delivery.
- **Idempotent writes** — `InsertCall` is `ON CONFLICT (id) DO NOTHING` (edge
  `call_count` bumps only for a new row) — keyed on `flanj.call.id`, which
  the SDK never emits and only the drift processor stamps, so a call reaching
  a store pod's `:4318` with no front (pipeline `[flanjredaction]` only) has
  none: `consumeLogs` stamps it onto the QUEUED record (`EnsureCallID`) before
  decoding, so a retry re-uses it instead of minting a fresh id per attempt
  (review of #46 — a 2-call batch retried mid-batch used to land as 3 rows,
  `call_count` 3); `PutSpecInfo` is an upsert; and
  `InsertFinding` keeps an **occurrence ledger** keyed by the finding record's
  own id (`internal/store` `recordOccurrence`, 2026-09-07), so a re-delivered
  finding counts once — a repeat is a NEW detection with a NEW id. A retried
  batch, whole or partial, therefore cannot duplicate rows or double an
  `occurrence_count`. The exporter never dedups itself: the ledger is in the
  store, where a front re-sending to another postgres pod is also caught.
  A ledger row is kept for the **re-delivery horizon** — `occurrenceTTL`, one
  hour on the store's own clock: this exporter's 15-minute retry budget plus a
  front's 5-minute `otlphttp` one, rounded up. Change `max_elapsed_time` far
  past that and the TTL has to move with it, or a retry landing after the hour
  is counted a second time. Until 2026-09-08 the row was pruned with the CALL
  it named instead, which a busy window rolls past in seconds — that is the
  bug the TTL replaces (`internal/store` `pruneOccurrences`).

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
"queued". What is in the queue when the PROCESS dies is lost — at most 64 MiB
of proto-encoded batches, the same at-most-once-across-a-crash a front's queue
already has (`docs/STORE.md`).
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

Store behaviour (ring buffer, pin, promote, the occurrence ledger and its TTL
prune, and the `ErrRejected` classification on both backends —
`rejected_test.go`) is tested in `internal/store`; this exporter's queue/retry/idempotency contract in
`exporter_test.go` (above). The e2e postgres lane drives the real thing
(`e2e/tests/store-durability.spec.ts`: DB stopped for longer than the SDK's own
retry budget, a call and its finding driven meanwhile, both present exactly once
after recovery).
