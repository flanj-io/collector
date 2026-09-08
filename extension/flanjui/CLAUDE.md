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
  `/api/findings` rows also carry two LOCAL read-API joins that never touch
  `model.Finding`: the ack state, and **`peer_host`** — the host of the
  finding's pinned source call, which is how the Contracts tab pairs a finding
  with its provider card (`ui/src/contracts.ts` `findingBelongsToContract`).
  It is joined here rather than in the browser because `/api/calls` returns only
  the 200 newest rows while `source_call_id` is frozen at the first occurrence,
  so the SPA's own lookup lost the pairing as soon as the evidence call aged out
  and the provider split into two cards.
  **Every read route answers a store failure the same way**: `503
  {error: "store_error", message: …}`, with the raw error going to the log and
  nowhere else — a pgx connection error is the DSN in prose, and the read routes
  used to hand it to the browser verbatim at 500.
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
    local_ui_url, dashboard_url?}` refreshed from `me` (≤1 CP call / 10s per pod; the UI polls it
    every 5s while pending). `dashboard_url` is the SPA's one door out (the
    Connected pill): present only while Connected AND the collector holds an
    address a BROWSER can open — `cp_public_url`, or a `cp_base_url` whose host
    is not obviously non-public (`dashboardURL` / `obviouslyNonPublicHost`:
    private IP literals, single-label names, anything carrying an `svc` or
    `cluster` label, reserved suffixes, and — since 2026-09-08 — any two-label
    name whose last label is not a common public TLD, which is the k8s
    `service.namespace` short form a Helm chart renders by default,
    `http://cp-api.flanj:3001`).
    It is never minted from the promote client's base alone: that is where the
    collector's requests go (docker DNS, a k8s Service), not where a laptop
    can (launch-week item 8, 2026-09-07). Absent → the SPA keeps the pill a
    Settings button.
  - `POST /api/flag {finding_id, message?, provider_display_name?}` — **Create
    thread**: `403 {error: not_flaggable}` for LOCAL-ONLY finding kinds
    (`model.Finding.Flaggable()` — `stale_client`, and only `stale_client`): the
    evidence rule is enforced server-side in the relay, never just by UI
    absence, so a hand-crafted request cannot promote a local notice.
    **CALL-LESS flagging, widened v1p4-2026-09-08:** the body omits `call` for
    ANY finding with no source call — a `definition_change` (call-less by
    nature, qfix2-2026-08-26) and, since v1p4, a version diff or anything else
    that reached the store without one. `400 finding_has_no_call` is gone from
    this relay: the message carries the ask, `promote.Build` never sends an
    empty one, and a Flag control that answers 400 is worse than no control.
    An EVICTED call still refuses (`404 call_not_found`) for every kind but
    `definition_change` — the sheet showed an "Evidence (1)" line for that call,
    and downgrading the flag silently would create a thread the operator did not
    mean to create. `412 {error: not_connected |
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
  - `POST /api/edges/thread {host, message, request_id}` — **Start a thread**
    from an EDGE row (v1 phase 4): a MESSAGE-ONLY thread. Same Connect gate as
    the flag, from the same helper (`requireConnectedForThread`) so the two
    doors answer with the same 412s. Outbound rows only (`404 edge_not_found`
    for an unknown or INBOUND host — an inbound `peer_host` is a forgeable XFF
    first hop and is never identity); the message is required
    (`400 missing_fields`) because it is the entire artifact, and it passes the
    redaction floor like every other free text. On the wire: no `call`, no
    `finding`, and `provider_host` naming the edge so the thread page anchors
    its provider slot on the domain rather than an unattributed asserted name.
    Idempotency key = `edge_<host>_<request_id>`, the request id minted ONCE by
    the sheet — so a retry replays and two different questions about one edge
    are two threads. No finding id to key a local record on, so the thread link
    is parked under `thread.link.<thread_id>` (the existing key for a thread
    with no finding record) → `{thread_id, thread_public_id, thread_url, state,
    status}`.
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

- **Agent-facing drift read surface (`mcp.go`) — a read-only MCP server at
  `/mcp` on THIS listener.** Streamable HTTP (`github.com/modelcontextprotocol/go-sdk`),
  stateless, JSON responses; four tools, every one annotated read-only:
  `drift_summary` · `list_edges` · `list_findings` (filters: `edge`, `kind`,
  `severity`, `include_acknowledged`, `limit`) · `get_finding`. Named in
  `launch-plan.md`'s one-line description of what launches; the prerequisite for
  the AI-reliability directory submissions.
  - **It is a route, not a listener.** The whole security story is that
    `ui_endpoint` is already validated loopback (`Config.Validate`), so the agent
    surface inherits the outbound-only posture with nothing new bound. On top of
    that it carries `http.CrossOriginProtection` (a page in the operator's own
    browser must not be able to drive it) and the SDK's own localhost
    DNS-rebinding check. Non-browser clients send neither `Sec-Fetch-Site` nor
    `Origin` and pass untouched.
  - **Read-only, and that is structural.** There is no tool for any route behind
    `guardMutating` / `guardLocalMutating` — no flag, no acknowledge, no
    connect, no contract upload. An agent does not satisfy the browser guard and
    is not meant to; suggest-and-approve is a later slice (v4 in
    `mvp-roadmap.md`), and this is NOT that.
  - **One builder for the rows.** `handlers.go` exposes `findingRows` and
    `edgeRows`; both `/api/findings` / `/api/edges` and the MCP tools read
    through them, so "the agent and the human see the same truth" is structural
    rather than two call sites promising to stay in step.
    `TestMCPFindingRowsAreTheUIRows` asserts the agent's rows are byte-identical
    to the live REST route's — against the route, not a fixture, because a
    fixture would freeze today's shape and let the surfaces drift under it.
  - **NO RAW BODY CROSSES, by construction.** No tool returns a body, a header
    map, or the full URL — `get_finding`'s evidence summary (`mcpSourceCall`) is
    an explicit ALLOWLIST of scalars, so a new field on `model.RedactedCall`
    cannot ride out to an agent just because a struct was passed through whole.
    The URL is excluded on purpose: a query string is the one place a credential
    rides outside a body, and `route` answers every question about which
    endpoint drifted. `TestMCPNeverEmitsARawBody` plants a canary in every body,
    both header maps and the URL query, drives every tool with every widening
    argument, and scans every byte of every answer.
  - **The floor runs once more on the way out** (`redactFindingValues`), over
    `expected` / `actual` / `detail` and nothing else — the only fields carrying
    observed content (`internal/drift` `actualFromValue` can quote a scalar).
    The agent's next hop may be a model provider outside this environment, which
    the browser's next hop is not. The floor is idempotent and add-only, so a
    clean value is returned byte for byte and the parity above holds; structural
    fields (ids, hashes, timestamps, kind, rule, endpoint, field_path) are never
    run through it — a redactor over an identifier could only corrupt it.
  - **An empty answer is never an all-clear on its own.** Every answer carries
    `evidence`: the per-call validation tally read STRAIGHT off
    `model.RedactedCall.Validated`, never re-derived from facts about the edge
    (that mirror is what reported CONFORMING over calls nothing had validated).
    A collector with no traffic says so; a collector with traffic and no
    contract says so and names the reason; an `edge` argument naming a host this
    collector has never observed is answered as an unknown edge with the known
    ones listed, never as "no findings". None of these is an error.
  - **Protocol.** The Go SDK speaks 2026-07-28 and negotiates down through
    2025-11-25 — what `@modelcontextprotocol/sdk` 1.30.0 speaks, the version the
    e2e `org-app` and `mock-mcp` harnesses pin — to 2024-11-05, so no pin of our
    own is needed and there is no mismatch to work around.
  - **No new config key.** The surface is on by default and has no switch: the
    read API beside it has no auth either, so gating one and not the other would
    be theatre. CONTRACTS §8 is unchanged.

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
- `mcp.go` — the agent-facing MCP server: tool definitions, the evidence
  tally, the outbound redaction pass and the `/mcp` handler.
- `embed.go` — `//go:embed all:web/dist`.
- `web/dist/index.html` — committed **placeholder**; the real SPA overwrites it
  at Docker build time (only the placeholder is tracked; `web/dist/assets/` is
  gitignored).
