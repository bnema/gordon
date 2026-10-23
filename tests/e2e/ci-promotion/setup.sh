#!/usr/bin/env bash
# Brings the e2e stack up and seeds it.
#
#   bash setup.sh                 edge + ermite + runner, fixture repo pushed
#   bash setup.sh --with-gordon   also dind + gordon, demo app applied at v1
#
# Idempotent: re-running keeps the generated secrets and the data volumes.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

WITH_GORDON=0
for a in "$@"; do
	case "$a" in
	--with-gordon) WITH_GORDON=1 ;;
	*) die "unknown argument $a" ;;
	esac
done

for c in docker curl jq git go openssl; do command -v "$c" >/dev/null || die "$c is not installed"; done
[ -r /dev/kvm ] && [ -w /dev/kvm ] || die "/dev/kvm is not usable by $(id -un): the runner needs it"
docker image inspect "${RUNNER_IMAGE:-ermite-runner:e2e-final}" >/dev/null 2>&1 ||
	die "runner image ${RUNNER_IMAGE:-ermite-runner:e2e-final} missing: build it (ermite-runner docs/container.md) or set RUNNER_IMAGE"
[ -n "$ERMITE_SRC" ] || die "set ERMITE_SRC to an ermite checkout"
[ -f "$ERMITE_SRC/Dockerfile" ] || die "ERMITE_SRC=$ERMITE_SRC has no Dockerfile"

mkdir -p "$E2E_STATE"/{ca,files,runner,runs}
chmod 700 "$E2E_STATE"

# --- secrets (.env, untracked) --------------------------------------------
if [ ! -f "$E2E_STATE/.env" ]; then
	log "generating secrets in $E2E_STATE/.env"
	(
		umask 077
		{
			echo "E2E_STATE=$E2E_STATE"
			echo "E2E_EDGE_PORT=$E2E_EDGE_PORT"
			echo "ERMITE_SRC=$ERMITE_SRC"
			echo "HOST_DOCKER_SOCK=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/docker.sock"
			echo "ERMITE_ADMIN_TOKEN=$(openssl rand -hex 32)"
			echo "ERMITE_SECRETS_KEY=$(head -c 32 /dev/urandom | base64 -w0)"
			echo "GORDON_AUTH_TOKEN_SECRET=$(openssl rand -hex 32)"
		} >"$E2E_STATE/.env"
	)
fi
load_env

if ss -ltnH "sport = :$E2E_EDGE_PORT" | grep -q . && ! compose ps -q edge 2>/dev/null | grep -q .; then
	die "port 127.0.0.1:$E2E_EDGE_PORT is taken by something else: set E2E_EDGE_PORT"
fi

# --- gordon binary for jobs, served by the edge -----------------------------
log "building gordon (linux/amd64, static) from $GORDON_SRC"
(cd "$GORDON_SRC" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build -trimpath -o "$E2E_STATE/files/gordon" .)
(cd "$E2E_STATE/files" && sha256sum gordon >gordon.sha256)
chmod 755 "$E2E_STATE/files/gordon"
chmod 644 "$E2E_STATE/files/gordon.sha256"

# --- edge first, to export its root CA --------------------------------------
# The CA bundle the containers trust must exist before they start.
[ -f "$E2E_STATE/ca/bundle.crt" ] || cat /etc/ssl/certs/ca-certificates.crt >"$E2E_STATE/ca/bundle.crt"
log "starting the edge"
compose up -d edge
root=""
for _ in $(seq 1 60); do
	# Caddy creates its local CA on the first TLS handshake or at start.
	if compose exec -T edge test -f /data/caddy/pki/authorities/local/root.crt 2>/dev/null; then
		root="$(compose exec -T edge cat /data/caddy/pki/authorities/local/root.crt)"
		break
	fi
	sleep 1
done
[ -n "$root" ] || die "Caddy did not create its root CA"
rm -f "$E2E_STATE/ca/root.crt" "$E2E_STATE/ca/bundle.crt"
printf '%s\n' "$root" >"$E2E_STATE/ca/root.crt"
cat /etc/ssl/certs/ca-certificates.crt "$E2E_STATE/ca/root.crt" >"$E2E_STATE/ca/bundle.crt"
chmod 644 "$E2E_STATE/ca/"*
log "edge root CA: $(openssl x509 -in "$E2E_STATE/ca/root.crt" -noout -subject -fingerprint -sha256 | tr '\n' ' ')"

# --- ermite -----------------------------------------------------------------
log "building and starting ermite from $ERMITE_SRC"
compose up -d --build --wait ermite
for _ in $(seq 1 30); do
	host_curl -fs -o /dev/null "https://ermite.localtest.me:$E2E_EDGE_PORT/-/health" && break
	sleep 1
