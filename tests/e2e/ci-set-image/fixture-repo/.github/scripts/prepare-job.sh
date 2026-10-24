#!/usr/bin/env bash
# Makes the edge reachable and trusted in an e2e job. Run it from the first
# step, before actions/checkout, by piping it from the repository variable
# E2E_PREPARE (setup.sh stores this file there), with E2E_CA_PEM set:
#
#   run: printf '%s\n' "$E2E_PREPARE" | bash
#
# The runner resolves only ermite.localtest.me (to the sandbox gateway) and
# relays that port to the Caddy edge, which routes by SNI and Host; so every
# other edge name is pinned to the same address in /etc/hosts.
set -euo pipefail

[ -n "${E2E_CA_PEM:-}" ] || { echo "::error::repository variable E2E_CA_PEM is empty"; exit 1; }
printf '%s\n' "$E2E_CA_PEM" | sudo tee /usr/local/share/ca-certificates/e2e-edge.crt >/dev/null
sudo update-ca-certificates >/dev/null
echo "NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/e2e-edge.crt" >>"$GITHUB_ENV"

gw="$(getent ahostsv4 ermite.localtest.me | awk 'NR==1 {print $1}')"
[ -n "$gw" ] || { echo "::error::ermite.localtest.me does not resolve in the job"; exit 1; }
echo "edge relay address: $gw"
for h in ci.localtest.me files.localtest.me app.localtest.me api.localtest.me gordon.localtest.me; do
	printf '%s %s\n' "$gw" "$h" | sudo tee -a /etc/hosts >/dev/null
done

# dockerd reads registry CAs from certs.d at each connection; no restart.
sudo mkdir -p /etc/docker/certs.d/ci.localtest.me
printf '%s\n' "$E2E_CA_PEM" | sudo tee /etc/docker/certs.d/ci.localtest.me/ca.crt >/dev/null

if [ "${INSTALL_GORDON:-0}" = 1 ]; then
	curl -fsS -o /tmp/gordon https://files.localtest.me/gordon
	curl -fsS -o /tmp/gordon.sha256 https://files.localtest.me/gordon.sha256
	(cd /tmp && sha256sum -c gordon.sha256)
	sudo install -m 0755 /tmp/gordon /usr/local/bin/gordon
	gordon version || true
fi
