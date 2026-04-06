#!/usr/bin/env bash
# Verify deploy/helm/workspace-operator/Chart.yaml version vs appVersion.
# For unified releases they must match. Chart-only hotfix: set ALLOW_CHART_APP_MISMATCH=1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${ROOT}/deploy/helm/workspace-operator/Chart.yaml"

if [[ ! -f "$CHART" ]]; then
  echo "release-verify: missing ${CHART}" >&2
  exit 1
fi

chart_ver=$(grep -E '^version:' "$CHART" | awk '{print $2}' | tr -d '\r')
app_ver=$(grep -E '^appVersion:' "$CHART" | sed -E 's/^appVersion:[[:space:]]*"?([^"]*)"?$/\1/')

if [[ -z "$chart_ver" || -z "$app_ver" ]]; then
  echo "release-verify: could not parse version or appVersion in Chart.yaml" >&2
  exit 1
fi

if [[ "$chart_ver" == "$app_ver" ]]; then
  echo "release-verify: OK — chart version and appVersion both ${chart_ver}"
  exit 0
fi

if [[ "${ALLOW_CHART_APP_MISMATCH:-}" == "1" ]]; then
  echo "release-verify: OK (ALLOW_CHART_APP_MISMATCH=1) — chart ${chart_ver}, appVersion ${app_ver}"
  exit 0
fi

echo "release-verify: Chart version (${chart_ver}) != appVersion (${app_ver})." >&2
echo "For a unified release, set both to the same semver (see docs/release-process.md)." >&2
echo "For a chart-only hotfix, export ALLOW_CHART_APP_MISMATCH=1 and re-run." >&2
exit 1
