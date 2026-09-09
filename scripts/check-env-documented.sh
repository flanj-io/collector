#!/usr/bin/env bash
# Guard: every FLANJ_* environment variable referenced by a shipped example
# config OR BY THE HELM CHART's rendered role configs must be documented in
# docs/DEPLOYMENT.md's env table.
#
# Why this exists: PR #19 changed the config surface (contracts moved from
# spec_path to UI uploads, which introduced spec_endpoint + FLANJ_SPEC_TOKEN)
# without touching DEPLOYMENT.md. The doc went on presenting an env contract
# that was missing a REQUIRED variable, so an operator following it verbatim
# got a store pod that refused to start. The docs are the only operator
# reference the README links to; a config change must not be able to strand
# them again silently.
#
# Runs in CI (.github/workflows/ci.yml) and locally:  bash scripts/check-env-documented.sh
set -euo pipefail

cd "$(dirname "$0")/.."

DOC=docs/DEPLOYMENT.md
SECTION='## Environment variables the configs read'

[ -f "$DOC" ] || { echo "::error::$DOC not found"; exit 1; }

# Every FLANJ_* name inside a ${env:...} reference in a shipped config. The glob
# is config/*.yaml, not config/*.example.yaml: the image bakes config.default.yaml
# (issue #55), which is a shipped config that is not an example, and a variable
# entering the contract through it must land in the table like any other.
# Commented-out lines count on purpose: they are documented variants an
# operator is invited to uncomment (FLANJ_PG_DSN is only ever shown that way).
from_config=$(grep -rhoE '\$\{env:FLANJ_[A-Z0-9_]+' config/*.yaml \
  | sed 's/^\${env://' | sort -u)

[ -n "$from_config" ] || { echo "::error::no \${env:FLANJ_*} references found in config/*.yaml — has the scan broken?"; exit 1; }

# The Helm chart renders its OWN role configs rather than using the image's
# baked ones, so it is a second way a variable can enter the operator contract
# without the doc noticing — the exact shape of #19, one directory over. Its
# templates are scanned the same way and held to the same table.
from_chart=""
if compgen -G 'charts/*/templates/*.yaml' >/dev/null; then
  from_chart=$(grep -rhoE '\$\{env:FLANJ_[A-Z0-9_]+' charts/*/templates/*.yaml \
    | sed 's/^\${env://' | sort -u)
fi

referenced=$(printf '%s\n%s\n' "$from_config" "$from_chart" | grep -v '^$' | sort -u)

# The env table: rows between the section heading and the next '## ' heading.
table=$(awk -v want="$SECTION" '
  $0 == want            { insec = 1; next }
  insec && /^## /       { exit }
  insec && /^\|/        { print }
' "$DOC")

[ -n "$table" ] || { echo "::error::$DOC: no markdown table under '$SECTION' (heading renamed or table removed?)"; exit 1; }

missing=0
for var in $referenced; do
  if ! grep -qF "$var" <<<"$table"; then
    src="config/*.yaml"
    grep -qx "$var" <<<"$from_config" || src="charts/*/templates"
    echo "::error file=$DOC::$var is referenced by $src but is missing from the env table under '$SECTION'"
    missing=1
  fi
done

if [ "$missing" -ne 0 ]; then
  echo
  echo "The operator env contract in $DOC is incomplete." >&2
  echo "Add a row per variable above: name, which role reads it, whether it is required, what it means." >&2
  exit 1
fi

echo "env contract OK — $(wc -w <<<"$referenced" | tr -d ' ') FLANJ_* variables documented"
echo "  config/*.yaml:"
# shellcheck disable=SC2086
printf '    %s\n' $from_config
if [ -n "$from_chart" ]; then
  echo "  charts/*/templates:"
  # shellcheck disable=SC2086
  printf '    %s\n' $from_chart
fi
