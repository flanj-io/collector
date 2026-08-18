# CLAUDE.md — processor/viniferaredaction

Defense-in-depth redaction **processor** (OTel logs processor). Own Go module,
wired into the distribution by `builder-config.yaml`.

## Role

Second line of defense. The SDK already redacts at source; this processor
re-applies the Vinifera floor to every `call` record's free-text attributes
**before** they reach the store exporter, catching anything the SDK missed.

`otlp receiver → [viniferaredaction] → viniferadrift → viniferastore exporter`

## Files

- `factory.go` — `NewFactory()`, type `viniferaredaction`, logs processor via
  `processorhelper.NewLogs` (MutatesData: true).
- `config.go` — one optional knob, `enable_ip` (off by default).
- `processor.go` — re-scans the attributes in `otlpattr.BodyAttrs()`; on any new
  hit, sets `vinifera.redaction.applied=true` and unions fired ids into
  `vinifera.redaction.patterns` (add-only).

## Invariants (do not regress)

- **Idempotent + add-only.** The actual redaction logic lives in
  `internal/redact` and is governed by `contracts/redaction-vectors.json`. This
  processor must never double-wrap an existing `⟦REDACTED:…⟧` token — that
  property comes from `internal/redact`, so route all matching through it.
- **Only `call` records** carry free text; `finding` records pass through
  untouched.

## Tests

The redaction oracle is `internal/redact` (`go test ./internal/redact`). This
wrapper is exercised by the ocb build + the runtime smoke (POST the golden call,
confirm the stored body is redacted).
