# Vinifera Collector

A single-binary [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) distribution for the
**local, self-hosted** side of Vinifera. It receives captured calls from the [SDK](https://github.com/vinifera-io/sdk),
re-applies redaction (defense-in-depth), validates live traffic against a provider's OpenAPI spec to
detect **drift**, stores redacted calls in an embedded rolling-window store, and serves a **local UI** on
`localhost` — all inside your own environment. **Raw calls never leave.**

Headless and **outbound-only** apart from the localhost UI. Ships and deploys as one unit (collector + store +
UI embedded in the binary).

- OTLP receiver → redaction processor → drift detection → local store (rolling window; embedded SQLite on a PVC by default, or a shared Postgres database — see docs/STORE.md).
- Scales as N stateless front collectors forwarding (OTLP) to one store pod — the same image in two roles, chosen by config (docs/STORE.md "Topologies"; deploy shapes + Kubernetes sketches in docs/DEPLOYMENT.md).
- Local Vue UI: **Overview** (health + drift findings with the correlation keys that make a finding actionable),
  **Traffic** (live tail of redacted calls), **Contracts** (the loaded specs, each finding with **Flag this** → one
  sheet → **Create thread** → a **thread link** you copy into the channel the two teams already share), **Threads**
  (state of every thread you created: turn label, opens, Open / Close / Reopen / Replace link) and **Settings**
  (**Connect** — org name + a one-click-confirmed contact email; required to create thread links, never to view
  your own data).

**We turn a detection into something you can act on with your vendor.**
Open-source SDK (Apache-2.0) and source-available collector (ELv2); hosted network layer.

## Status

Pre-release (v0). See [docs/CONCEPTS.md](docs/CONCEPTS.md) and [CLAUDE.md](CLAUDE.md).

**Supported:** REST/HTTP integrations — live request/response validated against the provider's OpenAPI.
**Supported:** MCP tools — tool-definition drift and result-vs-`outputSchema` mismatch, flagged to
the server operator with evidence. (The transport-neutral `Contract` model and definition-diff
classifier live in the public [`contract`](contract/) package.)
**Roadmap:** webhooks (received-webhook contract drift; missing-webhook detection under design).

## License

[Elastic License 2.0](LICENSE) (source-available). Contributions require a DCO sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md).
