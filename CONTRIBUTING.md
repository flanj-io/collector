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

## Releasing (maintainers)

The image is published by `.github/workflows/release.yml` on a `v*` tag — never by hand. It builds
`linux/amd64,linux/arm64` with the `VERSION` build-arg, stamps `org.opencontainers.image.revision` with
the commit, refuses to push an image whose `/api/health` reports a version other than the tag, and
verifies the published index with an **anonymous** pull.

```bash
git fetch origin
git tag v0.1.1 origin/main
git push origin v0.1.1
gh run list --workflow release.yml --limit 1    # confirm a run actually started
```

Three things that have bitten before:

- **Tag a freshly fetched `origin/main`.** The workflow is read from the tag's own tree, so a tag on an
  older commit runs that commit's workflows — or, if the file did not exist there, nothing at all, with
  no error anywhere. The `gh run list` above is the only thing that tells you.
- **Secrets live outside the repo.** `DOCKERHUB_TOKEN` (Read & Write on the `flanj` account) and the
  optional `DOCKERHUB_USERNAME`. Missing token fails at the login step, before anything is built.
- **The image goes first, the chart second.** `chart-release.yml` refuses to publish a chart whose
  default image tag is not already in the registry, so tag `v*` and let it finish before `chart-v*`.

`workflow_dispatch` runs the whole thing except the push and the anonymous check, which is how to
rehearse a release — including the credential, which is checked before the build either way.
