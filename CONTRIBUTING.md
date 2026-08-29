# Contributing to the Flanj Collector

Thanks for your interest in contributing. This repository is licensed under the **Elastic License 2.0
(ELv2)** — source-available; self-host/modify/internal-use unrestricted, managed-hosting-to-third-parties
blocked. See [LICENSE](./LICENSE).

## Developer Certificate of Origin (DCO)

All contributions must be signed off under the [Developer Certificate of Origin](https://developercertificate.org/).
Sign off every commit with a `Signed-off-by` trailer (real name + email):

```
Signed-off-by: Jane Doe <jane@example.com>
```

Use `git commit -s`. CI enforces the DCO check; unsigned commits will not be merged.

## Ground rules

- The collector is **headless and outbound-only** except its localhost UI. Never add an inbound network
  surface reachable off-host.
- **Defense-in-depth redaction** must stay idempotent and add-only (see `contracts/redaction-vectors.json`);
  it may only add redaction above what the SDK already applied, never remove it.
- Honor the ocb version triad in `builder-config.yaml` (ocb == beta components == the stable-component pin).
  Version skew is the most common build failure.
- Detection validates **technical** contract adherence only (types/shapes/enums), never business correctness.

## Workflow

1. Branch, write tests first, implement, build in Docker (`docker build .`).
2. `git commit -s`, open a PR. CI runs the multi-stage build + Go unit + contract tests.
