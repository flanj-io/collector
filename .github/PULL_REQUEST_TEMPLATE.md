<!-- Thanks for contributing to the Flanj Collector! Please fill this out so we can review quickly. -->

## What & why

<!-- What does this change do, and why? Link any related issue: Closes #123 -->

## How was it tested?

<!-- `go test ./...`, `docker build`, manual runtime checks. -->

## Checklist

- [ ] Commits are **signed off** for the DCO (`git commit -s`) — see `CONTRIBUTING.md`.
- [ ] `go test ./...` passes and the image builds (`docker build .`).
- [ ] The **ocb version triad** in `builder-config.yaml` stays consistent (ocb == beta == the stable pin).
- [ ] If this touches **redaction**, it stays idempotent + add-only and conforms to the redaction vectors;
      no raw body reaches the store or the wire before redaction.
- [ ] Detection changes remain **technical-only** (types/shapes/enums), never business correctness.
- [ ] The collector stays **outbound-only** (no new inbound surface; UI stays loopback).
- [ ] Docs / `CLAUDE.md` updated if behavior or structure changed.
