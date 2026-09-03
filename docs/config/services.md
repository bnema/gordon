# Services

A **service** is an application that Gordon runs and keeps available.

You tell Gordon which image contains the application and which named ports the application provides. Gordon then creates the container, starts it, checks that it is ready, replaces it during deployments, and cleans up old containers.

The service remains the same even when Gordon replaces its container. Think of the service as the application and the container as the current running copy of that application.

## A first service

```toml
[services.app]
image = "registry.example.com/app:latest"

[services.app.ports]
web = "8080/http"
```

This configuration says:

- the service is named `app`;
- Gordon runs the image `registry.example.com/app:latest`;
- the application accepts HTTP traffic on container port `8080`;
- that port is named `web`, so routes can refer to it clearly.

Declaring a port does not make it public. A route or traffic router must explicitly expose it.

> **Do not declare ports used only between containers.** The `ports` table is not an inventory of every port opened by the application. It contains only stable, named service ports that Gordon must route. Containers in the same service communicate directly over their private network using the destination container name and port, such as `postgres:5432` or `valkey:6379`.

For example, a web application can use PostgreSQL and Valkey internally while exporting only its HTTP port:

```toml
[services.shop.containers]
web = "registry.example.com/shop-web:latest"
postgres = "postgres:18"
valkey = "valkey/valkey:latest"

[services.shop.ports]
web = "web:3000/http"
```

There is intentionally no `postgres` or `valkey` entry under `services.shop.ports`. Gordon creates private DNS names for those containers, so `web` connects directly to `postgres:5432` and `valkey:6379`.

## One service with several routed ports

One application can provide several protocols from the same container:

```toml
[services.app]
image = "registry.example.com/app:latest"

[services.app.ports]
web = "8080/http"
database = "5432/tcp"
game = "9000/udp"

[services.app.ports.secure_web]
port = 8443
protocol = "http"
tls = true
```

The left side is a name chosen by you. A compact value contains the container port followed by the protocol spoken by the application.

| Protocol | Use it when the application speaks |
|---|---|
| `http` | HTTP |
| `tcp` | A non-HTTP TCP protocol, such as PostgreSQL or RCON |
| `udp` | A UDP protocol, such as a game or DNS protocol |

TLS is not a protocol value. It is an independent property of a TCP-based port. Use an expanded port table with `tls = true` when the application expects an encrypted connection. Do not write `https`, `tls`, or `http+tls` as a protocol.

A plain integer is shorthand for a TCP port:

```toml
[services.database.ports]
postgres = 5432
```

Use an expanded declaration when a port needs additional settings:

```toml
[services.game.ports.rcon]
port = 28016
protocol = "tcp"
private = true
trusted_cidrs = ["100.64.0.0/10"]
```

Gordon allocates the loopback backend port that it uses to reach the container. Public listeners are configured with routes or traffic entrypoints.

## Send HTTP traffic to a service

A **route** connects an HTTP hostname to one named port of a service:

```toml
[services.app]
image = "registry.example.com/app:latest"
ports = { web = "8080/http" }

[routes]
"app.example.com" = { service = "app", port = "web" }
```

Read the route as:

> Send requests for `app.example.com` to the `web` port of the `app` service.

A route never creates a second container. The service owns deployment; the route only directs HTTP traffic.

HTTP routes target `http` service ports. When the service port has `tls = true`, Gordon uses HTTPS for the private backend connection. Public HTTPS is configured and terminated at the route layer, so most HTTP service ports do not need `tls = true`.

## Expose TCP or UDP

An entrypoint tells Gordon where to listen. A traffic router connects that listener to a named service port.

```toml
[services.game]
image = "registry.example.com/game:latest"

[services.game.ports]
game = { port = 28015, protocol = "udp" }

[entrypoints.game]
address = ":28015"
protocol = "udp"

[[traffic.udp.routers]]
name = "game"
entrypoint = "game"
service = "game"
port = "game"
```

Read the traffic router as:

> Forward UDP packets received by the `game` entrypoint to the `game` port of the `game` service.

## Pass TLS through unchanged

TLS passthrough is a traffic binding behavior. The service port remains TCP and separately records that its traffic is TLS-wrapped:

```toml
[services.api]
image = "registry.example.com/api:latest"

[services.api.ports.secure]
port = 9443
protocol = "tcp"
tls = true

[entrypoints.secure-api]
address = ":9443"
protocol = "tls"

[[traffic.tls.routers]]
name = "secure-api"
entrypoint = "secure-api"
sni = "api.example.com"
service = "api"
port = "secure"
```

Gordon reads the TLS server name to choose the service but does not decrypt the connection.

## Environment and readiness

```toml
[services.game]
image = "registry.example.com/game:latest"
env_file = "/srv/gordon/services/game.env"
env = ["REGION=eu"]

[services.game.readiness]
type = "log"
path = "/var/log/game/server.log"
contains = "Server startup complete"
timeout = "2m"
```

## Volumes

```toml
[[services.game.volumes]]
source = "game-data"
target = "/var/lib/game"
read_only = false
```

When no volumes are declared, Gordon inspects the image's `VOLUME` metadata and creates deterministic managed volumes for those paths. Gordon preserves volumes by default when replacing or removing a service container.

## Disable or remove a service

Services are enabled by default:

```toml
[services.game]
image = "game:latest"
enabled = false
```

When disabled, Gordon stops and removes the service container according to its cleanup policy.

```toml
[services.game.cleanup]
preserve_volumes = true
remove_container = true
```

## Removed configuration syntax

The old array syntax is no longer supported:

```toml
# Removed
[[services]]
name = "game"
image = "game:latest"

[[services.ports]]
name = "web"
container = 8080
protocol = "tcp"
```

Write the service and port names as table keys instead:

```toml
[services.game]
image = "game:latest"

[services.game.ports]
web = "8080/http"
```

Gordon reports a focused startup error when it detects the removed syntax.

## Related

- [Routes Configuration](./routes.md)
- [Traffic Plane Configuration](./traffic.md)
- [Configuration Reference](./reference.md)
- [CLI traffic status](../cli/traffic.md)
