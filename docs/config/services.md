# Standalone Services

Use `[[services]]` when Gordon should run one container that exposes more than one named port. Gordon owns that one container; each route only chooses which published port receives traffic.

## One container, several ports

This image exposes a web server, an optional TCP service, and an optional UDP service:

```toml
[[services]]
name = "app"
image = "registry.example.com:5000/app:latest"
enabled = true

[[services.ports]]
name = "web"
container_port = 8080
protocol = "tcp"
publish = "127.0.0.1:18080"

[[services.ports]]
name = "tcp"
container_port = 9000
protocol = "tcp"
publish = "127.0.0.1:19000"

[[services.ports]]
name = "udp"
container_port = 9001
protocol = "udp"
publish = "127.0.0.1:19001"
```

To expose the web port on a hostname, select it explicitly:

```toml
[service_routes]
"app.example.com" = { service = "app", port_name = "web" }
```

Gordon creates **one** `app` container and forwards HTTP traffic to its `web` port. It does not create a normal `[routes]` container for `app.example.com`.

The selected port must be TCP and have a valid `publish` address. `[service_routes]` is only for Gordon-managed services; external routes remain for non-private, independently managed backends.

## Optional TCP and UDP traffic

Use a traffic router only for ports that need L4 traffic. It can target another named port on the same container:

```toml
[entrypoints.app-udp]
address = ":9001"
protocol = "udp"

[[traffic.udp.routers]]
name = "app-udp"
entrypoint = "app-udp"
service = "service:app:udp"
```

The `web`, `tcp`, and `udp` names are yours. They make the intended port clear in configuration. Private ports require `trusted_cidrs`; Gordon enforces that policy before forwarding.

## Volumes

Explicit volumes are optional:

```toml
[[services.volumes]]
source = "app-data"
target = "/var/lib/app"
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
