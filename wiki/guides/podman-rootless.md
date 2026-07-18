# Rootless Podman

Rootless Podman is the supported runtime for monolith-to-split production migration.

## Prepare the user runtime

```bash
sudo apt install -y podman slirp4netns uidmap
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$USER"
systemctl --user enable --now podman.socket
sudo loginctl enable-linger "$USER"
podman info --format '{{.Host.Security.Rootless}}'
```

The final command must print `true`. Run Gordon and Podman as the same user. For a host monolith/runtime service:

```ini
[Service]
Environment=XDG_RUNTIME_DIR=/run/user/%U
Environment=DOCKER_HOST=unix:///run/user/%U/podman/podman.sock
ExecStart=/usr/local/bin/gordon serve
```

In split mode, these environment values and the Podman socket belong **only to runtime**. Control, edge, and registry must not receive them.

## Public ports

Rootless processes cannot bind ports below 1024. Bind the monolith edge to an unprivileged port and forward the public port:

```toml
[entrypoints.edge]
address = ":9000"
protocol = "smart_tcp"
```

```bash
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=9000
sudo firewall-cmd --reload
```

During orchestrated split migration, Gordon owns the temporary loopback probe and final listener transfer. The generated split edge uses external TLS mode: terminate TLS in an operator-owned proxy/load balancer and send clear HTTP to `server.port`. A raw 443-to-high-port firewall redirect applies to the monolith smart-TCP listener, not the final split edge. Do not independently publish component gRPC or registry loopback ports.

## Registry clients

Configure clients for the public `server.gordon_domain`. A local plaintext registry exception can be useful for monolith development, but split edge forwards registry requests over the private component network to `gordon-registry`, never to `localhost`.

```toml
[[registry]]
location = "gordon.example.com"
insecure = false
```

## Migrate

Set the migration image and private handoff seed, then follow the [migration runbook](/docs/operations/migration.md):

```bash
export GORDON_MIGRATION_IMAGE="ghcr.io/example/gordon:<target-version>"
export GORDON_RUNTIME_HANDOFF_TOKEN="$(openssl rand -hex 32)"
gordon migrate plan --config ~/.config/gordon/gordon.toml --json
```

## Diagnose

```bash
systemctl --user status podman.socket
podman info
podman ps --filter label=gordon.component=true
gordon migrate status --config ~/.config/gordon/gordon.toml --json
```

Do not delete checkpoint, generated environment, or outbox state to recover a retry. Repair the failed dependency and run `gordon migrate resume`.
