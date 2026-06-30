# Standalone Services

Standalone services are Gordon-managed containers for non-HTTP workloads such as game servers, databases, or TCP/UDP daemons. Gordon creates, starts, restarts, reconciles, and removes these containers from the `[[services]]` configuration during startup and reload.

Standalone services are separate from HTTP `[routes]`. Route normal web apps with `[routes]`; use `[[services]]` when Gordon must manage a long-running container that is reached by explicit L4 traffic routers.

## Example

```toml
[[services]]
name = "rust"
image = "registry.example.com:5000/rust:latest"
enabled = true
env_file = "/srv/gordon/services/rust.env"

[services.readiness]
type = "log"
path = "/steamcmd/rust/server.log"
contains = "Server startup complete"
timeout = "2m"

[[services.ports]]
name = "game"
container = 28015
protocol = "udp"
publish = "127.0.0.1:38015"

[[services.ports]]
name = "rcon"
container = 28016
protocol = "tcp"
publish = "127.0.0.1:38016"
trusted_cidrs = ["100.64.0.0/10"]
```

The `publish` address is the host-side bind address that Gordon's traffic manager dials; use loopback for private backends. Expose services through `[traffic]` routers instead of binding service containers directly to a public interface.

## Traffic routing

Use `service:<service>:<port-name>` from TCP, UDP, or TLS passthrough routers:

```toml
[entrypoints.rust]
address = "0.0.0.0:28015"
protocol = "udp"

[[traffic.udp.routers]]
name = "rust-game"
entrypoint = "rust"
service = "service:rust:game"

[entrypoints.rcon]
address = "0.0.0.0:28016"
protocol = "tcp"
trusted_cidrs = ["100.64.0.0/10"]

[[traffic.tcp.routers]]
name = "rust-rcon"
entrypoint = "rcon"
service = "service:rust:rcon"
```

Ports named `rcon` default to private. Private ports require non-empty `trusted_cidrs` on both the service port and target entrypoint, and the CIDR sets must match. To intentionally expose an RCON port publicly, set `public = true` on that port.

## Volumes

Explicit volumes are optional:

```toml
[[services.volumes]]
source = "rust-data"
target = "/steamcmd/rust"
read_only = false
```

When `[[services.volumes]]` is omitted, Gordon inspects the image `VOLUME` metadata and creates deterministic Gordon-managed named volumes for those paths. If the image has no `VOLUME` metadata, the service is stateless unless the image writes inside its own filesystem.

Gordon tracks image-discovered managed volumes and only removes those managed volumes when `cleanup.preserve_volumes = false`. Explicit named volumes and bind mounts are not deleted as managed image volumes.

## Cleanup

```toml
[services.cleanup]
preserve_volumes = true
remove_container = true
```

By default Gordon removes old or disabled service containers while preserving volumes. Set `preserve_volumes = false` only for disposable managed image volumes.

## Related

- [Traffic Plane Configuration](./traffic.md)
- [Configuration Reference](./reference.md)
- [CLI traffic status](../cli/traffic.md)
