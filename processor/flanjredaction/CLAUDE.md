# CLAUDE.md — processor/flanjredaction

Defense-in-depth redaction **processor** (OTel logs processor). Own Go module,
wired into the distribution by `builder-config.yaml`.

## Role

Second line of defense. The SDK already redacts at source; this processor
re-applies the Flanj floor to every `call` record's free-text attributes
**before** they reach the store exporter, catching anything the SDK missed.

`otlp receiver → [flanjredaction] → flanjdrift → flanjstore exporter`

## Files

- `factory.go` — `NewFactory()`, type `flanjredaction`, logs processor via
  `processorhelper.NewLogs` (MutatesData: true).
- `config.go` — one optional knob, `enable_ip` (off by default).
- `processor.go` — re-scans the attributes in `otlpattr.BodyAttrs()`; on any new
  hit, sets `flanj.redaction.applied=true` and unions fired ids into
  `flanj.redaction.patterns` (add-only).

## Invariants (do not regress)

- **Idempotent + add-only.** The actual redaction logic lives in
  `internal/redact` and is governed by `contracts/redaction-vectors.json`. This
  processor must never double-wrap an existing `⟦REDACTED:…⟧` token — that
  property comes from `internal/redact`, so route all matching through it.
- **Only `call` and `contract_snapshot` records** carry free text; `finding` /
  `spec_info` records pass through untouched. The v0.5 `contract_snapshot`
  pass re-scans `flanj.mcp.contract_snapshot` (the observed tools/list is
  STORED as the edge's local spec, so the same defense-in-depth applies) —
  add-only, idempotent, no field records (a contract document, not a call
  body). Since 2026-09-18 the same pass re-floors `flanj.mcp.server.command`
  (a stdio server's launch line, stored with the contract for its card) —
  ELEMENT BY ELEMENT, as the SDK does, so it stays a JSON array; untouched
  commands pass byte-identical, and a value that is not a JSON array of strings
  is floored as plain text (the snapshot decoder then drops it).

## Tests

The redaction oracle is `internal/redact` (`go test ./internal/redact`). This
wrapper is exercised by the ocb build + the runtime smoke (POST the golden call,
confirm the stored body is redacted). `processor_test.go` covers the v0.5
snapshot pass (leaked PAN tokenised, idempotent, clean snapshot untouched).
