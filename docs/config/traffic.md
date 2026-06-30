# Traffic Plane Configuration

Gordon models network listeners as a traffic graph: entrypoints receive connections, routers select a service, and services resolve to backends.

## Entrypoints

`server.port` creates the default HTTP entrypoint `web`. `server.tls_port` creates the default TLS multiplexer entrypoint `websecure` when non-zero.

Custom entrypoints use the top-level `entrypoints` table:

```toml
[entrypoints.postgres]
address = "0.0.0.0:5432"
protocol = "tcp"
trusted_cidrs = ["100.64.0.0/10"]

[entrypoints.game]
address = "0.0.0.0:7777"
protocol = "udp"
```

Protocols are `http`, `tls_mux`, `tcp`, and `udp`.

## Network Services

Network services describe non-HTTP backends that L4 routers can target.

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
- `network_service:<service>:<port-name>` for TCP, UDP, and TLS passthrough

`static:<name>` is reserved and currently unsupported.

## TCP Routers

```toml
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

A plain TCP entrypoint supports one TCP router. If `max_connections` is omitted or set to `0`, Gordon applies the safe runtime default of `1024` active connections per entrypoint.

## UDP Routers

```toml
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

## TLS Passthrough

TLS passthrough routers run on traffic-manager-owned `tls_mux` entrypoints and route by ClientHello SNI without terminating TLS. The default `websecure` entrypoint is reserved for Gordon's legacy HTTPS listener until full HTTP/HTTPS traffic-manager ownership is enabled; use a custom `tls_mux` entrypoint for passthrough.

```toml
[entrypoints.rawtls]
address = "0.0.0.0:9443"
protocol = "tls_mux"

[[traffic.tls.routers]]
name = "raw-tls"
entrypoint = "rawtls"
sni = "raw.example.com"
service = "network_service:raw:tls"
```

Exact SNI matches win over wildcard matches. Ambiguous wildcard overlaps and HTTP-host/TLS-passthrough conflicts are rejected at validation time.

## Related

- [Server Settings](./server.md)
- [Routes](./routes.md)
- [CLI traffic status](../cli/traffic.md)
