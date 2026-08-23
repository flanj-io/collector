# ui/ — Vinifera Collector local UI

Vue 3 + Vite single-page app, served **embedded** by the `viniferaui` extension
(`go:embed`). Built to static assets; no runtime server of its own.

## What it shows

- **Overview** — health headline + the drift findings (live-vs-spec rows: expected-per-spec vs
  actual, the violated `location`; spec version-diff rows: v1→v2 breaking changes) with the
  **correlation keys** (request-id / idempotency-key / trace-id) that make a finding actionable.
- **Traffic** — live tail of redacted calls (filterable).
- **Contracts** — the loaded provider specs.
- **Flag this** (on a Contracts finding) — the Flag sheet: evidence line, "what leaves this
  collector", an editable optional message, **Create thread** → the success state shows the
  **thread link** (auto-selected, auto-copied where the clipboard allows) with **Copy thread link**,
  **Copy link + message** (fixed template, request ID first) and **Open thread** (owner handoff in
  a new tab). Not Connected yet? The sheet shows the Connect prompt inline and unlocks the moment
  the contact confirms. A flagged finding keeps a chip: `In thread · <turn> · opened ×N`.
- **Threads** — state of every thread this collector created (provider · endpoint · evidence ·
  status · opened · last reply · link) with Open / Close thread / Reopen / Copy thread link /
  Replace link; `#threads/<thread_id>` deep-links and highlights a row.
- **Settings** — **Connect to Vinifera network**: org name + contact email (+ your name, this
  collector's address); one-click email confirmation; Resend / Change contact.

## Data source

Reads the collector's localhost API: `/api/health`, `/api/edges`, `/api/calls`, `/api/findings`,
`/api/contracts`, `/api/contracts/spec`, `/api/connect`, `/api/threads`; writes through the relay
(`POST /api/connect`, `/api/flag`, `/api/threads/:id/{open,close,reopen,replace-link}`) — every
write carries `X-Vinifera-UI: 1` + JSON (`src/api.ts`). Findings link to their source call by
`source_call_id` to render correlation keys. No user-facing "peek / magic link / invite" wording —
`extension/viniferaui/naming_test.go` scans these sources.

## Files

- `src/App.vue` — tabs, polling, the finding cards and chips. `src/FlagSheet.vue` — the Flag sheet.
  `src/ConnectPanel.vue` — Connect. `src/ThreadsTab.vue` — the Threads list.
- `src/threads.ts` — pure helpers (turn/link labels, chip, paste text, prefilled message) with
  `src/threads.test.ts` (vitest: `npm test`). `src/api.ts`, `src/clipboard.ts`, `src/types.ts`.

## Develop

```bash
npm install
npm run dev        # Vite on :5336, proxying /api -> a running collector (:5335)
npm run build      # -> dist/  (embedded by the extension)
npm test           # vitest over the pure helpers
npm run dev:mock   # proxy /api to :5399 (bring your own mock relay)
```

## Build → embed pipeline

The Dockerfile's node stage runs `npm run build` and copies `dist/` over
`../extension/viniferaui/web/dist`, which the Go build embeds. In the repo only
the placeholder `web/dist/index.html` is tracked; built assets are gitignored.
