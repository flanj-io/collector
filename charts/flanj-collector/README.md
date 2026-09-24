<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/flanj-io/collector/main/docs/brand/flanj-lockup-dark.svg">
  <img alt="Flanj" src="https://raw.githubusercontent.com/flanj-io/collector/main/docs/brand/flanj-lockup.svg" width="166" height="48">
</picture>

# flanj-collector — Helm chart

Your integrations break when the other side changes. Flanj catches it, with proof both teams can act on.

[![Helm chart: oci://registry-1.docker.io/flanj/flanj-collector](https://img.shields.io/badge/helm%20chart-oci%3A%2F%2Fregistry--1.docker.io%2Fflanj%2Fflanj--collector-1f2933)](https://github.com/flanj-io/collector/tree/main/charts/flanj-collector)
[![Docker Hub: flanj/collector](https://img.shields.io/docker/v/flanj/collector?sort=semver&label=docker%20hub&color=1f2933)](https://hub.docker.com/r/flanj/collector)
[![License: Elastic License 2.0](https://img.shields.io/badge/license-Elastic%202.0-1f2933)](https://github.com/flanj-io/collector/blob/main/LICENSE)
[![CI](https://github.com/flanj-io/collector/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/flanj-io/collector/actions/workflows/ci.yml)

The Helm chart for the Flanj collector's tiered shape: N stateless front collectors and one store pod, the
same image in two roles. Every call it handles was redacted at source and again on arrival; the window it
keeps stays on your cluster, and raw calls never leave.

One `helm install` gives you the tiered shape:

```bash
helm install flanj oci://registry-1.docker.io/flanj/flanj-collector \
  --namespace flanj --create-namespace \
  --set specToken.value="$(openssl rand -hex 32)"
```

Then point your application at the fronts. The variable goes on **your** workload's container — it is the
application that sends:

```yaml
env:
  - name: FLANJ_OTLP_ENDPOINT
    value: http://flanj-collector.flanj:4318/v1/logs
  - name: NODE_OPTIONS
    value: "--require @flanj/sdk/register"
```

The second variable is the SDK preload: without it the app sends nothing, because nothing loads the SDK.
A Python app needs no `NODE_OPTIONS` — `import flanj.register` as its first line is the preload.

That address is the same on every cluster that ran the command above: the chart gives the front collectors
a Service called `flanj-collector` whose name does not move with the release name (see
[The fixed front address](#the-fixed-front-address)). The UI binds the store pod's loopback and is never a
Service, so reach it with a port-forward:

```bash
kubectl -n flanj port-forward sts/flanj-flanj-collector-store 5335:5335
```

Check that `5335` is free first — the Docker quickstart publishes it, and another collector may hold it:

```bash
lsof -nP -iTCP:5335 -sTCP:LISTEN
```

No output means it is free (macOS and Linux). If something is listening, `kubectl port-forward` does not fail:
it binds `[::1]` only and prints an ordinary "Forwarding from [::1]:5335" line, and the address you open then
answers from whatever holds the IPv4 port. Forward a different local port instead — `15335:5335` — and open
<http://localhost:15335>. Both `localhost` and `127.0.0.1` reach the UI only when the port-forward bound both.

The operator's reference for the shapes, the flows and what each object is for
is [`docs/DEPLOYMENT.md`](../../docs/DEPLOYMENT.md); the store's backends,
window and migration story are in [`docs/STORE.md`](../../docs/STORE.md). This
page covers the chart only.

## Scope: the tiered shape, and only that

`docs/DEPLOYMENT.md` documents three shapes. This chart installs **one** of
them, because it is the only one with more than one object to get right:

| Shape | Installed by |
|---|---|
| Single pod | `docker compose` / `docker run`, or the raw StatefulSet in DEPLOYMENT.md — one object |
| N pods + shared postgres | the raw Deployment in DEPLOYMENT.md — one object |
| **Tiered: N fronts → 1 store pod** | **this chart** |

## The three tiers

All three are this same topology; the store's state is what changes.

| Tier | Values | Store |
|---|---|---|
| eval | `store.persistence.enabled=false` | sqlite on an emptyDir — dies with the pod |
| small prod | *(defaults)* | sqlite on a PVC, StatefulSet, one pod |
| scale | `store.backend=postgres` `store.persistence.enabled=false` | postgres, Deployment, `store.replicas` may exceed 1 |

Worked values for each are in [`ci/`](ci/), and CI installs two of them into a
kind cluster on every pull request.

## `specToken` is required, on purpose

The tiered shape does not boot without a shared contract token, and this is the
one thing to understand before installing:

- the **store pod** serves the contracts uploaded in its UI to the fronts over
  an intra-cluster port, and **refuses to start** rather than bind that port
  with no auth;
- a **front** with a wrong or empty token **starts fine**, reports Ready, gets
  `401` on every contract read, and detects no REST drift at all.

So the chart refuses to render without one — in `values.schema.json`, which
runs before anything is created, and again at render time with a longer
explanation. Give it a value and both roles get the identical one from the same
Secret key, which is the only way this is structurally safe rather than a thing
you have to type correctly twice:

```bash
--set specToken.value="$(openssl rand -hex 32)"
# or, if you manage the Secret:
--set specToken.existingSecret=flanj-spec-token --set specToken.key=spec-token
```

`FLANJ_SPEC_TOKEN` is never logged, on either side.

## What the chart refuses

Each of these is an install that would come up looking healthy and be wrong:

| Values | Why |
|---|---|
| no `specToken` | the store pod exits at startup; a front detects nothing and says so only in its log |
| `store.replicas > 1` with `backend=sqlite` | exactly one pod may own a sqlite file — two is a corrupted store, not a degraded one |
| `store.persistence.enabled` with `backend=postgres` | the evidence is in the database; the PVC would be an empty volume that looks like durability |
| `backend=postgres` with no DSN and no bundled database | nothing to write to |
| `postgres.enabled` with `backend=sqlite` | a database nothing writes to |
| `postgres.enabled` with no `postgres.password` | not generated on purpose — a password regenerated by the next `helm upgrade` locks the store out of its own database |
| `autoscaling.enabled` with both targets `0` | an HPA with no metric never scales |
| `service.front.fixedName` equal to another Service the chart renders | two objects with one name in one manifest — an install that half-applies |

## Values

Full list with comments: [`values.yaml`](values.yaml). The ones that matter:

| Key | Default | |
|---|---|---|
| `image.repository` / `image.tag` | `flanj/collector` / chart `appVersion` | |
| `specToken.value` / `.existingSecret` | — | **required** (see above) |
| `controlPlane.baseUrl` | `https://app.flanj.io` | the hosted control plane; an in-cluster address reaches one through your own network. Nothing is sent until Connect, whatever it names. `""` = fully local: captures and detects, cannot create thread links |
| `controlPlane.publicUrl` | `https://app.flanj.io` | the origin **your browser** can open; change it with `baseUrl` whenever that becomes an in-cluster name |
| `controlPlane.deployToken.value` | `""` | optional (2026-09-14): an operator's or per-account deploy token, used once at Connect when set; Connect needs none |
| `collector.replicas` | `2` | the fronts — the tier you scale |
| `collector.autoscaling.*` | off | HPA on cpu/memory; fronts are stateless |
| `store.backend` | `sqlite` | `sqlite` \| `postgres` |
| `store.persistence.*` | on, 10Gi | sqlite only |
| `store.dsn.value` / `.existingSecret` | — | postgres |
| `store.window.maxRows` / `.maxBytes` | 10000 / 256 MiB | the rolling window |
| `postgres.enabled` | `false` | bundled **evaluation** database |
| `service.front.fixedName` | `flanj-collector` | an additional front Service at a name that does not move with the release; `""` disables it |
| `endpointConfigMap.enabled` | `true` | `ConfigMap/flanj-endpoint` — `FLANJ_OTLP_ENDPOINT`, for `envFrom` |
| `endpointConfigMap.namespaces` | `[]` | extra namespaces to render it into; the release namespace is always included |
| `networkPolicy.enabled` | `false` | restricts the store's two ports to this release's fronts |
| `collector.config` / `store.config` | `""` | a complete config that replaces what the chart renders |

### Config

The chart renders both role configs from your values and mounts them at
`/etc/flanj/chart/{front,store}.yaml`. It does not use the image's baked
`/etc/flanj/front.yaml` / `store.yaml` — those carry the example store Service
name, where the rendered ones carry the Services this release actually created. The
baked files stay in the image, readable, as the annotated originals.

For anything the values do not cover, `collector.config` / `store.config` take
a complete config as a string and replace the rendered one entirely. You then
own every key — including `store_pod_endpoint` and `store_pod_token`, which is
where the silent 401 lives. `FLANJ_SPEC_TOKEN`, `CP_DEPLOY_TOKEN` and
`FLANJ_PG_DSN` are still in the pods' environment, so `${env:...}` keeps
working.

Every key the chart renders is frozen in
[`contracts/CONTRACTS.md`](../../contracts/CONTRACTS.md) §8, which is why
`appVersion` matters: a chart ahead of the image can render a config the image
refuses to load.

## The fixed front address

The chart renders **two** Services for the front collectors, on the same pods and the same port:

| Service | Name |
|---|---|
| release-scoped | `<release>-flanj-collector-front` — e.g. `flanj-flanj-collector-front` |
| fixed | `flanj-collector` (`service.front.fixedName`) |

The second exists so there is one address that is right for everyone who ran the documented install, which
documentation can print literally instead of guessing what you called the release:

```
http://flanj-collector.<release namespace>:4318/v1/logs
```

The release-scoped Service stays and keeps working, so an existing deployment upgrades with nothing to
change.

**Two releases in one namespace collide on the fixed name.** That is the intended failure: Helm refuses the
second install because the object already belongs to the first release, before anything is created — rather
than one release quietly taking over an address the other's applications are using. Set
`service.front.fixedName: ""` on the second release (its applications then use the release-scoped name), or
give it its own namespace. The name cannot collide with anything else the chart makes for any release name
— every other Service it renders ends in `-front`, `-store`, `-store-headless` or `-postgres` — and a
`fixedName` aimed at one of those is refused at render time.

### `ConfigMap/flanj-endpoint`

Rendered alongside it: one key, `FLANJ_OTLP_ENDPOINT`, the same address, for workloads that would rather
reference a ConfigMap than write the literal string. **A pod can only reference a ConfigMap in its own
namespace**, and this chart installs into the collector's — so pointing a workload in another namespace at
it is one ordered route:

1. Make sure the namespace exists first — `--create-namespace` only creates the release's. Your
   application's namespace usually exists already; this creates it only if it does not:

   ```bash
   kubectl create namespace shop --dry-run=client -o yaml | kubectl apply -f -
   ```

2. Add it to the map's namespaces (the release namespace is always included; the installing credential
   must be allowed to write into each one listed):

   ```bash
   helm upgrade flanj oci://registry-1.docker.io/flanj/flanj-collector \
     --namespace flanj --reuse-values \
     --set 'endpointConfigMap.namespaces={shop}'
   ```

3. Reference it from the workload deployed in that namespace, keeping the SDK preload in `env:` beside it:

   ```yaml
   envFrom:
     - configMapRef:
         name: flanj-endpoint
   env:
     - name: NODE_OPTIONS
       value: "--require @flanj/sdk/register"
   ```

   `envFrom` replaces only `FLANJ_OTLP_ENDPOINT`. Node still needs the preload — swapping the whole `env:`
   block for `envFrom:` drops it, and the pod then captures nothing and warns of nothing. Python does not.

4. Restart the workload's pods to pick it up — `envFrom` is read only at pod start:

   ```bash
   kubectl -n shop rollout restart deployment/<your-workload>
   ```

The value follows `service.front.fixedName`, falling back to the release-scoped Service when that is
empty, so the map can never name an address the release does not serve. `endpointConfigMap.enabled: false`
turns it off — which is also the answer for a second release in one namespace, since the name collides
exactly as the Service does.

The fixed Service needs no NetworkPolicy of its own: `networkPolicy.enabled` restricts the *store* pod's
two cluster ports, and this is a second name for the front pods, which that policy does not cover either
way.

## The UI is not a Service

It binds `127.0.0.1:5335` inside the store pod. The collector is outbound-only;
nothing it serves is reachable off-host, and that is a non-negotiable, not a
default to relax. Reach it with `kubectl port-forward` to the store pod.

The one link the UI offers out — the Connected pill's dashboard door — is
minted from `controlPlane.publicUrl`, not `baseUrl`. In-cluster names are
recognised as non-public (including the short `service.namespace` form this
chart renders), so with `publicUrl` unset the pill stays a Settings button
rather than becoming a dead link.

## After installing, read a front's log

The roles fail differently on purpose, so `kubectl get pods` is not the check. `deploy/<name>` alone
follows a single pod of the Deployment, and a `401` on the *other* front is missed — add `--all-pods`
(or select on the label instead of naming the Deployment):

```bash
kubectl -n flanj logs deploy/flanj-flanj-collector-front --all-pods | grep -i 'contract refresh'
# or: kubectl -n flanj logs -l app.kubernetes.io/component=front --all-pods | grep -i 'contract refresh'
```

Two refresh failures are expected within the first minute, in either order: the store Service is not yet
resolvable (`contract refresh failed … dial tcp: lookup flanj-flanj-collector-store: no such host`) or not yet
Ready (`contract refresh failed … store pod unreachable … context deadline exceeded`). They arrive roughly
15–35 seconds after the pod starts, before each front's next refresh succeeds. There is no success line:
silence after them is the healthy result. A `401` is never expected:
`contract refresh failed … 401 Unauthorized`
means the two roles hold different tokens — which this chart prevents unless
you supplied an `existingSecret` whose value changed underneath it.

## Two restarts on a first postgres install are normal

The store pod fails closed when it cannot open its database, so on
`postgres.enabled=true` it will usually restart once or twice while the
database finishes its first `initdb` — the same as any pod whose dependency is
still coming up, and the same with a BYO DSN pointing at a database that is
briefly away. There is deliberately no wait-for-the-database init container:
the restart IS the wait, and it keeps working for the outage six months from
now, which an init container does not. What matters is whether it settles, and
`helm install --wait` blocks until it does.

## Upgrades

**Store pod first, then the fronts** (DEPLOYMENT.md "Upgrades"). A single
`helm upgrade` rolls both; on a version boundary where that ordering is
load-bearing, upgrade the store alone first:

```bash
helm upgrade flanj <chart> --namespace flanj --reuse-values --set collector.replicas=0 ...   # store only
helm upgrade flanj <chart> --namespace flanj --reuse-values --set collector.replicas=3 ...   # then the fronts
```

Every upgrade needs `--reuse-values` (or the same `--set specToken.value=…`
as the install): without it helm re-renders from the chart's defaults, the
token you generated at install is dropped, and the upgrade fails with
`specToken: Must validate "then" as "if" was valid` — that error means
specToken was dropped, not that the token is wrong. An upgrade that replaces
the store pod also ends an open `kubectl port-forward` to it; run the
port-forward again afterwards.

## Publishing the chart

```bash
helm package charts/flanj-collector
helm push flanj-collector-<version>.tgz oci://registry-1.docker.io/flanj
```

Chart `version` is the chart's own SemVer; `appVersion` is the collector image
tag it is written against. Bump `appVersion` with the image.
