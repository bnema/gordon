# Service ownership model

This document defines Gordon's service, container, port, route, and traffic ownership boundaries. The configuration forms shown here are canonical; removed forms are rejected rather than interpreted through compatibility logic.

## Ownership

A **service** is an application whose lifecycle Gordon owns. Gordon deploys it, keeps it running, checks readiness, replaces changed containers, updates routing targets, and cleans up removed resources.

A service owns one or more **containers**. Containers in the same service share a private network and resolve each other by their configured container names. Ports used only between those containers are not declared in Gordon configuration.

A named **service port** is a stable interface that Gordon may route. It identifies one owning container, one container port, an application protocol, and whether the application expects TLS-wrapped traffic. Declaring a service port does not create a public listener.

A **route** binds an HTTP hostname to an HTTP service port. It never owns an image or container.

A **traffic router** binds a TCP, UDP, or TLS-aware entrypoint to a compatible service port. Public exposure and source policy belong to the route, traffic router, or entrypoint—not to the container.

## Canonical configuration

### Single-container service

```toml
[services.app]
image = "registry.example.com/app:latest"
ports = { web = "8080/http", admin = "9090/http" }

[routes]
"app.example.com" = { service = "app", port = "web" }
"admin.example.com" = { service = "app", port = "admin" }
```

The top-level `image` form creates one implicit container. It cannot be combined with `services.<name>.containers`.

### Multi-container service

```toml
[services.shop.containers]
web = "registry.example.com/shop-web:latest"
postgres = "postgres:18"
valkey = "valkey/valkey:latest"

[services.shop.ports]
web = "web:3000/http"

[routes]
"shop.example.com" = { service = "shop", port = "web" }
```

Only `shop.web` is declared because Gordon routes it. The web container connects directly to `postgres:5432` and `valkey:6379` on the service network.

### Backend TLS

TLS is independent of the application protocol. It requires the expanded form:

```toml
[services.app.ports.secure_web]
port = 8443
protocol = "http"
tls = true
```

The only service-port protocol values are `http`, `tcp`, and `udp`. Gordon rejects `https`, `tls`, `h2c`, and compound values such as `http+tls`. UDP cannot set `tls = true`.

Public HTTPS termination is a route concern. On an HTTP service port, `tls = true` only means Gordon connects to the application using HTTPS.

### TCP and UDP

```toml
[services.game]
image = "registry.example.com/game:latest"
ports = { game = "28015/udp", rcon = "28016/tcp" }

[entrypoints.game]
address = ":28015"
protocol = "udp"

[[traffic.udp.routers]]
name = "game"
entrypoint = "game"
service = "game"
port = "game"

[entrypoints.rcon]
address = ":28016"
protocol = "tcp"
trusted_cidrs = ["100.64.0.0/10"]

[[traffic.tcp.routers]]
name = "rcon"
entrypoint = "rcon"
service = "game"
port = "rcon"
```

### TLS passthrough

```toml
[services.mail.ports.smtps]
port = 465
protocol = "tcp"
tls = true

[entrypoints.smtps]
address = ":465"
protocol = "tls"

[[traffic.tls.routers]]
name = "smtps"
entrypoint = "smtps"
sni = "mail.example.com"
service = "mail"
port = "smtps"
```

The traffic binding selects passthrough. The service port remains TCP and records that the application expects TLS-wrapped bytes.

## Runtime rules

- Gordon creates a private network per multi-container service and stable DNS aliases from container keys.
- Gordon publishes or resolves only declared service ports. Users do not coordinate host-side `publish` addresses.
- Internal-only ports remain direct private-network connections.
- Replacement creates a candidate, waits for readiness, atomically switches routed targets, drains the old container, then removes it.
- A changed container image replaces that container without restarting unrelated stateful containers after their compatibility checks pass.
- Removing a route removes only the HTTP binding. Removing a service owns container, network, and managed-volume cleanup.
- Image pushes resolve matching service containers. An explicit service/container selector disambiguates images used more than once.

## Removed forms

Gordon rejects these forms with migration guidance:

- `[[services]]` and `[[services.ports]]` arrays;
- image strings or `{ image = ... }` inside `[routes]`;
- route `{ target = "service:name:port" }` values;
- traffic `service = "service:name:port"` references;
- manually coordinated service-port `publish` addresses;
- service-port protocols `https`, `tls`, `h2c`, or `http+tls`.

## Lifecycle command boundary

Service commands own deployment:

```text
gordon services add|list|show|deploy|restart|logs|disable|remove
gordon push --service <service> [--container <container>]
```

Route commands own HTTP bindings:

```text
gordon routes add <domain> <service> <port>
gordon routes remove <domain>
```

Removing a route must never stop or remove a service container.
