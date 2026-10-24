#!/usr/bin/env bash
# Removes everything the harness created: the containers, networks and volumes
# of compose project gordon-e2e-ci, the images it built, and the state dir.
#
#   bash teardown.sh                everything
#   bash teardown.sh --keep-state   keep $E2E_STATE (secrets, CA, logs)
set -euo pipefail
. "$(dirname "$0")/lib.sh"

keep=0
for a in "$@"; do
	case "$a" in
	--keep-state) keep=1 ;;
	*) die "unknown argument $a" ;;
	esac
done

if [ -f "$E2E_STATE/.env" ]; then
	compose --profile tools down -v --remove-orphans --rmi local
else
	# No .env (setup never got that far, or it was removed): compose still
	# needs every required variable to parse the file, so pass placeholders.
	# down only matches resources by project name.
	E2E_STATE="$E2E_STATE" ERMITE_SRC="${ERMITE_SRC:-$E2E_DIR}" \
		E2E_SUBNET="$E2E_SUBNET" E2E_EDGE_IP="$E2E_EDGE_IP" \
		HOST_DOCKER_SOCK=/dev/null ERMITE_ADMIN_TOKEN=x ERMITE_SECRETS_KEY=x \
		GORDON_AUTH_TOKEN_SECRET=x \
		docker compose -p "$E2E_PROJECT" -f "$E2E_DIR/compose.yaml" --env-file /dev/null \
		--profile tools down -v --remove-orphans --rmi local
fi
# Built by setup.sh on the host daemon for the seed push, with every tag
# `gordon images push` added to it. Only images with the harness label go.
seed="$(docker image ls -q --filter label=gordon-e2e-ci.seed=1 | sort -u)"
if [ -n "$seed" ]; then
	# shellcheck disable=SC2086 # one image id per word
	docker image rm -f $seed >/dev/null
fi

left="$(docker ps -aq --filter "label=com.docker.compose.project=$E2E_PROJECT")"
[ -z "$left" ] || die "containers of $E2E_PROJECT are still there: $left"

if [ "$keep" = 0 ]; then
	rm -rf "$E2E_STATE"
	log "removed $E2E_STATE"
fi
log "teardown done"
