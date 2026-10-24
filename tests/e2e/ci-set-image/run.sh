#!/usr/bin/env bash
# Triggers a workflow of the fixture repo and waits for it.
#
#   bash run.sh probe              OIDC claims + relay checks (no Gordon CI host)
#   bash run.sh release [VERSION]  push + set the image of service web
#
# Exit 0 only when the run concluded "success". The run JSON is written to
# $E2E_STATE/runs/ and its log is checked for leaked tokens. A release also
# records its version in $E2E_STATE/runs/release.version for playwright.sh.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
load_env

wf="${1:-probe}"
version=""
case "$wf" in
probe) args=() ;;
release)
	version="${2:-v2}"
	args=(-f "version=$version")
	;;
*) die "unknown workflow $wf (probe or release)" ;;
esac

rc=0
dispatch_and_wait "$wf.yml" main "${args[@]}" || rc=$?
run_json="$E2E_STATE/runs/$wf-main.json"
id="$(jq -r .id "$run_json")"
printf '%s\n' "$id" >"$E2E_STATE/runs/$wf.last"
[ -z "$version" ] || printf '%s\n' "$version" >"$E2E_STATE/runs/release.version"

log_file="$(save_run_log "$id" "$wf")" || rc=1
log "log: $log_file"
grep -E 'CHECK|ci-edge-ok|app.localtest.me ->|"digest"|"outcome"|header:|payload:' "$log_file" | sed 's/^/    /' >&2 || true
exit "$rc"
