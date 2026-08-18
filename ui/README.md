# ui/ — Vinifera Collector local UI

Vue 3 + Vite single-page app, served **embedded** by the `viniferaui` extension
(`go:embed`). Built to static assets; no runtime server of its own.

## What it shows (the v0 surface)

- **Health divergence headline** — "Provider: operational. You: N contract drift
  finding(s) on `/endpoint`".
- **Contract drift** rows — expected-per-spec vs actual (live), the violated
  `location`, and the **correlation keys** (request-id / idempotency-key /
  trace-id) that make a finding actionable.
- **Spec version diff** rows — v1→v2 breaking changes.
- **Flag this** — prompts for a provider engineer email and `POST /api/flag`,
  then surfaces the returned peek link.

## Data source

Fetches the collector's localhost read API: `/api/health`, `/api/findings`,
`/api/calls`. Findings link to their source call by `source_call_id` to render
correlation keys.

## Develop

```bash
yarn install       # or npm install
yarn dev           # Vite on :5336, proxying /api -> a running collector (:5335)
yarn build         # -> dist/  (embedded by the extension)
```

## Build → embed pipeline

The Dockerfile's node stage runs `npm run build` and copies `dist/` over
`../extension/viniferaui/web/dist`, which the Go build embeds. In the repo only
the placeholder `web/dist/index.html` is tracked; built assets are gitignored.
