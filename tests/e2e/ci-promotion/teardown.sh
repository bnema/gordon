#!/usr/bin/env bash
# Removes everything the harness created: the containers, networks and volumes
# of compose project gordon-e2e-ci, the images it built, and the state dir.
#
#   bash teardown.sh            everything
#   bash teardown.sh --keep-state   keep $E2E_STATE (secrets, CA, logs)
set -euo pipefail
. "$(dirname "$0")/lib.sh"

keep=0
[ "${1:-}" = --keep-state ] && keep=1

if [ -f "$E2E_STATE/.env" ]; then
	compose --profile tools down -v --remove-orphans --rmi local
else
	docker compose -p "$E2E_PROJECT" -f "$E2E_DIR/compose.yaml" down -v --remove-orphans 2>/dev/null || true
fi
# Built by setup.sh on the host daemon for the seed push.
docker image rm -f e2e-demo:v1 >/dev/null 2>&1 || true

left="$(docker ps -aq --filter "label=com.docker.compose.project=$E2E_PROJECT")"
[ -z "$left" ] || die "containers of $E2E_PROJECT are still there: $left"

if [ "$keep" = 0 ]; then
	rm -rf "$E2E_STATE"
	log "removed $E2E_STATE"
fi
log "teardown done"
