#!/usr/bin/env bash
# Guard: every published port that reaches the UI must carry a LOOPBACK host
# address.
#
# Why this exists. The UI serves every captured call and it has no credential —
# no password, no token, no login. Its only protection is that it binds
# container loopback (`ui_endpoint: 127.0.0.1:5335`, enforced in the UI
# extension), and a container cannot publish a port it reaches over another
# container's loopback, so the documented way in is a sidecar bridge on :5336
# sharing the collector's network namespace. That bridge UNDOES the bind: the
# moment its port is published with no host address, Docker binds 0.0.0.0 AND
# [::], and an unauthenticated UI is on the network. `-p 5335:5336` reads like a
# local convenience and is not one — `docker port` prints both wildcard binds.
#
# So: anything in this repo that documents or composes a publish of the UI's
# host port (5335) or the bridge's container port (5336) must name a loopback
# host address. Same shape as scripts/check-env-documented.sh — a doc and a
# shipped file are both part of the operator contract, and neither may drift
# away from what the code guarantees.
#
# What it reads: every tracked (and every not-yet-tracked, not-ignored) *.md,
# *.yml, *.yaml and *.sh.
# What it understands: `-p SPEC` / `--publish SPEC` and short-form compose
# `ports:` items. Long-form compose ports (target/published/host_ip) are covered
# coarsely — see LONG FORM below — because this repo writes the short form and a
# line scan cannot attribute a YAML mapping to its sequence item.
# What it deliberately ignores: `kubectl port-forward`, which binds the client's
# own loopback and publishes nothing, and prose that merely names a port.
#
# Runs in CI (.github/workflows/ci.yml) and locally:  bash scripts/check-ui-loopback.sh
set -euo pipefail

cd "$(dirname "$0")/.."

UI_HOST_PORT=5335   # what a person types into a browser
BRIDGE_PORT=5336    # the sidecar's container port, which the publish maps

note() { printf '%s\n' "$*" >&2; }

# Is this host address loopback? Empty — no address at all — is the dangerous
# case: Docker then publishes on every interface.
is_loopback() {
  case "$1" in
    localhost|::1|'[::1]') return 0 ;;
    127.*) return 0 ;;
    *) return 1 ;;
  esac
}

# verdict <spec> -> "ok" | "bad" | "skip"
#
# A publish spec is [host_ip:]host_port[:container_port][/proto]. It concerns us
# when the host port is the UI's, or the container port is the bridge's or the
# UI's.
verdict() {
  spec="${1%%/*}"; v_ip=""; v_hostp=""; v_ctrp=""
  # An IPv6 literal is bracketed; peel it off before splitting on ':'.
  case "$spec" in
    \[*\]:*) v_ip="${spec%%]:*}]"; spec="${spec#*]:}" ;;
  esac
  case "$spec" in
    *:*:*) v_ip="${spec%%:*}"; spec="${spec#*:}"; v_hostp="${spec%%:*}"; v_ctrp="${spec##*:}" ;;
    *:*)   v_hostp="${spec%%:*}"; v_ctrp="${spec##*:}" ;;
    *)     v_ctrp="$spec"; v_hostp="" ;;   # `-p 5336`: a random host port, on every interface
  esac
  case "$v_hostp" in ''|*[!0-9]*) [ -z "$v_hostp" ] || { echo skip; return; } ;; esac
  case "$v_ctrp" in ''|*[!0-9]*) echo skip; return ;; esac
  if [ "$v_hostp" = "$UI_HOST_PORT" ] || [ "$v_ctrp" = "$BRIDGE_PORT" ] || [ "$v_ctrp" = "$UI_HOST_PORT" ]; then
    if is_loopback "$v_ip"; then echo ok; else echo bad; fi
  else
    echo skip
  fi
}

# scan_line <line> -> prints every offending spec on that line, one per line
scan_line() {
  sl_line="$1"
  # 1. docker CLI:  -p SPEC   --publish SPEC   --publish=SPEC
  for sl_spec in $(printf '%s\n' "$sl_line" \
        | grep -oE '(^|[[:space:]])(-p|--publish)[[:space:]=]+"?[][0-9a-zA-Z.:/]+"?' 2>/dev/null \
        | sed -E 's/.*(-p|--publish)[[:space:]=]+//; s/"//g' || true); do
    if [ "$(verdict "$sl_spec")" = bad ]; then printf '%s\n' "$sl_spec"; fi
  done
  # 2. short-form compose `ports:` item — a sequence entry that is ONLY a port
  #    spec. A markdown bullet with any prose after the number does not match.
  if printf '%s\n' "$sl_line" | grep -qE '^[[:space:]]*-[[:space:]]*"?[][0-9a-zA-Z.:]*[0-9]+(/(tcp|udp))?"?[[:space:]]*$'; then
    sl_spec=$(printf '%s\n' "$sl_line" | sed -E 's/^[[:space:]]*-[[:space:]]*//; s/"//g; s/[[:space:]]*$//')
    case "$sl_spec" in
      *:*) if [ "$(verdict "$sl_spec")" = bad ]; then printf '%s\n' "$sl_spec"; fi ;;
    esac
  fi
  return 0
}

