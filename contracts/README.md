# `contracts/` — vendored cross-repo contract (do not hand-edit)

Everything in this directory except `golden-otlp-server-call.json` and the three
`contract-*` Step A fixtures (see the table) is a **byte-identical copy** of the
canonical contract that lives in the private `e2e` repo at `e2e/contracts/` (`CONTRACTS.md` + `v1/*`).
It is the agreed language between the SDK, this collector and the control plane — the OTLP wire
convention the collector ingests, the `RedactedCall`/`Finding` shapes it stores and emits, the redaction
floor it must enforce bit-for-bit with the TypeScript implementation, the control-plane endpoints it
calls (Connect, flag, thread state), and its frozen runtime config keys. The collector's own tests assert against these files so the
repo proves conformance standalone; the `e2e` integration gate is the cross-repo safety net.

## What is vendored here

| File | Role | Consumed by |
|---|---|---|
| `CONTRACTS.md` | the human-readable contract — the **public half** (§2 OTLP ingest, §3/§4 `RedactedCall`/`Finding`, §5 the CP endpoints this collector calls — register/me, flags, thread routes, §6 redaction floor, §7 versioning, §8 frozen config keys). The control plane's own contract is private and is not vendored here. | — |
| `golden-otlp-call.json` | SDK → collector wire fixture: one drifting egress (client-direction) call; ingesting it must yield the expected Finding | `internal/drift/drift_test.go`, `internal/otlpattr/otlpattr_test.go`, CI |
| `golden-otlp-server-call.json` | **collector-local** fixture (ingress / server-direction call) — *not* part of the canonical set | `internal/otlpattr/otlpattr_test.go` |
| `golden-otlp-mcp-call.json` | v0.5 SDK → collector wire fixture: one MCP `tools/call` whose `structuredContent` returns `refund.amount` as a string where the tool's `outputSchema` declares integer — Step C's `output_mismatch` evidence | `internal/otlpattr/otlpattr_test.go`, `internal/drift/mcp_test.go` |
| `golden-otlp-mcp-snapshot.json` | v0.5 wire fixture: one complete observed `tools/list` (`contract_snapshot`) — the self-delivering local spec Step C loads (`outputSchema` absent on `list_transactions`, exercising the honest no-output-contract limit) | `internal/otlpattr/otlpattr_test.go`, `internal/drift/mcp_test.go` |
| `spec-v1.yaml`, `spec-v2.yaml` | the mock provider's OpenAPI specs (live-vs-spec validation; v1→v2 version-diff) | `internal/drift/drift_test.go`; baked into the image at `/etc/flanj/` by the `Dockerfile` |
| `redaction-vectors.json` | scalar/recognizer-level redaction floor vectors | `internal/redact/*_test.go` |
| `redaction-fixtures.json` | the structured cross-language PARITY battery (the TypeScript SDK and control-plane DLP run the same file) | `internal/redact/fixtures_test.go` |
| `redacted-call.schema.json`, `finding.schema.json`, `cp-flag-request.schema.json` | JSON Schemas for `RedactedCall`, `Finding`, and the flag request body | `internal/promote/promote_test.go` (compiles all three); `internal/model` mirrors the first two |
| `sample-redacted-call.json`, `sample-finding.json` | sample payloads used to build a conforming flag | `internal/promote/promote_test.go` |
| `contract-normalization-openapi.yaml`, `contract-normalization-tools-list.json` | **collector-owned** (v0.5 Step A, like `golden-otlp-server-call.json`) — the SAME logical contract expressed as OpenAPI and as an MCP `tools/list`; normalizing both must yield deep-equal Operations | `contract/normalize_test.go` |
| `contract-diff-cases.json` | **collector-owned** (v0.5 Step A) — the definition-diff classifier battery (≥2 cases per class BREAKING / NON_BREAKING / DESCRIPTION, incl. a rename) | `contract/diff/diff_test.go` |

## Layout note

The canonical tree is `contracts/CONTRACTS.md` + `contracts/v1/<fixtures>`; vendored copies are kept
**flat** (same as `sdk/contracts/`). So the `./v1/…` links inside `CONTRACTS.md` resolve to *this*
directory, and its `./README.md` link refers to the canonical governance doc in `e2e/contracts/README.md`,
not this file.

## Changing anything

Edit the canonical file in `e2e/contracts/`, bump `schema_version` if the change is breaking, re-vendor
byte-identically to `sdk/`, `collector/`, `control-plane/`, and make every repo's suite green. See
`e2e/contracts/README.md` (governance). `golden-otlp-server-call.json` and the three `contract-*`
Step A fixtures are the exceptions: they are owned here; promote them to the canonical set if another
repo ever needs them (the `contract-*` files are candidates once `mcp-drift-watch` or `e2e` consumes
them — flagged as an open item in the v0.5 Step A handoff).
