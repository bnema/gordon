#!/usr/bin/env bash
# Runs the Playwright checks against the last run of a workflow.
#
#   bash playwright.sh release   run page + app versions (after run.sh release)
#   bash playwright.sh probe     run page only (after run.sh probe)
#
# Uses the playwright and chromium found on PATH (set CHROMIUM to override
# the browser); no npm install.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
load_env

kind="${1:-release}"
[ -f "$E2E_STATE/runs/$kind.last" ] || die "no $kind run recorded: bash run.sh $kind first"
E2E_RUN_ID="$(cat "$E2E_STATE/runs/$kind.last")"
if [ "$kind" = release ]; then
	[ -s "$E2E_STATE/runs/release.version" ] || die "no release version recorded: bash run.sh release first"
	E2E_VERSION="$(cat "$E2E_STATE/runs/release.version")"
	export E2E_VERSION
fi
command -v playwright >/dev/null || die "playwright is not on PATH"
if [ -z "${CHROMIUM:-}" ]; then
	for c in chromium chromium-browser google-chrome; do
		CHROMIUM="$(command -v "$c" || true)"
		[ -n "$CHROMIUM" ] && break
	done
fi
[ -n "${CHROMIUM:-}" ] && [ -x "$CHROMIUM" ] || die "no Chromium found: install chromium or set CHROMIUM"
export CHROMIUM
# A one-time owner login link: valid once, about a minute. Never printed.
E2E_LOGIN_URL="$(ermite_cli web login --json | jq -r .url)"
export E2E_RUN_ID E2E_LOGIN_URL E2E_EXPECT="$kind" E2E_EDGE_PORT
export NODE_PATH="${NODE_PATH:-$(dirname "$(readlink -f "$(command -v playwright)")")/..}"

cd "$E2E_DIR/playwright"
exec playwright test -c playwright.config.js
