# Split bootstrap (from scratch)

This guide sets up a fresh **v3 split** Gordon deployment without `gordon migrate`.
Use it for new installs. `migrate` only converts a running v2 monolith and is
transitional; see [Migration runbook](./migration.md).

A split deployment runs four roles as separate containers on one private
component network. Each role is the same `gordon` binary started with
`gordon serve --role <role> --config <role.toml>`.

## Roles

| Role | Owns | Container-runtime (engine) access |
| --- | --- | --- |
| control | Admin API, desired state, authenticated snapshots and commands | No |
| runtime | Containers, images, networks, volumes, backups | **Yes** |
| edge | Public application traffic and registry forwarding | No |
| registry | OCI storage and durable push-event delivery | No |

Only **runtime** receives `DOCKER_HOST`, `PODMAN_HOST`, or `CONTAINER_HOST` and
the engine socket. Every other role reaches runtime and control over Gordon's
authenticated component gRPC, never the engine socket.

Connection directions (each role authenticates its **outbound** calls with its
own component token):

- control dials **runtime** (`[runtime].endpoint`) to apply commands and read state.
- runtime dials **control** to publish its actual state and events.
- edge dials **control** (`[control].endpoint`) for route/traffic snapshots and drain.
- registry dials **control** (`[control].event_endpoint`) to publish push events.

## Prerequisites

- Rootless Podman with the user socket active and API reachable (see
  [Rootless Podman](../../wiki/guides/podman-rootless.md)).
- The `gordon` image available or pullable by that Podman user.
- A secrets backend for auth material: `pass`, `sops`, or `unsafe` (`unsafe`
  reads plaintext env files with `0600` permissions; use it only where that is
  acceptable).
- A public TLS terminator (external listener or upstream proxy) in front of edge.

## 1. Create the private network

```bash
podman network create gordon-internal
```

Attach every component to this network with a stable alias: `gordon-control`,
`gordon-runtime`, `gordon-edge`, `gordon-registry`. Components address each other
by alias on this network only. Edge forwards registry requests to the
`gordon-registry` alias, never `localhost` or `127.0.0.1`.

## 2. Create one component token per role

Each token is scoped to its role. Omitting `--scope` grants the role's default
scopes, which are exactly what that role needs:

```bash
gordon auth component-token create --name control-1  --role control  --config ./control.toml
gordon auth component-token create --name runtime-1  --role runtime  --config ./control.toml
gordon auth component-token create --name edge-1     --role edge     --config ./control.toml
gordon auth component-token create --name registry-1 --role registry --config ./control.toml
```

