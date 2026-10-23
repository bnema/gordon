# Shared helpers of the e2e scripts. Sourced by bash scripts only.
# shellcheck shell=bash

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E_STATE="${E2E_STATE:-$E2E_DIR/.state}"
E2E_PROJECT=gordon-e2e-ci
E2E_EDGE_PORT="${E2E_EDGE_PORT:-18443}"
E2E_REPO=e2e-demo
GORDON_SRC="${GORDON_SRC:-$(cd "$E2E_DIR/../../.." && pwd)}"
ERMITE_SRC="${ERMITE_SRC:-}"
export E2E_STATE E2E_EDGE_PORT GORDON_SRC ERMITE_SRC

log() { printf '\033[1;34m[e2e]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m[e2e] %s\033[0m\n' "$*" >&2; exit 1; }

compose() {
	docker compose -p "$E2E_PROJECT" -f "$E2E_DIR/compose.yaml" --env-file "$E2E_STATE/.env" "$@"
}

# load_env exports the generated secrets of .env into this shell.
load_env() {
	[ -f "$E2E_STATE/.env" ] || die "no $E2E_STATE/.env: run setup.sh first"
	set -a
	# shellcheck disable=SC1091
	. "$E2E_STATE/.env"
	set +a
}

# ermite_cli runs the ermite CLI inside the ermite container against its own
# loopback listener. The admin token travels through the environment, never
# on a command line.
ermite_cli() {
	ERMITE_TOKEN="$ERMITE_ADMIN_TOKEN" compose exec -T \
		-e ERMITE_URL=http://127.0.0.1:7341 -e ERMITE_TOKEN ermite ermite "$@"
}

# tools runs a command in the tools container on the e2e network, where the
# edge names resolve to the edge on 443. Extra -e NAME flags pass variables.
tools() {
	compose --profile tools run --rm -T "$@"
}

# gordon_admin runs the gordon CLI against the main host with the admin token
# of $E2E_STATE/gordon-admin-token (read inside the container).
gordon_admin() {
	# shellcheck disable=SC2016 # expanded inside the container
	tools tools sh -c 'GORDON_TOKEN="$(cat /state/gordon-admin-token)" exec gordon --remote https://gordon.localtest.me "$@"' gordon "$@"
}

# host_curl reaches the edge from the host with the Caddy root CA.
host_curl() {
	curl -sS --cacert "$E2E_STATE/ca/root.crt" \
		--resolve "ermite.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
		--resolve "ci.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
		--resolve "gordon.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
		--resolve "app.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
		--resolve "api.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
		--resolve "files.localtest.me:$E2E_EDGE_PORT:127.0.0.1" "$@"
}

# save_run_log ID NAME: writes the job logs of run ID to $E2E_STATE/runs/
# NAME-ID.log, prints its path, and fails when it holds a JWT or an ermite
# job/OIDC request token.
save_run_log() {
	local f="$E2E_STATE/runs/$2-$1.log"
	ermite_cli run view "$E2E_REPO" "$1" --log >"$f" 2>&1 || true
	printf '%s\n' "$f"
	if grep -Eq 'eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{20,}' "$f"; then
		log "FAIL: run $1 log contains a JWT"
		return 1
	fi
	if grep -Eq 'ermiteoidc_[A-Za-z0-9]|ermitegh_[A-Za-z0-9]' "$f"; then
		log "FAIL: run $1 log contains an ermite job token"
		return 1
	fi
	log "run $1 log: no JWT, no job token"
}

# dispatch_and_wait WORKFLOW REF [-f k=v...]: starts a workflow_dispatch run,
# waits for it, writes its JSON to $E2E_STATE/runs/<workflow>-<ref>.json and
# returns 0 only when it concluded "success".
dispatch_and_wait() {
	local wf="$1" ref="$2"
	shift 2
	local id out
	out="$(ermite_cli workflow run "$wf" --repo "$E2E_REPO" --ref "$ref" --json "$@")" || die "dispatch $wf@$ref failed"
	# .id is the run id of `run view` and of the web URL; .number counts the
	# runs of one workflow only.
	id="$(printf '%s' "$out" | jq -r '.id')"
	[ -n "$id" ] && [ "$id" != null ] || die "dispatch $wf@$ref returned no run id: $out"
	log "run $id: $wf on $ref"
	local status=""
	for _ in $(seq 1 360); do
		out="$(ermite_cli run view "$E2E_REPO" "$id" --json)" || true
		status="$(printf '%s' "$out" | jq -r '.status // empty')"
		[ "$status" = completed ] && break
		sleep 5
	done
	mkdir -p "$E2E_STATE/runs"
	printf '%s\n' "$out" >"$E2E_STATE/runs/${wf%.yml}-${ref//\//_}.json"
	local conclusion
	conclusion="$(printf '%s' "$out" | jq -r '.conclusion // empty')"
	log "run $id: status=$status conclusion=$conclusion"
	[ "$conclusion" = success ]
}