- `config.go` — frozen keys `ui_endpoint`, `integration_id`,
  `consumer_display_name`, `provider_display_name`, `cp_base_url`, `cp_public_url`
  (optional, browser-facing — validated at boot: absolute http(s), no
  credentials), `cp_deploy_token` (CONTRACTS §8).
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

- **Loopback bind enforced** in `Config.Validate` — it is what the agent MCP
  surface's posture rests on too.
- **The agent surface is read-only and body-free.** No MCP tool may write, and
  no MCP tool may return a call body, a header map or a full URL.
- **Only the redacted call promotes.** The flag body carries the stored
  `RedactedCall` — raw bodies never existed past redaction-at-source.
- Flag idempotency key = `flag_<finding.id>` (re-flag returns the existing
  thread). Question idempotency key = `edge_<host>_<request_id>` — a different
  namespace, so a question can never collide with a flag.
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
`mcp_test.go` drives the agent surface with a REAL MCP client over streamable
HTTP (never a hand-rolled JSON-RPC POST): tool listing and the read-only
boundary, the byte-for-byte parity with `GET /api/findings`, per-edge queries
including a call-less `definition_change` reaching its edge through its contract
binding, the three honest-empty answers, the canary scan, the outbound redaction
pass and its idempotence, the cross-site refusal, and the store-failure answer.
`handlers_test.go` drives the relay end to end against a stub CP + an in-memory
store (`fakestore_test.go`): guards on every mutating route, the Connect →
pending → confirm → flag → threads → open → close/reopen → replace-link walk,
412/403 pass-through, CP-unreachable behaviour, and a log observer proving the
key / handoff / thread-link tokens never hit a log line. `naming_test.go` is the
denylist scan. Run: `go test ./...` in this directory (own module).