Tokens are backed by the configured `auth.secrets_backend`, so create all four
against the same backend (here, control's config). The plaintext token is shown
once; store it in a secret manager and inject it as an environment variable.
Never place a token value in TOML, shell history, logs, or issue reports.

Default scopes per role:

| Role | Default scopes |
| --- | --- |
| control | `runtime:deploy`, `runtime:reconcile`, `runtime:logs`, `runtime:status`, `runtime:selfupdate`, `runtime:drain:ack`, `registry:inspect`, `control:event:publish`, `events:watch` |
| runtime | `runtime:state:publish`, `runtime:event:publish` |
| edge | `routes:watch`, `traffic:watch`, `edge:drain`, `edge:applied-state` |
| registry | `registry:event:publish`, `registry:status` |

Manage tokens with `gordon auth component-token list` and
`gordon auth component-token revoke <key-id>`. `--role` rejects a scope that the
role does not allow.

## 3. Write one config per role

The keys below are the real config surfaces. control and runtime read the main
`gordon.toml` structure (`internal/app/run.go`, `internal/app/runtime_control_wiring.go`);
edge reads `EdgeConfig` and registry reads `RegistryConfig`
(`internal/app/edge_config.go`, `internal/app/registry_config.go`). Do not invent
keys. Inject every token through `*_token_env`, not literal `token` values.

Addresses use the network aliases from step 1. Plaintext component gRPC is
acceptable **only** on the private `gordon-internal` network: set `insecure`/
`insecure_tls = true` only for that shape, never on a shared or public network.

### control.toml

```toml
[server]
data_dir = "/var/lib/gordon"

[control]
# Component gRPC server that runtime/edge/registry authenticate against.
listen_address = "0.0.0.0:9443"
# Where control dials runtime; carries the control token.
endpoint = "gordon-runtime:9444"
insecure_tls = true
edge_alias = "gordon-edge"
registry_alias = "gordon-registry"
registry_port = 5000

[control.http]
# Admin API (put a TLS terminator in front for remote CLI access).
listen_address = "0.0.0.0:8080"
insecure_tls = true

[runtime]
# Runtime endpoint control dials, and the control token used to reach it.
endpoint = "gordon-runtime:9444"
token_env = "GORDON_CONTROL_TOKEN"
insecure = true

[auth]
enabled = true
secrets_backend = "unsafe"
```

### runtime.toml

```toml
[server]
data_dir = "/var/lib/gordon"

[runtime]
# Runtime's own gRPC server bind on the private network.
listen_address = "0.0.0.0:9444"
# Control endpoint runtime dials to publish state/events, with the runtime token.
endpoint = "gordon-control:9443"
token_env = "GORDON_RUNTIME_TOKEN"
registry_storage_root = "/var/lib/gordon/registry"
insecure = true

[auth]
enabled = true
secrets_backend = "unsafe"
```

Runtime is the only role given engine access. Provide the rootless Podman socket
to this container (see the quadlet unit below and the rootless guide).

### edge.toml

```toml
[control]
# Control's component gRPC endpoint edge subscribes to, with the edge token.
endpoint = "gordon-control:9443"
token_env = "GORDON_EDGE_TOKEN"
insecure_tls = true

[edge]
# Plaintext listener behind the host TLS terminator.
listen_address = "0.0.0.0:8081"
registry_domain = "registry.example.com"
registry_forward_token_env = "GORDON_EDGE_REGISTRY_FORWARD_TOKEN"
trusted_proxy_cidrs = ["10.89.0.0/16"]

[edge.tls]
# The external terminator owns public TLS.
mode = "external"
```

### registry.toml

```toml
[storage]
data_dir = "/var/lib/gordon/registry"

[listen]
address = "0.0.0.0:5000"

[listen.tls]
mode = "external"

[auth]
enabled = true
secrets_backend = "unsafe"
type = "token"

[control]
# Control's component event endpoint registry publishes push events to.
event_endpoint = "gordon-control:9443"
event_token_env = "GORDON_REGISTRY_EVENT_TOKEN"
insecure_tls = true

[forwarding]
token_env = "GORDON_REGISTRY_FORWARD_TOKEN"
```

## 4. Quadlet units (rootless)

Place these under `~/.config/containers/systemd/`. Regenerate services with
`systemctl --user daemon-reload`.

`gordon-internal.network`:

```ini
[Network]
NetworkName=gordon-internal
```

`gordon-control.container`:

```ini
[Unit]
Description=Gordon control
After=gordon-runtime.service

[Container]
Image=ghcr.io/example/gordon:v3
Network=gordon-internal.network
NetworkAlias=gordon-control
Exec=serve --role control --config /etc/gordon/control.toml
Volume=%h/gordon/control.toml:/etc/gordon/control.toml:ro,Z
Volume=gordon-control-data:/var/lib/gordon:Z
Environment=GORDON_CONTROL_TOKEN=

[Install]
WantedBy=default.target
```

`gordon-runtime.container` (the only unit with engine access):

```ini
[Unit]
Description=Gordon runtime

[Container]
Image=ghcr.io/example/gordon:v3
Network=gordon-internal.network
NetworkAlias=gordon-runtime
Exec=serve --role runtime --config /etc/gordon/runtime.toml
Volume=%h/gordon/runtime.toml:/etc/gordon/runtime.toml:ro,Z
Volume=gordon-runtime-data:/var/lib/gordon:Z
# Expose only the rootless Podman socket to runtime. The exact host socket path
# is user-specific; see the rootless guide.
Volume=%t/podman/podman.sock:/run/podman/podman.sock:Z
Environment=CONTAINER_HOST=unix:///run/podman/podman.sock
Environment=GORDON_RUNTIME_TOKEN=

[Install]
WantedBy=default.target
```

`gordon-registry.container`:

```ini
[Unit]
Description=Gordon registry
After=gordon-control.service

[Container]
Image=ghcr.io/example/gordon:v3
Network=gordon-internal.network
NetworkAlias=gordon-registry
Exec=serve --role registry --config /etc/gordon/registry.toml
Volume=%h/gordon/registry.toml:/etc/gordon/registry.toml:ro,Z
Volume=gordon-registry-data:/var/lib/gordon/registry:Z
Environment=GORDON_REGISTRY_EVENT_TOKEN=
Environment=GORDON_REGISTRY_FORWARD_TOKEN=

[Install]
WantedBy=default.target
```

`gordon-edge.container`:

```ini
[Unit]
Description=Gordon edge
After=gordon-control.service gordon-registry.service

[Container]
Image=ghcr.io/example/gordon:v3
Network=gordon-internal.network
NetworkAlias=gordon-edge
Exec=serve --role edge --config /etc/gordon/edge.toml
Volume=%h/gordon/edge.toml:/etc/gordon/edge.toml:ro,Z
PublishPort=127.0.0.1:8081:8081
Environment=GORDON_EDGE_TOKEN=
Environment=GORDON_EDGE_REGISTRY_FORWARD_TOKEN=

[Install]
WantedBy=default.target
```

Set each `Environment=...TOKEN=` from your secret manager (for example with a
systemd `EnvironmentFile=` pointing at a `0600` file) rather than committing the
value. Point the host TLS terminator at edge's published port and registry.

## 5. Start order and health

Start runtime and control first, then registry, then edge:

```bash
systemctl --user start gordon-runtime.service gordon-control.service
systemctl --user start gordon-registry.service
systemctl --user start gordon-edge.service
```

Verify:

```bash
podman ps --filter label=gordon.component=true
status="$(curl -sS -o /dev/null -w '%{http_code}' https://registry.example.com/v2/)"
case "$status" in 200|401) ;; *) echo "registry probe failed: HTTP $status" >&2; exit 1;; esac
```

The registry probe accepts **only** `200` or `401`; `401` proves
edge-to-registry reachability when auth is enabled. Then deploy a test image and
confirm application traffic through edge.

## Related

- [Split mode](./split-mode.md)
- [Migration runbook](./migration.md)
- [Internal RPC conventions](../development/internal-rpc-conventions.md)
- [Rootless Podman](../../wiki/guides/podman-rootless.md)
