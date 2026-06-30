# Traffic Plane Configuration

Gordon models network listeners as a traffic graph: entrypoints receive packets or connections, routers select a service, and services resolve to backends.

## Smart TCP Edge Entrypoint

Public application traffic is normally exposed with a `smart_tcp` entrypoint. The conventional route-capable entrypoint name is `edge`, but Gordon does not require that name or assign a built-in public port. When exactly one route-capable `smart_tcp` or `tls_mux` entrypoint exists, normal Gordon routes use it even if it has a custom name. Choose the address that matches your deployment, firewall, and container mapping:

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
trusted_cidrs = []
```

Do not treat `entrypoints.edge.address` as an HTTP port or an HTTPS port. It is one TCP socket that sniffs each new connection and dispatches the original byte stream.

Supported entrypoint protocols are `smart_tcp`, `tls_mux`, `tcp`, and `udp`. `smart_tcp` is the primary public edge model; `tls_mux` can also serve normal Gordon routes through TLS fallback, `tcp` and `udp` are for explicit L4 services, and UDP remains separate from the TCP entrypoint.

## Smart TCP Dispatch Order

For each accepted connection on a `smart_tcp` entrypoint Gordon:

1. Applies entrypoint-wide `trusted_cidrs` to the peer socket IP.
2. Rejects PROXY protocol v1 and v2 prefixes. PROXY headers are not trusted or parsed.
3. Peeks a minimal prefix and replays the bytes to the selected handler/backend.
4. Dispatches cleartext HTTP/1.x and h2c prior-knowledge to Gordon's HTTP handler.
5. Dispatches TLS ClientHello traffic by SNI:
   - matching TLS passthrough router -> raw TCP backend without TLS termination;
   - no passthrough match -> normal Gordon HTTPS fallback.
6. Dispatches unknown non-HTTP/non-TLS bytes only to an explicit raw fallback router, after raw-fallback source policy allows it.
7. Refuses the connection when no deterministic handler or allowed fallback exists.

Malformed HTTP-looking or TLS-looking traffic is rejected instead of bypassing to raw fallback.

## Standalone and Network Services

Standalone services are Gordon-managed containers that L4 routers can target with `service:<service>:<port-name>`. Define them under `[[services]]` when Gordon should own the container lifecycle.

```toml
[[services]]
name = "rust"
image = "registry.example.com:5000/rust:latest"
enabled = true

[[services.ports]]
name = "game"
container = 28015
protocol = "udp"
publish = "127.0.0.1:38015"

[entrypoints.rust]
address = "0.0.0.0:28015"
protocol = "udp"

[[traffic.udp.routers]]
name = "rust-game"
entrypoint = "rust"
service = "service:rust:game"
```

Network services describe manually managed non-HTTP backends that L4 routers can target.

```toml
[[network_services]]
name = "postgres"

[[network_services.ports]]
name = "db"
container = 5432
protocol = "tcp"
```

Service references use:

- `route:<domain>` for configured HTTP routes
- `external_route:<domain>` for configured external HTTP routes
- `network_service:<service>:<port-name>` for manually managed TCP, UDP, and TLS passthrough backends
- `service:<service>:<port-name>` for Gordon-managed standalone service backends

`static:<name>` is reserved and currently unsupported.

## TLS Passthrough on Smart TCP

TLS passthrough routers run on `smart_tcp` or `tls_mux` entrypoints and route by ClientHello SNI without terminating TLS.

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"

[[traffic.tls.routers]]
name = "raw-tls"
entrypoint = "edge"
sni = "raw.example.com"
service = "network_service:raw:tls"
```

Exact SNI matches win over wildcard matches. Ambiguous wildcard overlaps and HTTP-host/TLS-passthrough conflicts on the same smart TCP entrypoint are rejected at validation time. HTTPS application routes that do not match a passthrough SNI use Gordon's normal HTTPS fallback and certificate selection.

## Raw TCP Fallback

Raw fallback is disabled by default. Unknown non-HTTP/non-TLS bytes reach a TCP backend only when the smart TCP entrypoint explicitly names one TCP router as `raw_fallback` and the source policy allows the peer socket IP.

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
raw_fallback = "ssh-fallback"
raw_fallback_trusted_cidrs = ["100.64.0.0/10"]

[[traffic.tcp.routers]]
name = "ssh-fallback"
entrypoint = "edge"
service = "network_service:ssh:ssh"
```

Use `raw_fallback_trusted_cidrs` for private raw fallback exposure. To expose raw fallback publicly, acknowledge it explicitly:

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
raw_fallback = "public-raw"
allow_public_raw_fallback = true
```

`trusted_cidrs` and `raw_fallback_trusted_cidrs` both use the peer socket IP. They do not use `X-Forwarded-For`.

## TCP Routers

Plain TCP entrypoints support one TCP router per entrypoint. TCP routers on a `smart_tcp` entrypoint are valid only when referenced by that entrypoint's `raw_fallback`.

```toml
[entrypoints.postgres]
address = "0.0.0.0:5432"
protocol = "tcp"
trusted_cidrs = ["100.64.0.0/10"]

[traffic.tcp]
dial_timeout = "10s"
idle_timeout = "5m"
drain_timeout = "30s"
max_connections = 1024

[[traffic.tcp.routers]]
name = "postgres"
entrypoint = "postgres"
service = "network_service:postgres:db"
```

If `max_connections` is omitted or set to `0`, Gordon applies the safe runtime default of `1024` active connections per entrypoint.

## UDP Routers

UDP is not sniffed or unified into smart TCP. Use a separate UDP entrypoint:

```toml
[entrypoints.game]
address = "0.0.0.0:7777"
protocol = "udp"

[traffic.udp]
idle_timeout = "30s"
drain_timeout = "30s"
max_sessions = 4096

[[traffic.udp.routers]]
name = "game"
entrypoint = "game"
service = "network_service:game:game"
```

UDP sessions are keyed by client address and expire after `idle_timeout`. If `max_sessions` is omitted or set to `0`, Gordon applies the safe runtime default of `4096` active sessions per entrypoint.

## Related

- [Server Settings](./server.md)
- [Routes](./routes.md)
- [Standalone Services](./services.md)
- [CLI traffic status](../cli/traffic.md)
