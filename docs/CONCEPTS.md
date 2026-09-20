# Flanj — concepts (engineering overview)

*A technical overview for contributors to the public `sdk`, `sdk-py` and `collector` repos. It contains the
engineering model only, not product strategy.*

## What Flanj does

Flanj is an integration-reliability tool. It captures the real request and response traffic between a
service and a third-party API it depends on, redacts it at source, and validates that live traffic against
the provider's published contract (an OpenAPI document, or for MCP the observed `tools/list`). When the
live traffic diverges from the contract (a field changes type, an enum gains an undocumented value, a tool
definition changes), that **drift** is surfaced with the exact evidence: the redacted call that proves it.
Raw calls never leave the environment they were captured in.

## Two planes

- **Local plane (self-hosted, this is the OSS part):** capture, redaction, storage, drift detection, and a
  local UI — all inside the user's own environment. **Raw calls never leave.**
- **Control plane (hosted, separate/closed):** collaboration — when a user *references* a call to flag it,
  a redacted copy is promoted to a durable thread that a provider engineer can open and reply to. The local
  plane only ever pushes outbound to the control plane; nothing peers inbound.

## The components in these public repos

- **`sdk`** — a thin OpenTelemetry (JS) distribution that adds HTTP **request/response body capture**, MCP
  client capture, and **redaction-at-source**. OTel auto-instrumentation gives spans/metadata but not bodies; the bodies are the
  non-redundant evidence. Redaction happens here, at the call site, **before** anything is stored or sent.
- **`sdk-py`** — the Python SDK: MCP client capture only, with the same redaction floor and the same OTLP
  record convention. HTTP body capture is the one thing it does not do.
- **`collector`** — an OpenTelemetry Collector distribution (built with `ocb`): receives the SDK's OTLP,
  applies defense-in-depth redaction, runs drift detection near the source, stores redacted calls in a
  local store (a rolling window; embedded by default, or a customer-provided Postgres so multiple
  collector pods can share one store), and serves a localhost UI. Headless and outbound-only apart
  from that UI. See `STORE.md` for backends, sizing, and migration, and `DEPLOYMENT.md` for the
  deployment shapes (single pod · N pods + shared postgres · N front collectors → one store pod).

## The agent-facing read surface

The collector serves a small **read-only MCP server** at `/mcp`, on the same loopback listener as the
UI. A coding agent running in the operator's environment can ask "what changed on the dependencies I
call?" and be answered by the collector already watching them — `drift_summary`, `list_edges`,
`list_findings`, `get_finding`, nothing else. It is the same data the UI shows, built by the same code,
so a person and an agent read one story.

Three properties make it safe to hand to a model:

- **Read-only.** No tool writes anything. Flagging a drift to a provider stays a person's act in the UI.
- **No raw body, ever.** No tool returns a body, a header map or a full URL, and the free-value fields of
  a finding pass the redaction floor once more on the way out.
- **A contract's provenance is part of its evidence.** A contract enters the collector one of two ways,
  both of them a human pressing a control: **uploaded** from a file, or **fetched** from a URL — the
  provider's own published spec. A fetched contract keeps the URL and the moment it was read, and the
  finding and the flagged thread both say so: *"checked against your published spec at `<url>`, fetched
  `<when>`"*. That is a claim the provider can check against what they serve today; "somebody here had a
  file" never was.

  Nothing binds itself. The collector can **look** for a spec at the conventional paths on a host it
  already calls, and it **offers** what it finds — a human binds it. The reason is the discipline the
  whole product rests on: **a wrong contract is worse than no contract.** No contract renders `not
  checked`, honestly, and costs nobody anything. A mismatched one renders `DRIFTED` — loudly, to a
  stranger, about their real API.

  And nothing re-fetches. A fetched document is read once, when it was approved.

- **An empty answer is not an all-clear.** Every answer carries how many calls were actually validated
  against a contract, so "no findings" cannot be read as "nothing is wrong" when the truth is "nothing
  was checked".

## Non-negotiables (why the code is shaped the way it is)

1. **Redaction at source, before store or transmit.** A redaction floor — composed, hardened validators
   (Luhn-gated card number, email, IBAN, phone) behind our own interface, with deep traversal of nested bodies and
   base64 decode-then-scan; local, zero external calls — is mandatory and runs before a body is ever attached
   to a span/log or written to disk. This is defense in depth — the collector re-applies the identical floor
   in Go (`internal/redact`), idempotently, and a shared fixture suite keeps it byte-for-byte in parity with the TypeScript and Python SDKs.
2. **Raw calls never leave the local environment.** Only a *referenced* (redacted) call is promoted to the
   control plane, and only when a human flags it.
3. **Outbound-only collector.** No inbound surface; the collector only pushes to the control plane. The
   localhost UI and the agent MCP surface share one loopback listener — the second is a route on the
   first, never a listener of its own.

   Since 2026-09-17 there is one more outbound destination, and it is not Flanj: **contract fetch and
   probe** issue `GET`s to a **provider's** host for the OpenAPI document that provider publishes. Both
   are operator-initiated — nothing schedules them and no config key enables them — and both are READS:
   no data leaves on either path. The probe only ever asks a host this deployment already sends traffic
   to, and a fetch reaches a private or internal address only on such a host; metadata, link-local and
   other reserved addresses are refused always, judged after DNS resolution. The document itself can
   cause no further request: a contract must be self-contained, and a `$ref` out of it is refused before
   anything is read. See `docs/DEPLOYMENT.md` for the exact requests, timeouts, caps and egress-policy notes.
4. **Technical adherence only.** Drift detection validates fields/types/shapes/enums — never business or
   economic correctness (prices, fees, FX), which are legitimately variable.

## The contract

Cross-component wire formats (the OTLP attribute convention, the redacted-call record, the redaction floor)
are pinned in `contracts/` (vendored from a canonical source). The redaction floor is governed by golden
fixture files (scalar vectors + the cross-language parity battery) that all implementations conform to.
Changes to any wire format go through the contract first.
