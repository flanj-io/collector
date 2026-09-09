#!/usr/bin/env bash
# Kind smoke test for charts/flanj-collector — the TIERED lane, both store
# backends.
#
# `helm lint` proves the chart renders. It does not prove the thing it renders
# BOOTS, and the tiered shape's two worst failures are invisible from
# `kubectl get pods`: a front holding the wrong contract token is Ready forever
# while detecting nothing, and a store pod without one exits at startup. So
# this asserts behaviour, not YAML:
#
#   1. every pod reaches Ready
#   2. the UI answers on the store pod's loopback bind, through a port-forward
#   3. a call POSTed to the FRONT service arrives in the STORE's window
#      (the front->store hop, which is the whole point of the topology)
#   4. no front logs a contract-refresh 401 (the silent one)
#   5. installing without a spec token is REFUSED, not deployed
#   6. on the PVC tier, the window survives losing the store pod
#
# ...on all three tiers the chart claims: sqlite on an emptyDir (eval),
# postgres (scale), and sqlite on a PVC (the default). One tier at a time — a
# two-core CI node does not fit all three at once, and a full node reads as a
# timeout rather than as a failure.
#
# Runs in CI (.github/workflows/ci.yml) and locally:
#
#   docker build -t flanj-collector:smoke .
#   bash scripts/helm-smoke.sh flanj-collector:smoke
#
# Requires kind, kubectl and helm. Creates and deletes a cluster named
# flanj-helm-smoke unless KIND_CLUSTER / SKIP_CLUSTER say otherwise.
set -euo pipefail

cd "$(dirname "$0")/.."

IMAGE="${1:-${FLANJ_IMAGE:-flanj-collector:smoke}}"
# The chart under test. Defaults to the working tree; point it at the PUBLISHED
# artifact to run this same lane against what an operator actually gets:
#
#   CHART=oci://registry-1.docker.io/flanj/flanj-collector \
#   CHART_FLAGS='--version 0.1.0' \
#   bash scripts/helm-smoke.sh flanj/collector:v0.6.0
#
# With a registry image, drop `image.pullPolicy=Never` by setting PULL_POLICY.
CHART="${CHART:-charts/flanj-collector}"
# Deliberately word-split at every use (an OCI ref needs `--version X`).
CHART_FLAGS="${CHART_FLAGS:-}"
PULL_POLICY="${PULL_POLICY:-Never}"
CLUSTER="${KIND_CLUSTER:-flanj-helm-smoke}"
NS="${SMOKE_NAMESPACE:-flanj-smoke}"
SKIP_CLUSTER="${SKIP_CLUSTER:-}"
KEEP="${KEEP_CLUSTER:-}"