done
host_curl -fs -o /dev/null "https://ermite.localtest.me:$E2E_EDGE_PORT/-/health" || die "ermite is not reachable through the edge"
issuer="$(host_curl -fs "https://ermite.localtest.me:$E2E_EDGE_PORT/-/oidc/.well-known/openid-configuration" | jq -r .issuer)"
[ "$issuer" = "https://ermite.localtest.me/-/oidc" ] || die "unexpected OIDC issuer: $issuer"
log "ermite OIDC issuer: $issuer"

# --- runner -----------------------------------------------------------------
if [ ! -s "$E2E_STATE/runner/register-token" ]; then
	log "creating the runner registration token"
	(umask 077 && ermite_cli token create e2e-runner --preset runner --expires 1d >"$E2E_STATE/runner/register-token")
fi
# The runner reads it as root inside its container; the directory stays 0700.
chmod 644 "$E2E_STATE/runner/register-token"
log "starting the runner"
compose up -d runner
online=""
for _ in $(seq 1 90); do
	online="$(ermite_cli runner list --json 2>/dev/null | jq -r '.[] | select(.name=="e2e-runner" and .online) | .isolation' || true)"
	[ -n "$online" ] && break
	sleep 2
done
[ -n "$online" ] || { compose logs --tail 50 runner >&2; die "the runner did not come online"; }
log "runner online (isolation $online)"

# --- fixture repository -----------------------------------------------------
if ! ermite_cli repo show "$E2E_REPO" >/dev/null 2>&1; then
	ermite_cli repo create "$E2E_REPO" >/dev/null
fi
ermite_cli variable set E2E_CA_PEM --repo "$E2E_REPO" <"$E2E_STATE/ca/root.crt" >/dev/null
ermite_cli variable set E2E_PREPARE --repo "$E2E_REPO" <"$E2E_DIR/fixture-repo/.github/scripts/prepare-job.sh" >/dev/null

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp -r "$E2E_DIR/fixture-repo/." "$work/"
(
	cd "$work"
	git init -q -b main
	git -c user.name=e2e -c user.email=e2e@localtest.me -c commit.gpgsign=false add -A
	git -c user.name=e2e -c user.email=e2e@localtest.me -c commit.gpgsign=false commit -qm "e2e fixture"
	# The root token is the owner: its push makes later runs trusted (OIDC).
	git -c http.sslCAInfo="$E2E_STATE/ca/root.crt" \
		-c "http.extraHeader=Authorization: Bearer $ERMITE_ADMIN_TOKEN" \
		-c "http.curloptResolve=ermite.localtest.me:$E2E_EDGE_PORT:127.0.0.1" \
		push -qf "https://ermite.localtest.me:$E2E_EDGE_PORT/$E2E_REPO.git" main
)
log "fixture repo pushed to e2e/$E2E_REPO"

[ "$WITH_GORDON" = 1 ] || { log "base stack ready (without gordon)"; exit 0; }

# --- gordon -----------------------------------------------------------------
(umask 077 && sed "s|@TOKEN_SECRET@|$GORDON_AUTH_TOKEN_SECRET|" "$E2E_DIR/gordon.toml" >"$E2E_STATE/gordon.toml.new")
# The gordon user of the image (uid 1000 in the container) must read it.
chmod 644 "$E2E_STATE/gordon.toml.new"
mv "$E2E_STATE/gordon.toml.new" "$E2E_STATE/gordon.toml"
log "building and starting dind + gordon"
compose up -d --build --force-recreate dind gordon
for _ in $(seq 1 60); do
	code="$(host_curl -s -o /dev/null -w '%{http_code}' "https://gordon.localtest.me:$E2E_EDGE_PORT/v2/" || true)"
	[ "$code" = 401 ] || [ "$code" = 200 ] && break
	sleep 2
done
[ "$code" = 401 ] || [ "$code" = 200 ] || { compose logs --tail 80 gordon >&2; die "gordon registry not reachable through the edge (last $code)"; }

# Admin token for the host side (seed + scenarios); stays in the state dir.
if [ ! -s "$E2E_STATE/gordon-admin-token" ]; then
	(umask 077 && compose exec -T gordon ./gordon auth token generate -c /etc/gordon/gordon.toml \
		--subject e2e-admin --scopes 'push,pull,admin:*:*' --expiry 1d --raw >"$E2E_STATE/gordon-admin-token")
fi

# The CLI runs in the tools container: there gordon.localtest.me is the edge
# on 443, and the image is read from the host daemon through its socket.
log "building and pushing the demo image v1"
docker build -q --build-arg VERSION=v1 -t e2e-demo:v1 "$E2E_DIR/fixture-repo" >/dev/null
gordon_admin images push e2e-demo:v1 --tag v1 --json >"$E2E_STATE/push-v1.json"
image_v1="$(jq -r .image "$E2E_STATE/push-v1.json")"
log "v1 image: $image_v1"
sed "s|@IMAGE@|$image_v1|g" "$E2E_DIR/e2e-demo.toml.tmpl" >"$E2E_STATE/e2e-demo.toml"
gordon_admin apps apply --file /state/e2e-demo.toml
gordon_admin apps deploy e2e-demo --all
log "gordon ready, e2e-demo deployed at v1"
