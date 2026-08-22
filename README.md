# Vinifera Collector

A single-binary [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) distribution for the
**local, self-hosted** side of Vinifera. It receives captured calls from the [SDK](https://github.com/vinifera-io/sdk),
re-applies redaction (defense-in-depth), validates live traffic against a provider's OpenAPI spec to
detect **drift**, stores redacted calls in an embedded rolling-window store, and serves a **local UI** on
`localhost` — all inside your own environment. **Raw calls never leave.**

Headless and **outbound-only** apart from the localhost UI. Ships and deploys as one unit (collector + store +
UI embedded in the binary).

- OTLP receiver → redaction processor → drift detection → local store (rolling window; embedded SQLite on a PVC by default, or a shared Postgres database for multi-pod deployments — see docs/STORE.md).
- Local Vue UI: **Overview** (health + drift findings with the correlation keys that make a finding actionable),
  **Traffic** (live tail of redacted calls), **Contracts** (the loaded specs), plus a **flag** action that promotes a
  redacted call to the control plane and copy-link / revoke controls for the resulting peek link.

Open-source SDK (Apache-2.0) and source-available collector (ELv2); hosted network layer.

## Status

Pre-release (v0). See [docs/CONCEPTS.md](docs/CONCEPTS.md) and [CLAUDE.md](CLAUDE.md).

## License

[Elastic License 2.0](LICENSE) (source-available). Contributions require a DCO sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md).
