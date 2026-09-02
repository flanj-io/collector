# CLAUDE.md — extension/flanjui

localhost UI **extension**. Own Go module. Serves the embedded Vue SPA + a read
API + the flag action.

## Role

- Serves the built SPA (`go:embed all:web/dist`) — the Dockerfile's node stage
  copies `ui/dist` over `web/dist` before the Go build.
- Read API: `GET /api/health | /api/edges | /api/calls | /api/findings | /api/contracts`.
  `GET /api/edges` returns the auto-discovered **external** edges (inbound +
  outbound), each carrying an observed `rpm` (calls over the trailing minute);
  internal same-team edges are classified out and never returned.
  `GET /api/contracts` lists the provider contracts the drift processor loaded;
  `GET /api/contracts/spec?integration=...` serves the raw spec document.
  **v0.5 MCP (Step D): no new routes.** `/api/contracts` rows may carry format
  `"mcp"` (an observed `tools/list` snapshot — `…/spec` then serves the raw
  snapshot JSON verbatim, which the SPA parses into per-tool rows); `/api/calls`
  rows pass the additive `transport` / `mcp_*` fields and
  `correlation.client_request_id` through untouched; `/api/findings` carries the
  three MCP kinds plus the optional `snapshot_observed_at` (CONTRACTS §4). All
  MCP rendering lives in the SPA (`ui/src/mcp.ts`).
- **Control-plane relay (v0.1a — CONTRACTS §5, spec Step 4b).** The UI never
  holds a bearer; the relay does, and every mutating route is guarded
  (`guard.go`): POST only (405), `X-Flanj-UI: 1` (403 `ui_header_required`),
  `Content-Type: application/json` (415), no foreign `Origin` (403
  `forbidden_origin`), never a CORS header. Errors are `{error, message}` with
  the deck's copy (`messages.go`). Tokens, handoffs and the collector key never
  reach a log line.
  - `GET|POST /api/connect` (`connect.go`) — **Connect**: `POST {consumer_display_name,
    contact_email, contact_display_name?, local_ui_url?}` registers the deployment
    with the CP (`register`, Bearer `cp_deploy_token` — used ONLY for the first
    Connect of a deployment), persists the once-returned **collector key** +
    contact in the store settings KV (`connect.*`, per deployment, shared by
    every pod; never returned to the UI) → `202 {status:"pending", …}` (`200`
    when already connected). Display names pass the redaction floor
    (`internal/redact`, like the flag message) before they are sent or stored.
    Once a key exists EVERY later register (resend / change of contact) goes out
    with Bearer **collector key** (`RegisterWithKey`, CONTRACTS-CP §5.1): the
    same email = resend, key unchanged; a new email = a new pending contact on
    the same collector, key unchanged — the previously confirmed contact stays
    usable for threads (`confirmed_contact_email` from `me`) until the new one
    confirms. `GET` → `{status: disconnected|pending|connected,
    consumer_display_name, contact_email, contact_display_name,
    confirmed_contact_email, collector_public_id, registered_at, confirmed_at,
    local_ui_url}` refreshed from `me` (≤1 CP call / 10s per pod; the UI polls it
    every 5s while pending).
  - `POST /api/flag {finding_id, message?, provider_display_name?}` — **Create
    thread**: `403 {error: not_flaggable}` for LOCAL-ONLY finding kinds
    (`model.Finding.Flaggable()` — `stale_client`, and only `stale_client`): the
    evidence rule is enforced server-side in the relay, never just by UI
    absence, so a hand-crafted request cannot promote a local notice. A
    `definition_change` is CALL-LESS by nature, so `400 finding_has_no_call` is
    lifted for that kind (qfix2-2026-08-26) and the body omits `call`; every
    other kind still needs its failing call. `412 {error: not_connected |
    contact_unconfirmed}` before Connect /
    the FIRST confirmation — the gate is "a confirmed contact exists"
    (`confirmed_contact_email` non-null), so a new pending contact never blocks
    it (a never-confirmed contact is re-checked against the CP right then, so it
    unlocks the moment the click lands); otherwise assembles the CP
    flag body from the stored call + finding (`internal/promote`), POSTs it with
    the collector key, persists a per-finding thread record (`threads.go`,
    settings KV `thread.finding.<finding_id>` — ids, endpoint, provider, the
    current thread link — plus the reverse pointer `thread.id.<thread_id>` →
    the finding id) and marks the call promoted →
    `{thread_id, thread_public_id, thread_url, state, status, finding_id}`.
    **Every thread write in `threads.go` is a single blind `PutSetting`, never a
    read-modify-write** — the KV has no compare-and-swap, so a read-then-write on
    a key two pods share is a lost update waiting to happen. The pointer is only
    ever written with a REAL finding id (an empty one would orphan the record
    that holds the live link), and `findThreadByID` verifies the record it loads
    still names the thread that was asked for: `thread.finding.<id>` is rewritten
    in place on re-flag and the KV has no delete, so a superseded pointer
    survives and must resolve to "no local record", never to the new thread.
    No email field; nothing is emailed.
  - `GET /api/threads` — ONE call to the CP's §5.5a list (Bearer collector key,
    most-recently-active first), each row joined to the local record by thread
    id. The envelope is the collector's own internal shape:
    `{threads, count, total, limit, has_more}` — §5.5a has no cursor, so
    `has_more` is what stops 200 rows from silently becoming the whole truth.
    The list path WRITES NOTHING except recovering a missing pointer from the
    legacy `threads.index` (lazy, per listed thread, a single blind write of the
    real finding id; the legacy array is never cleared — a previous version
    still lists from it). A row with no local record still renders, with an
    empty `thread_url` the UI turns into a disabled Copy thread link. Not
    connected → `412 not_connected`, no CP configured → `503 cp_not_configured`
    (the same codes as the summary route): an empty list would tell a collector
    that HAS threads that it has none. CP failure → the CP's code / `502
    cp_unreachable`, never a stale list. `GET /api/threads/{id}/summary`.
  - `POST /api/threads/{id}/open` → `{owner_url, expires_at}` — a 10-minute
    single-use owner handoff the UI opens in a new tab (never stored/logged);
    `/close` · `/reopen` → the CP's `{state, closed_at, reopened_at}`;
    `/replace-link` → `{thread_url, expires_at, revoked}` (revoke + mint). The
    CP kills every outstanding token the moment it mints the new one, so the new
    link is persisted for EVERY row: onto the finding record when there is one,
    otherwise onto `thread.link.<thread_id>` — a key only that thread's Replace
    link writes, blind.
  - `GET /api/health` also carries `connect_status` (from the store only) and
    the configured display names.

**Loopback only.** `ui_endpoint` is validated to a loopback address — the
collector is outbound-only; nothing serves off-host.

## Files

- `factory.go` — type `flanjui`; builds a `confighttp.ServerConfig` from
  `ui_endpoint`.
- `extension.go` — server lifecycle; **lazy** store resolution (`resolveStore`):
  extensions can start in any order, so the store handle is resolved on first
  use, not at Start.
- `handlers.go` — the read API + the flag handler. The flag body is built by
  `internal/promote` (which redacts the free-text message defense-in-depth) and
  must conform to `cp-flag-request.schema.json`.
- `connect.go` — Connect state (store settings KV), `me` refresh cache, the
  `/api/connect` handlers. `threads.go` — the per-finding thread records + the
  `/api/threads…` handlers. `guard.go` — the mutating-route guard + CP error
  mapping. `messages.go` — every user-facing relay string (deck copy).
- `embed.go` — `//go:embed all:web/dist`.
- `web/dist/index.html` — committed **placeholder**; the real SPA overwrites it
  at Docker build time (only the placeholder is tracked; `web/dist/assets/` is
  gitignored).
