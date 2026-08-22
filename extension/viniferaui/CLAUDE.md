# CLAUDE.md — extension/viniferaui

localhost UI **extension**. Own Go module. Serves the embedded Vue SPA + a read
API + the flag action.

## Role

- Serves the built SPA (`go:embed all:web/dist`) — the Dockerfile's node stage
  copies `ui/dist` over `web/dist` before the Go build.
- Read API: `GET /api/health | /api/edges | /api/calls | /api/findings`.
  `GET /api/edges` returns the auto-discovered **external** edges (inbound +
  outbound); internal same-team edges are classified out and never returned.
- Flag action: `POST /api/flag` → assembles the CP flag body from the stored
  call + finding and POSTs `cp_base_url/api/v1/flags` (Bearer `cp_deploy_token`);
  on success marks the call promoted (evict-after-promote).
- Peek-link relay: `POST /api/peek-link` (+ `/api/peek-link/revoke`) → relays
  copy-link mint / regenerate / revoke to the CP thread peek-link endpoints
  (channel attribution on the CP token record; the UI never holds the deploy
  token). Backs the flag flow's channel picker + Copy link + Revoke controls.

**Loopback only.** `ui_endpoint` is validated to a loopback address — the
collector is outbound-only; nothing serves off-host.

## Files

- `factory.go` — type `viniferaui`; builds a `confighttp.ServerConfig` from
  `ui_endpoint`.
- `extension.go` — server lifecycle; **lazy** store resolution (`resolveStore`):
  extensions can start in any order, so the store handle is resolved on first
  use, not at Start.
- `handlers.go` — the read API + flag handler. The flag body is built by
  `internal/promote` (which redacts the free-text message defense-in-depth) and
  must conform to `cp-flag-request.schema.json`.
- `embed.go` — `//go:embed all:web/dist`.
- `web/dist/index.html` — committed **placeholder**; the real SPA overwrites it
  at Docker build time (only the placeholder is tracked; `web/dist/assets/` is
  gitignored).
- `config.go` — frozen keys `ui_endpoint`, `integration_id`,
  `consumer_display_name`, `cp_base_url`, `cp_deploy_token` (CONTRACTS §8).

## Invariants

- **Loopback bind enforced** in `Config.Validate`.
- **Only the redacted call promotes.** The flag body carries the stored
  `RedactedCall` — raw bodies never existed past redaction-at-source.
- Flag idempotency key = `flag_<finding.id>` (re-flag returns the existing
  thread).

## Tests

The flag-body contract conformance + POST headers are tested in
`internal/promote` against `cp-flag-request.schema.json` and a stub server.
