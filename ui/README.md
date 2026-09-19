<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/flanj-io/collector/main/docs/brand/flanj-lockup-dark.svg">
  <img alt="Flanj" src="https://raw.githubusercontent.com/flanj-io/collector/main/docs/brand/flanj-lockup.svg" width="166" height="48">
</picture>

# ui/ — Flanj collector local UI

Your integrations break when the other side changes. Flanj catches it, with proof both teams can act on.

[![License: Elastic License 2.0](https://img.shields.io/badge/license-Elastic%202.0-1f2933)](https://github.com/flanj-io/collector/blob/main/LICENSE)
[![CI](https://github.com/flanj-io/collector/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/flanj-io/collector/actions/workflows/ci.yml)

The Flanj collector's local UI: a Vue 3 + Vite single-page app, served embedded by the `flanjui`
extension (`go:embed`) on the collector's loopback listener. It shows redacted calls only, and nothing it
renders is reachable off-host.

Built to static assets; no runtime server of its own.

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
  BREAKING / NON-BREAKING / DESCRIPTION badge and both snapshot columns. A row whose stored
  document is past the 8 MB contract-channel cap carries a `too large to serve` chip and a
  line naming the overage — rendered only when `/api/health.serves_fronts` says this pod is
  the store pod of a tiered deployment, since a single pod reads the same document in-process
  with no cap and validates against it fine (`contractOverCap` in `src/contracts.ts`).
- **Flag this** (on a Contracts finding) — the Flag sheet: evidence line, "what leaves this
  collector", an editable optional message, **Create thread** → the success state shows the
  **thread link** (auto-selected, auto-copied where the clipboard allows) with **Copy thread link**,
  **Copy link + message** (fixed template, request ID first) and **Open thread** (owner handoff in
  a new tab). Not Connected yet? The sheet shows the Connect prompt inline and unlocks the moment
  the contact confirms. A flagged finding keeps a chip: `In thread · <turn> · opened ×N`.
  MCP findings swap in the MCP evidence / disclosure / prefill copy; the IDs line uses the
  JSON-RPC wording only while the client-generated id is the **sole** correlation key (mixed keys
  → the standard count line + an honest client-id note). Flaggable definition changes keep a
  disabled Flag control (call-less — a known v0.5 limit); local notices never show one.
- **Threads** — state of every thread this collector created (provider · endpoint · evidence ·
  status · opened · last reply · link) with Open / Close thread / Reopen / Copy thread link /
  Replace link; `#threads/<thread_id>` deep-links and highlights a row.
- **Settings** — **Connect to Flanj network**: collector name + contact email (+ your name, this
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
- `src/contracts.ts` — the contract-coverage copy and pure helpers (roll call, provenance +
  recency, binding checks, finding→card attribution, the document-cap row state) with
  `src/contracts.test.ts` + `src/contract-over-cap.test.ts`.
- `src/mcp.ts` — the v0.5 MCP pure helpers (fixed copy: badges, headlines, local notices,
  tool rows, traffic facets, flag-sheet lines; `snapshotTimes` parses the definition_change detail
  tail — its regex is pinned by a collector Go test, `internal/drift`) with `src/mcp.test.ts`.
- `src/tokens.css` — the Flanj token layer, **vendored** (Blueprint,
  locked 2026-09-09) below a do-not-edit preamble; `src/theme.ts` — the Light / Dark preference
  (storage key `flanj.theme`, stamped as `data-flanj-theme` on `<html>`); `public/favicon.svg` —
  the Flanj favicon verbatim, copied into `dist/` by Vite.

## Design tokens and theme

The surface follows the **Blueprint collector design** (a 2px
`--rule` frame around a 24px grid-paper ground; sheet header with the inline colour mark, the text
wordmark, `localhost:<port> · <version>` in mono, the org pill and a green-bolt Connected pill;
mono uppercase tabs with a 2px ink underline over a copper hairline and square count chips;
headline cards with a 6px left rule in their tone and a leading hex bolt, no "You:" prefix — the
subline carries scope; findings as framed cards with `expected ≠ actual ≠ location` in mono cells;
2px-outline buttons that lift under the hard offset shadow, primary = ink fill; a Light / Dark
segmented control). The design's classes are bound straight to the canonical tokens — its alias layer
(`--bg`, `--panel`, `--cu` …) and font-name literals are not vendored. The hex bolt ships once, as
the inline `<symbol id="hxbolt">` at the top of `App.vue`'s template; every bolt is
`<svg class="hx [sm] tone-ok|tone-breaking|tone-warning|tone-accent|tone-info"><use href="#hxbolt"/>`
and no bolt carries an inline style (a bolt is a mark, so it takes the bare family colour). The
states the design does not draw — the uploader, the question sheet, the validated stamp, the MCP
headline and tool rows, the edge-registration and connect-disclosure panels, the theme-flip
notice, the load-error and relay banners, the flag sheet's three branches, the Threads rows — are
extrapolated in the same idiom. The MCP headline keeps the whole `Server: … You: …` sentence
(`src/mcp.ts`): the integration tests pin its lowercase clause, so it is the one line that keeps its pivot. A
description-only definition change names itself in that clause but never takes the drift tone —
the line rides the verdict its validated calls earned (`ok`, or `neutral` with none) — and the
DESCRIPTION class wears one vocabulary end to end: the row's steel badge, a steel `N DESCRIPTION`
card chip, and the tab pill in the steel outline (`.tab-count.warn.desc`) when every un-acked
informational row is a wording change; the copper fill is kept for NON-BREAKING schema classes.

Dimming is a palette move, never opacity: an acknowledged finding and a closed thread row drop
their text to `--ink-soft`, their frame to `--rule-soft` and their chips to the steel outline (each
pair stays ≥ 4.5:1 in both schemes; `src/tokens.test.ts` refuses `opacity` on any dimmed-state
selector). Timestamps use one format across the surface, `YYYY-MM-DD HH:MM:SS` in local time
(`src/time.ts`) — the Traffic captured column, a finding's snapshot labels and its detail line — with
the full RFC 3339 instant on the captured cell's title. A traffic row is a control (`role="button"`,
`tabindex="0"`, `aria-expanded`, Enter / Space toggle the detail, the token focus ring). At phone
width the Traffic table renders each call as a stacked card (route whole on its own line; direction,
host, time and status on the second; correlation and contract on the third), the toolbar stops being
sticky, and the Threads rows label every fact inline. The Edges tables carry the design's evidence —
calls in the window (with the observed rate as a muted suffix only when non-zero), drifted calls in
red mono when any, last seen — stacked as two group rules rather than two half-width boxes.

Every colour, rule width, radius and focus ring comes from `src/tokens.css`; the SFC style blocks
hold layout and component rules only. Text takes the `-ink` role of its family, and text at or
below 14px never uses `--ink-faint` (the muted text role is `--ink-soft`). Motion is the lift on
hover and colour fades; `prefers-reduced-motion` keeps only the fades. The rules
`src/tokens.test.ts` enforces:

- the body below the preamble is the canonical file byte for byte (a pinned sha256, plus a direct
  comparison only when `FLANJ_TOKENS_SOURCE` points at a copy of the canonical file) — change the
  canonical copy, then re-vendor and update the digest;
- no SFC or ui CSS declares a custom property whose name the canonical file also defines, and no
  hex or rgb literal lives in an SFC;
- the theme attribute is `data-flanj-theme` (never `data-theme`): `index.html` stamps
  `data-flanj-theme="light"` in the served markup so the first paint is light for everyone who never
  chose, and an inline `<script>` in its `<head>` — ahead of the module bundle, which runs only after
  the first frame — re-stamps a stored `flanj.theme = dark` before anything paints, so a dark-theme
  user never sees a light flash; `src/theme-wired.test.ts` executes that script against a fake
  document and storage, then mounts the app, clicks the Appearance control and reads the attribute
  off `<html>`;
- green (`--ok*`) is spent on reached verdicts only (`conforming`, `No drift detected`, Connected);
  the warning tier (`--sev-warning*`) is the same copper as `--accent` by design, so it renders only
  finding-tier badges, tags and counts that carry a label or an outline, and every other attention
  state uses `--accent*`;
- every control draws `var(--focus-ring)` at `var(--focus-offset)` on `:focus-visible`;
- exactly one `<symbol id="hxbolt">` across the SFC templates, and no template carries an inline
  `style=` attribute.

No webfont is loaded — the page makes no outbound request — so the Space Grotesk stack renders as
`system-ui` and the mono stack as the platform monospace face. The mark in the topbar is
`docs/brand/flanj-mark.svg` — the colour mark — inlined: the pipes and the outline bind
`--logo-steel` / `--logo-outline` by class so they switch with the scheme, and the F/J threads
reference one `#brandcu` copper gradient in the shared defs holder beside `#hxbolt`, its three
stops the asset's own hex as `stop-color` attributes (no token for the copper yet). The wordmark
is text. `brand-mark.test.ts` guards that structure.

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