- `config.go` — frozen keys `ui_endpoint`, `integration_id`,
  `consumer_display_name`, `provider_display_name`, `cp_base_url`, `cp_deploy_token` (CONTRACTS §8).
  `provider_display_name` is the FLAG's fallback provider name only — it stopped
  naming edges when contracts moved into the UI (its edge linkage came from the
  config spec's `peer_host`).
- `contracts_upload.go` — `POST /api/contracts/{preview,upload,remove}`: the only
  way a provider contract enters this collector. Parse-before-persist, mandatory
  host binding, integration id DERIVED from the host, replace with one previous
  document kept, and the version diff on replace. The binding is
  **`host[:port]`** — the CONTRACTS §2 edge key, matched by exact string — so
  `normalizeHost` KEEPS a trailing port and strips only scheme, userinfo, path,
  query, fragment and the scheme's OWN default port (`https://h:443` → `h`, via
  `internal/edge.StripDefaultPort`, the same rule the SDK applies at capture).
  A non-default port is a different listener and must never be folded away. Nothing on this path reaches
  the control plane — an uploaded contract never leaves.

## Invariants

- **Loopback bind enforced** in `Config.Validate`.
- **Only the redacted call promotes.** The flag body carries the stored
  `RedactedCall` — raw bodies never existed past redaction-at-source.
- Flag idempotency key = `flag_<finding.id>` (re-flag returns the existing
  thread).
- **Create thread needs Connect + a confirmed contact; viewing local data never
  does.** The collector key is read from the store on every relay call (never
  cached in a per-pod file) and never logged or returned to the UI.
- **No user-facing "peek / peek link / magic link / minting / invite / invitee /
  previewer"** — `naming_test.go` scans `ui/src/**/*.vue|ts` user-facing text +
  the relay messages (wire identifiers like `peek_url`, `/api/peek/*`, `vpeek_`
  are allowed).

## Tests

The flag-body contract conformance + POST headers are tested in
`internal/promote` against `cp-flag-request.schema.json` and a stub server.
`handlers_test.go` drives the relay end to end against a stub CP + an in-memory
store (`fakestore_test.go`): guards on every mutating route, the Connect →
pending → confirm → flag → threads → open → close/reopen → replace-link walk,
412/403 pass-through, CP-unreachable behaviour, and a log observer proving the
key / handoff / thread-link tokens never hit a log line. `naming_test.go` is the
denylist scan. Run: `go test ./...` in this directory (own module).