# --- self-test -------------------------------------------------------------
# A guard nobody has seen fail is not a guard. Both directions are proven here,
# with no repo and no Docker, so a matcher that quietly stops matching cannot
# pass as a clean repo. `if` rather than `&&`/`||`: under `set -e` a bare
# `[ x ] && fail` makes the PASSING case the failing status of the statement.
selftest() {
  for s in '5335:5336' '5335:5335' '0.0.0.0:5335:5336' '[::]:5335:5336' '5336' \
           '192.168.1.10:5335:5336' '5335:5336/tcp'; do
    if [ "$(verdict "$s")" != bad ]; then
      note "::error::self-test: publish spec '$s' must be REFUSED, got '$(verdict "$s")'"; exit 1
    fi
  done
  for s in '127.0.0.1:5335:5336' 'localhost:5335:5336' '[::1]:5335:5336' \
           '127.0.0.1:5335:5336/tcp' '4318:4318' '8080:8080'; do
    if [ "$(verdict "$s")" = bad ]; then
      note "::error::self-test: publish spec '$s' must be ACCEPTED"; exit 1
    fi
  done
  # The two line shapes that are NOT publishes. A port-forward binds the
  # client's own loopback; prose is prose. Either one flagged would make this
  # guard fail a repo that is correct, which is how a guard gets deleted.
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    if [ -n "$(scan_line "$l")" ]; then
      note "::error::self-test: not a publish, must not be flagged: $l"; exit 1
    fi
  done <<'NOTPUBLISH'
kubectl -n flanj port-forward sts/flanj-flanj-collector-store 5335:5335
npm run dev        # Vite on :5336, proxying /api -> a running collector (:5335)
    ui_endpoint: 127.0.0.1:5335 # loopback only — the collector is outbound-only
NOTPUBLISH
  # ...and the one that is.
  if [ "$(scan_line '  -p 4318:4318 -p 5335:5336 \')" != "5335:5336" ]; then
    note "::error::self-test: the docker-CLI matcher no longer sees '-p 5335:5336'"; exit 1
  fi
  echo "self-test: refuses wildcard publishes of the UI port, accepts loopback ones, ignores port-forward"
}

selftest

# --- the scan --------------------------------------------------------------
# --others --exclude-standard: a compose file added in this change but not yet
# `git add`ed is exactly the file this guard exists for.
#
# This script excludes ITSELF: the wildcard specs in its self-test are the
# fixtures that prove the matcher fires, and a guard cannot be its own offender.
FILES=$(git ls-files --cached --others --exclude-standard \
          '*.md' '*.yml' '*.yaml' '*.sh' 2>/dev/null \
        | grep -vE '^(ui/node_modules|_build|\.claude)/' \
        | grep -vxF 'scripts/check-ui-loopback.sh' || true)

if [ -z "$FILES" ]; then
  note "::error::no files to scan — is this a git checkout?"; exit 1
fi

fails=0
scanned=0
while IFS= read -r f; do
  [ -n "$f" ] || continue
  [ -f "$f" ] || continue
  scanned=$((scanned + 1))
  n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    while IFS= read -r offending; do
      [ -n "$offending" ] || continue
      note "::error file=$f,line=$n::publishes the UI on every interface: \`$offending\`. The UI has no credential; its loopback bind is its access control, and this publish undoes it. Write \`127.0.0.1:${UI_HOST_PORT}:${BRIDGE_PORT}\`."
      fails=1
    done <<EOF
$(scan_line "$line")
EOF
  done < "$f"
done <<EOF
$FILES
EOF

# --- LONG FORM -------------------------------------------------------------
# Long-form compose ports are a mapping per sequence item, which a line scan
# cannot attribute reliably. Rather than guess, refuse the combination outright:
# a compose file naming our ports in long form must also name a loopback
# `host_ip:`, and it gets read by a human. This repo writes the short form, so
# the branch is dormant — it exists so switching form cannot silently drop the
# address.
while IFS= read -r f; do
  [ -n "$f" ] || continue
  case "$f" in *compose*.yml|*compose*.yaml) ;; *) continue ;; esac
  if grep -qE "^[[:space:]]*(-[[:space:]]*)?(target|published):[[:space:]]*\"?($UI_HOST_PORT|$BRIDGE_PORT)\"?[[:space:]]*\$" "$f"; then
    if ! grep -qE "^[[:space:]]*(-[[:space:]]*)?host_ip:[[:space:]]*\"?(127\.[0-9.]+|localhost|::1|\[::1\])\"?" "$f"; then
      note "::error file=$f::uses the long-form ports syntax for the UI port with no loopback \`host_ip:\`. Write the short form \`\"127.0.0.1:${UI_HOST_PORT}:${BRIDGE_PORT}\"\`, which this guard can verify."
      fails=1
    fi
  fi
done <<EOF
$FILES
EOF

if [ "$fails" -ne 0 ]; then
  note ""
  note "The UI is unauthenticated by design: it is reachable only from the host it runs on."
  note "Every documented or composed publish of it must pin the host address to loopback."
  exit 1
fi

echo "UI loopback OK — $scanned files scanned, no publish of :$UI_HOST_PORT / :$BRIDGE_PORT without a loopback host address"
