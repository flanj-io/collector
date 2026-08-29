# ui/ — Flanj Collector local UI

Vue 3 + Vite single-page app, served **embedded** by the `flanjui` extension
(`go:embed`). Built to static assets; no runtime server of its own.

## What it shows

- **Overview** — health headline + the drift findings (live-vs-spec rows: expected-per-spec vs
  actual, the violated `location`; spec version-diff rows: v1→v2 breaking changes) with the
  **correlation keys** (request-id / idempotency-key / trace-id) that make a finding actionable.
  v0.5 MCP: one health headline per observed MCP server (output mismatch → breaking definition
  change → clean) and the **Local notices** band (stale-client + description-only changes —
  visible to you only, never a flag control; the sub-line names the provider only while every
  notice points at one).
- **Traffic** — live tail of redacted calls (filterable). MCP rows render the tool in the
  method/path slot with a `TOOL` chip and an ok/error status from `isError`; the method facet
  filters MCP rows as `TOOL` (never `tools/call`) and the status facet treats them as ok
  (`2xx`) / error (`err`) — no HTTP class.
- **Contracts** — the loaded provider specs. MCP servers list here spec-free (the observed
  `tools/list` IS the contract): per-tool rows (`input + output contract` / `input contract
  only` with the honest no-output-contract note), definition-change rows with the
  BREAKING / NON-BREAKING / DESCRIPTION badge and both snapshot columns.
- **Flag this** (on a Contracts finding) — the Flag sheet: evidence line, "what leaves this
  collector", an editable optional message, **Create thread** → the success state shows the
  **thread link** (auto-selected, auto-copied where the clipboard allows) with **Copy thread link**,
  **Copy link + message** (fixed template, request ID first) and **Open thread** (owner handoff in
  a new tab). Not Connected yet? The sheet shows the Connect prompt inline and unlocks the moment
  the contact confirms. A flagged finding keeps a chip: `In thread · <turn> · opened ×N`.
  MCP findings swap in the deck's MCP evidence / disclosure / prefill copy; the IDs line uses the
  JSON-RPC wording only while the client-generated id is the **sole** correlation key (mixed keys
  → the standard count line + an honest client-id note). Flaggable definition changes keep a
  disabled Flag control (call-less — a known v0.5 limit); local notices never show one.
- **Threads** — state of every thread this collector created (provider · endpoint · evidence ·
  status · opened · last reply · link) with Open / Close thread / Reopen / Copy thread link /
  Replace link; `#threads/<thread_id>` deep-links and highlights a row.
- **Settings** — **Connect to Flanj network**: org name + contact email (+ your name, this
  collector's address); one-click email confirmation; Resend / Change contact.

## Data source

Reads the collector's localhost API: `/api/health`, `/api/edges`, `/api/calls`, `/api/findings`,
`/api/contracts`, `/api/contracts/spec`, `/api/connect`, `/api/threads`; writes through the relay
(`POST /api/connect`, `/api/flag`, `/api/threads/:id/{open,close,reopen,replace-link}`) — every
write carries `X-Flanj-UI: 1` + JSON (`src/api.ts`). Findings link to their source call by
`source_call_id` to render correlation keys. No user-facing "peek / magic link / invite" wording —
`extension/flanjui/naming_test.go` scans these sources.

## Files

- `src/App.vue` — tabs, polling, the finding cards and chips. `src/FlagSheet.vue` — the Flag sheet.
  `src/ConnectPanel.vue` — Connect. `src/ThreadsTab.vue` — the Threads list.
- `src/threads.ts` — pure helpers (turn/link labels, chip, paste text, prefilled message) with
  `src/threads.test.ts` (vitest: `npm test`). `src/api.ts`, `src/clipboard.ts`, `src/types.ts`.
- `src/mcp.ts` — the v0.5 MCP pure helpers (verbatim deck copy: badges, headlines, local notices,
  tool rows, traffic facets, flag-sheet lines; `snapshotTimes` parses the definition_change detail
  tail — its regex is pinned by a collector Go test, `internal/drift`) with `src/mcp.test.ts`.

## Develop

```bash
npm install
npm run dev        # Vite on :5336, proxying /api -> a running collector (:5335)
npm run build      # -> dist/  (embedded by the extension)
npm test           # vitest over the pure helpers
npm run typecheck  # vue-tsc over src/ INCLUDING the .vue templates
npm run dev:mock   # proxy /api to :5399 (bring your own mock relay)
```

`npm run typecheck` is the only check that reads a `<template>`: `vite build` strips types
without checking them and `npm test` only loads the `.ts` unit files, so before it existed a
wrong prop, a renamed field or a null deref inside a template shipped green. CI runs it in the
`docker-build` job next to the vitest step. `tsconfig.json` turns on the `checkUnknown*`
template options (off by default in Vue Language Tools 3) and pins
`useDefineForClassFields: false` — esbuild reads that key, and letting it default off `target`
would change the shipped bundle's class-field emit. `typescript` is held on the 5.x line
deliberately: npm's `latest` is now TypeScript 7, which drops the `./lib/tsc` export `vue-tsc`
loads and fails with `ERR_PACKAGE_PATH_NOT_EXPORTED`.

## Build → embed pipeline

The Dockerfile's node stage runs `npm run build` and copies `dist/` over
`../extension/flanjui/web/dist`, which the Go build embeds. In the repo only
the placeholder `web/dist/index.html` is tracked; built assets are gitignored.
