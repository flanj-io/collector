# ui/ — Vinifera Collector local UI

Vue 3 + Vite single-page app, served **embedded** by the `viniferaui` extension
(`go:embed`). Built to static assets; no runtime server of its own.

## What it shows

- **Overview** — health headline + the drift findings (live-vs-spec rows: expected-per-spec vs
  actual, the violated `location`; spec version-diff rows: v1→v2 breaking changes) with the
  **correlation keys** (request-id / idempotency-key / trace-id) that make a finding actionable.
- **Traffic** — live tail of redacted calls (filterable).
- **Contracts** — the loaded provider specs.
- **Flag this** — prompts for a provider engineer email and `POST /api/flag`, then surfaces the
  returned peek link; a channel picker + **Copy link** / **Revoke & regenerate** relay through
  `POST /api/peek-link` (+ `/revoke`).

## Data source

Fetches the collector's localhost read API: `/api/health`, `/api/edges`, `/api/calls`,
`/api/findings`, `/api/contracts`, `/api/contracts/spec`; writes via `POST /api/flag` and
`POST /api/peek-link(/revoke)`. Findings link to their source call by `source_call_id` to render
correlation keys.

## Develop

```bash
npm install
npm run dev        # Vite on :5336, proxying /api -> a running collector (:5335)
npm run build      # -> dist/  (embedded by the extension)
```

## Build → embed pipeline

The Dockerfile's node stage runs `npm run build` and copies `dist/` over
`../extension/viniferaui/web/dist`, which the Go build embeds. In the repo only
the placeholder `web/dist/index.html` is tracked; built assets are gitignored.