IMAGE_REPO="${IMAGE%:*}"
IMAGE_TAG="${IMAGE##*:}"

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { printf '\n\033[31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

# The one pattern that means "the store pod refused this front's token". Kept
# here, next to its self-test, because the previous version of it (a bare
# `401|Unauthorized`) matched a millisecond in a timestamp and reported a
# token mismatch that did not exist.
AUTH_REFUSAL_RE='store pod returned 40[13]'

# Self-test the pattern before the cluster costs five minutes: it must fire on
# the sentence a front really logs, and NOT on the timestamp that broke it.
# Three lines, no cluster, and the bug cannot come back unnoticed.
# `if` rather than `&&`/`||`: under `set -e` a bare `grep -q ... && fail` makes
# the PASSING case (grep finds nothing) the failing status of the statement.
selftest_auth_refusal_re() {
  local real='store pod returned 401 Unauthorized'
  local decoy='2026-09-08T21:04:46.401Z  warn  builders/builders.go:40  "otlphttp" alias is deprecated'
  if ! grep -qE "$AUTH_REFUSAL_RE" <<<"$real"; then
    fail "self-test: the token-refusal pattern no longer matches a front's real 401 line"
  fi
  if grep -qE "$AUTH_REFUSAL_RE" <<<"$decoy"; then
    fail "self-test: the token-refusal pattern matches a plain timestamp (.401Z) — it would fail a healthy lane"
  fi
}
selftest_auth_refusal_re

PF_PID=""
cleanup() {
  local rc=$?
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
  if [ $rc -ne 0 ]; then
    echo "--- diagnostics ---" >&2
    kubectl -n "$NS" get pods -o wide 2>&1 | sed 's/^/  /' >&2 || true
    kubectl -n "$NS" describe pods 2>&1 | grep -E 'Name:|Warning|Error|Message' | sed 's/^/  /' >&2 || true
    kubectl -n "$NS" logs -l app.kubernetes.io/component=store --tail=60 2>&1 | sed 's/^/  store: /' >&2 || true
    kubectl -n "$NS" logs -l app.kubernetes.io/component=front --tail=60 2>&1 | sed 's/^/  front: /' >&2 || true
  fi
  if [ -z "$SKIP_CLUSTER" ] && [ -z "$KEEP" ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# --- cluster ---------------------------------------------------------------
if [ -z "$SKIP_CLUSTER" ]; then
  say "kind cluster $CLUSTER"
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  kind create cluster --name "$CLUSTER" --wait 120s
  kind load docker-image "$IMAGE" --name "$CLUSTER"
  # The bundled evaluation database is pulled once, here, rather than by every
  # pod on a cluster with no registry mirror.
  PG_IMAGE="$(helm show values "$CHART" $CHART_FLAGS | awk '/^  image: postgres/ {print $2}')"
  docker pull "$PG_IMAGE" >/dev/null
  kind load docker-image "$PG_IMAGE" --name "$CLUSTER"
fi

kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# --- 5. the refusal, before anything is installed --------------------------
say "the chart refuses the tiered shape with no contract token"
# shellcheck disable=SC2086  # CHART_FLAGS must word-split
if helm template smoke "$CHART" $CHART_FLAGS >/dev/null 2>&1; then
  fail "helm template rendered with no specToken — the values-schema guard is gone, and this chart will happily ship a store pod that exits at startup"
fi
if helm template smoke "$CHART" $CHART_FLAGS --skip-schema-validation >/dev/null 2>&1; then
  fail "helm template rendered with no specToken under --skip-schema-validation — the render-time guard is gone"
fi
# ...and the two invariants that make the store a store.
helm template smoke "$CHART" $CHART_FLAGS --set specToken.value=t --set store.replicas=2 >/dev/null 2>&1 \
  && fail "chart accepted store.replicas=2 on sqlite — two pods on one db file is a corrupted store"
helm template smoke "$CHART" $CHART_FLAGS --set specToken.value=t --set store.backend=postgres \
  --set store.dsn.value=x >/dev/null 2>&1 \
  && fail "chart accepted a PVC on the postgres tier"
echo "  refused: no token (schema + render), sqlite replicas>1, postgres+PVC"

# install_and_check <keep|drop> <release> [--set ...]
#
# The lanes are independent, so each one is UNINSTALLED when it passes. They
# used to be left running, which fits a laptop and does not fit a two-core CI
# runner: with all three tiers resident the next release's pods go Pending on
# cpu and every later step reads as a timeout rather than as "the node is
# full". Only the lane the durability and upgrade steps need is kept.
install_and_check() {
  local keep=$1; shift
  local release=$1; shift
  say "install $release: $*"
  helm upgrade --install "$release" "$CHART" $CHART_FLAGS -n "$NS" \
    --set image.repository="$IMAGE_REPO" \
    --set image.tag="$IMAGE_TAG" \
    --set image.pullPolicy="$PULL_POLICY" \
    --set specToken.value=smoke-shared-contract-token \
    --set integration.id=acme-payments \
    --set integration.consumerDisplayName='Smoke Consumer' \
    --wait --timeout 5m "$@"

  local front="$release-flanj-collector-front"
  local store="$release-flanj-collector-store"

  # 1. Ready. --wait already blocked on it; assert the replica counts landed.
  kubectl -n "$NS" rollout status "deploy/$front" --timeout=120s
  if kubectl -n "$NS" get statefulset "$store" >/dev/null 2>&1; then
    kubectl -n "$NS" rollout status "statefulset/$store" --timeout=180s
  else
    kubectl -n "$NS" rollout status "deploy/$store" --timeout=180s
  fi

  # 2. the UI, on the store pod's loopback bind, through a port-forward — the
  #    only way it is ever reachable.
  local store_pod
  store_pod=$(kubectl -n "$NS" get pod -l "app.kubernetes.io/instance=$release,app.kubernetes.io/component=store" -o name | head -1)
  kubectl -n "$NS" port-forward "$store_pod" "5335:5335" >/dev/null 2>&1 &
  PF_PID=$!
  # ...and the FRONT Service, which is what the SDK targets.
  local front_port=14318
  kubectl -n "$NS" port-forward "svc/$front" "$front_port:4318" >/dev/null 2>&1 &
  local front_pf=$!
  sleep 3

  local health
  health=$(curl -fsS --max-time 10 http://127.0.0.1:5335/api/health) \
    || fail "$release: the UI did not answer on the store pod's loopback bind"
  echo "  /api/health: $health"
  [ "$(jq -r '.serves_fronts' <<<"$health")" = "true" ] \
    || fail "$release: the store pod does not report serves_fronts — spec_endpoint is not bound, so no front can read a contract"

  # 3. the hop. POST a call to the FRONT service; it must land in the STORE.
  local before after
  before=$(jq -r '.calls' <<<"$health")
  curl -fsS --max-time 10 -X POST "http://127.0.0.1:$front_port/v1/logs" \
    -H 'Content-Type: application/json' \
    --data-binary @<(jq 'del(._comment)' contracts/golden-otlp-call.json) \
    >/dev/null || fail "$release: the front refused the golden OTLP call"
  kill "$front_pf" 2>/dev/null || true

  local i=0
  while :; do
    after=$(curl -fsS --max-time 10 http://127.0.0.1:5335/api/health | jq -r '.calls')
    [ "$after" -gt "$before" ] && break
    i=$((i+1))
    [ $i -ge 30 ] && fail "$release: a call POSTed to the front never reached the store (calls stayed at $before) — the front->store hop is the topology"
    sleep 2
  done
  echo "  front -> store hop: calls $before -> $after"

  # 4. the silent failure. A front with a wrong token is Ready and detects
  #    nothing; it says so once per refresh, in its own log, and nowhere else.
  local sel="app.kubernetes.io/instance=$release,app.kubernetes.io/component=front"
  local frontlog
  frontlog=$(kubectl -n "$NS" logs -l "$sel" --tail=400 --all-containers 2>/dev/null || true)

  # 401 is the one that never clears: the roles hold different tokens, the pod
  # stays Ready, and no contract is ever read.
  #
  # MATCH THE COLLECTOR'S OWN SENTENCE, never a bare `401`. A front's log is
  # full of RFC3339 timestamps, and one in a thousand of them ends its
  # millisecond field in 401 — `2026-09-08T21:04:46.401Z`. On 2026-09-08 that
  # is exactly what happened: `grep -qiE '401|Unauthorized'` matched the
  # timestamp on an "otlphttp alias is deprecated" line and failed main with
  # "the two roles hold different tokens", while the fronts' only real problem
  # was the startup race the block below already tolerates. A check that
  # reports the wrong cause is worse than no check — it sends the next person
  # to read a token that was never wrong.
  #
  # The front emits `store pod returned 401 Unauthorized` and nothing else
  # (processor/flanjdrift/remotesource.go: `store pod returned %s` with
  # resp.Status; the store pod's specserver answers 401 for a bad token).
  # 403 is matched too: a proxy in front of the store pod authorizes
  # differently, and it is the same operator fix.
  if grep -qE "$AUTH_REFUSAL_RE" <<<"$frontlog"; then
    grep -E "$AUTH_REFUSAL_RE" <<<"$frontlog" | sed 's/^/  /' >&2
    fail "$release: a front is getting 401 from the store pod's contract endpoint — the two roles hold different tokens"
  fi
  # A front with no store_pod_endpoint detects no REST drift at all, and says
  # so exactly once, at start.
  if grep -q 'no contract source' <<<"$frontlog"; then
    fail "$release: a front has no store_pod_endpoint — it will detect no REST drift at all"
  fi
  # Every OTHER refresh failure has to be the startup race and nothing else:
  # fronts and the store pod start together, so the first tick can land before
  # :5337 is accepting. That clears on the next tick (ten seconds). One that
  # does not clear is a real one, whatever it says.
  i=0
  while kubectl -n "$NS" logs -l "$sel" --since=20s --all-containers 2>/dev/null \
        | grep -q 'contract refresh failed'; do
    i=$((i+1))
    if [ $i -ge 6 ]; then
      kubectl -n "$NS" logs -l "$sel" --since=20s --all-containers | grep 'contract refresh failed' | sed 's/^/  /' >&2
      fail "$release: fronts are still failing to refresh contracts a minute in — this is not the startup race"
    fi
    sleep 10
  done
  echo "  fronts: contract refresh settled, no 401, store_pod_endpoint set"

  kill "$PF_PID" 2>/dev/null || true; PF_PID=""

  if [ "$keep" = drop ]; then
    helm uninstall "$release" -n "$NS" --wait >/dev/null
    echo "  uninstalled $release (the node has to fit the next lane)"
  fi
}

# --- the three tiers, one at a time ----------------------------------------
# eval: sqlite on an emptyDir, one flag from the defaults.
install_and_check drop ev --set store.persistence.enabled=false --set collector.replicas=1

# scale: postgres with the bundled evaluation database, where the store tier
# may have more than one pod.
install_and_check drop pg \
  --set store.backend=postgres \
  --set store.persistence.enabled=false \
  --set store.replicas=2 \
  --set postgres.enabled=true \
  --set postgres.password=smoke-postgres-password \
  --set postgres.persistence.enabled=false

# small prod: the DEFAULTS — sqlite on a PVC. Kept, because the two steps below
# are about this tier. It is also the only lane where the image's `nonroot` uid
# meets a freshly provisioned volume, which is what podSecurityContext.fsGroup
# exists to survive.
install_and_check keep sq --set collector.replicas=2 --set store.persistence.size=1Gi

# --- 6. the PVC tier's whole promise ---------------------------------------
# The window has to outlive the pod. On the eval tier it does not, and that is
# the documented difference between the two — so assert the one that claims it.
say "the PVC tier keeps its window across a lost store pod"
kubectl -n "$NS" delete pod sq-flanj-collector-store-0 --wait=true >/dev/null
kubectl -n "$NS" rollout status statefulset/sq-flanj-collector-store --timeout=180s
kubectl -n "$NS" port-forward sts/sq-flanj-collector-store 5335:5335 >/dev/null 2>&1 &
PF_PID=$!
sleep 3
survived=$(curl -fsS --max-time 10 http://127.0.0.1:5335/api/health | jq -r '.calls')
[ "$survived" -ge 1 ] \
  || fail "the PVC store came back empty (calls=$survived) — the volume is not surviving the pod, which is the entire difference between this tier and the eval one"
echo "  calls after the store pod was deleted: $survived"
kill "$PF_PID" 2>/dev/null || true; PF_PID=""

# --- upgrade in place ------------------------------------------------------
say "helm upgrade (scale the fronts, the tier that is meant to grow)"
helm upgrade sq "$CHART" $CHART_FLAGS -n "$NS" \
  --set image.repository="$IMAGE_REPO" --set image.tag="$IMAGE_TAG" --set image.pullPolicy="$PULL_POLICY" \
  --set specToken.value=smoke-shared-contract-token \
  --set store.persistence.size=1Gi \
  --set collector.replicas=3 --wait --timeout 5m
kubectl -n "$NS" rollout status deploy/sq-flanj-collector-front --timeout=120s
[ "$(kubectl -n "$NS" get deploy sq-flanj-collector-front -o jsonpath='{.status.readyReplicas}')" = "3" ] \
  || fail "upgrade did not scale the fronts to 3"

say "helm-smoke: PASS"
