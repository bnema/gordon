#!/usr/bin/env bash
# Negative checks of Gordon's CI host (needs `setup.sh --with-gordon`).
#
# Host side (curl, no token): CI host paths outside the allowlist -> 404,
# plain-http -> 404, ci host never serves the admin API.
# Job side (real ermite ID tokens, workflow scenarios.yml):
#   main   : wrong audience 401, policy subject 200, catalog/tags/pull/admin/
#            mount 404, tag image 400, other service 403, replay -> same op
#   other  : the branch subject is outside the policy -> 403
set -euo pipefail
. "$(dirname "$0")/lib.sh"
load_env

fail=0
check() { # name want got
	if [ "$2" = "$3" ]; then log "SCENARIO $1 PASS ($3)"; else log "SCENARIO $1 FAIL (want $2, got $3)"; fail=1; fi
}
ci="https://ci.localtest.me:$E2E_EDGE_PORT"
zero="sha256:$(printf '0%.0s' $(seq 64))"

# --- host side, anonymous ---------------------------------------------------
check anon-catalog 404 "$(host_curl -o /dev/null -w '%{http_code}' "$ci/v2/_catalog")"
check anon-manifest-pull 404 "$(host_curl -o /dev/null -w '%{http_code}' "$ci/v2/e2e-demo/manifests/v1")"
check anon-blob-pull 404 "$(host_curl -o /dev/null -w '%{http_code}' "$ci/v2/e2e-demo/blobs/$zero")"
check anon-admin 404 "$(host_curl -o /dev/null -w '%{http_code}' "$ci/admin/apps")"
check anon-wellknown 404 "$(host_curl -o /dev/null -w '%{http_code}' "$ci/.well-known/gordon/")"
check anon-v2-challenge 401 "$(host_curl -o /dev/null -w '%{http_code}' "$ci/v2/")"
# GET, not HEAD: the CI allowlist only admits GET /v2/ (what docker sends).
realm="$(host_curl -s -D- -o /dev/null "$ci/v2/" | tr -d '\r' | sed -n 's/^[Ww]ww-[Aa]uthenticate: *//p')"
case "$realm" in *'realm="https://ci.localtest.me/auth/token"'*) check realm ok ok ;; *) check realm 'realm=https://ci.localtest.me/auth/token' "$realm" ;; esac
# Plain HTTP to Gordon's edge from the trusted proxy peer, without
# X-Forwarded-Proto: https -> the CI router's HTTPS-only rule answers 404.
# (From any other peer Gordon's onboarding gate answers 403 first.)
http_status() { sed -n 's/^ *HTTP\/[0-9.]* \([0-9]*\).*/\1/p' | tail -1; }
plain="$(compose exec -T edge sh -c 'wget -S -q -O /dev/null --header "Host: ci.localtest.me" http://dind:8080/v2/ 2>&1' | http_status || true)"
check plain-http-trusted-peer 404 "${plain:-none}"
plain="$(tools tools sh -c 'wget -S -q -O /dev/null --header "Host: ci.localtest.me" http://dind:8080/v2/ 2>&1' | http_status || true)"
check plain-http-other-peer 403 "${plain:-none}"

# --- a ci-access token on the main host is refused ---------------------------
# (covered by Go tests; the job-side check exchanges only on the CI host)

# --- job side ---------------------------------------------------------------
digest="$(jq -r '.digest // empty' "$E2E_STATE/push-v1.json" 2>/dev/null || true)"
dispatch_and_wait scenarios.yml main -f "digest=$digest" || fail=1
id="$(jq -r .id "$E2E_STATE/runs/scenarios-main.json")"
f="$(save_run_log "$id" scenarios-main)" || fail=1
grep -E 'SCENARIO' "$f" | sed 's/^/    /' >&2 || true

# Branch "other": same tree, pushed by the owner, so the run is trusted and
# gets ID tokens, but its subject (ref:refs/heads/other) is not in the policy.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git -c http.sslCAInfo="$E2E_STATE/ca/root.crt" \
	-c "http.extraHeader=Authorization: Bearer $ERMITE_ADMIN_TOKEN" \
	-c "http.curloptResolve=ermite.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
	clone -q "https://ermite.localtest.me:$E2E_EDGE_PORT/$E2E_REPO.git" "$work/repo"
git -C "$work/repo" -c http.sslCAInfo="$E2E_STATE/ca/root.crt" \
	-c "http.extraHeader=Authorization: Bearer $ERMITE_ADMIN_TOKEN" \
	-c "http.curloptResolve=ermite.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
	push -qf origin main:other
dispatch_and_wait scenarios.yml other || fail=1
id="$(jq -r .id "$E2E_STATE/runs/scenarios-other.json")"
f="$(save_run_log "$id" scenarios-other)" || fail=1
grep -E 'SCENARIO' "$f" | sed 's/^/    /' >&2 || true

exit "$fail"
