#!/usr/bin/env bash
# Negative checks against Gordon's CI host with real ermite ID tokens.
# Prints "SCENARIO <name> PASS|FAIL (...)"; exits 1 when any check fails.
#
# EXPECT_SUBJECT_ALLOWED=1 on main (the policy subject), 0 on another branch.
# DIGEST is a pushed manifest digest of e2e-demo (from release), optional.
set -uo pipefail

CI=https://ci.localtest.me
AUD=https://ci.localtest.me
fail=0

check() { # name want got
	if [ "$2" = "$3" ]; then echo "SCENARIO $1 PASS ($3)"; else echo "SCENARIO $1 FAIL (want $2, got $3)"; fail=1; fi
}
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# Exchange: Basic oidc:<jwt> on /auth/token -> {"token": ...}. The basic
# credential is built in a variable and passed through a curl config on
# stdin, never on the command line.
exchange() { # jwt -> prints http code, writes body to /tmp/exchange.json
	local cred
	cred="$(printf 'oidc:%s' "$1" | base64 -w0)"
	printf 'header = "Authorization: Basic %s"\n' "$cred" |
		curl -s -K - -o /tmp/exchange.json -w '%{http_code}' \
			"$CI/auth/token?service=ci.localtest.me&scope=repository:e2e-demo:push,pull"
}

good_jwt="$(bash .github/scripts/oidc-token.sh "$AUD")"
wrong_aud_jwt="$(bash .github/scripts/oidc-token.sh "https://wrong-audience.localtest.me")"

# 1. wrong audience -> 401
check wrong-audience 401 "$(exchange "$wrong_aud_jwt")"

# 2. subject outside the policy -> 403; the policy subject -> 200
if [ "${EXPECT_SUBJECT_ALLOWED:-1}" = 1 ]; then
	check policy-subject 200 "$(exchange "$good_jwt")"
else
	check wrong-subject 403 "$(exchange "$good_jwt")"
	[ "$fail" = 0 ] && exit 0 || exit 1
fi

ci_token="$(jq -r '.token // .access_token' /tmp/exchange.json)"
echo "::add-mask::$ci_token"
auth=(-H "Authorization: Bearer $ci_token")

# 3. paths outside the allowlist -> 404, even with a valid ci-access token
check catalog 404 "$(code "${auth[@]}" "$CI/v2/_catalog")"
check tags-list 404 "$(code "${auth[@]}" "$CI/v2/e2e-demo/tags/list")"
check manifest-pull 404 "$(code "${auth[@]}" "$CI/v2/e2e-demo/manifests/v1")"
check blob-pull 404 "$(code "${auth[@]}" "$CI/v2/e2e-demo/blobs/sha256:$(printf '0%.0s' $(seq 64))")"
check admin 404 "$(code "${auth[@]}" "$CI/admin/apps")"
check mount 404 "$(code "${auth[@]}" -X POST "$CI/v2/e2e-demo/blobs/uploads/?mount=sha256:$(printf '0%.0s' $(seq 64))&from=other")"

# 4. promote with a tag image -> 400
promote() { # key image -> code, body in /tmp/promote.json
	curl -s -o /tmp/promote.json -w '%{http_code}' "${auth[@]}" \
		-H "Idempotency-Key: $1" -H 'Content-Type: application/json' \
		-d "{\"service\":\"web\",\"image\":\"$2\"}" "$CI/-/ci/apps/e2e-demo/promote"
}
check tag-image 400 "$(promote "e2e-tag-$GITHUB_RUN_ID" "e2e-demo:v1")"

# 5. other service of the same app -> 403
other="$(curl -s -o /dev/null -w '%{http_code}' "${auth[@]}" -H "Idempotency-Key: e2e-api-$GITHUB_RUN_ID" \
	-H 'Content-Type: application/json' -d '{"service":"api","image":"e2e-demo@sha256:'"$(printf 'a%.0s' $(seq 64))"'"}' \
	"$CI/-/ci/apps/e2e-demo/promote")"
check other-service 403 "$other"

# 6. replay the same key -> same operation
if [ -n "${DIGEST:-}" ]; then
	key="e2e-replay-$GITHUB_RUN_ID"
	c1="$(promote "$key" "e2e-demo@$DIGEST")"
	op1="$(jq -r .op /tmp/promote.json)"
	c2="$(promote "$key" "e2e-demo@$DIGEST")"
	op2="$(jq -r .op /tmp/promote.json)"
	echo "replay: first $c1 op=$op1, second $c2 op=$op2"
	if [ -n "$op1" ] && [ "$op1" != null ]; then check replay-same-op "$op1" "$op2"; else check replay-same-op '<op id>' "${op1:-empty}"; fi
	case "$c1" in 200 | 202) check replay-first-accepted ok ok ;; *) check replay-first-accepted 200/202 "$c1" ;; esac
	lookup="$(code "${auth[@]}" "$CI/-/ci/apps/e2e-demo/operations/by-key/$key")"
	check lookup-by-key 200 "$lookup"
else
	echo "SCENARIO replay SKIP (no DIGEST)"
fi

exit "$fail"
