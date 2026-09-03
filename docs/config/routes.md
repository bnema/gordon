# Routes

A **route** sends HTTP requests for a hostname to one named port of a service.

Routes do not run images and do not create containers. Services own applications and their deployment lifecycle; routes only decide where HTTP requests go.

## Example

```toml
[services.app]
image = "registry.example.com/app:latest"
ports = { web = "8080/http" }

[routes]
"app.example.com" = { service = "app", port = "web" }
```

Read this route as:

> Send HTTP requests for `app.example.com` to the `web` port of the `app` service.

Gordon creates one container for the `app` service. Adding another route to the same service does not create another container:

```toml
[routes]
"app.example.com" = { service = "app", port = "web" }
"www.example.com" = { service = "app", port = "web" }
```

## Route fields

| Field | Required | Description |
|---|---:|---|
| `service` | yes | Name of a configured service |
| `port` | yes | Name of an `http` port on that service |
| `https` | no | Whether the hostname should receive HTTPS certificate coverage; defaults to `true` |

The hostname is the key in the `[routes]` table. It must be a valid fully qualified domain name.

## HTTP-only development route

```toml
[routes]
"dev-app.example.com" = { service = "app", port = "web", https = false }
```

Setting `https = false` disables certificate coverage for that hostname. It does not change the protocol spoken by the application port.

## Backend protocols

A route targets an `http` service port. TLS is a separate property of that port:

```toml
[services.app.ports.secure_web]
port = 8443
protocol = "http"
tls = true
```

With `tls = true`, Gordon uses HTTPS when connecting to the service. Without it, Gordon uses HTTP. This backend setting is independent of public HTTPS termination on the route.

A route cannot target a `tcp` or `udp` port. Use a traffic router for those protocols.

## External applications

Use `[external_routes]` when Gordon does not deploy or manage the destination application:

```toml
[external_routes]
"legacy.example.com" = "backend.example.net:8080"
```

External routes have separate SSRF protections. A normal route cannot contain an arbitrary network address.

## Removed image-backed route syntax

Routes previously combined application deployment and HTTP routing:

```toml
# Removed
[routes]
"app.example.com" = { image = "app:latest" }
```

Split the application and route explicitly:

```toml
[services.app]
image = "app:latest"
ports = { web = "8080/http" }

[routes]
"app.example.com" = { service = "app", port = "web" }
```

Gordon reports a focused configuration error when it detects an image-backed route and points to this migration.

## Related

- [Services](./services.md)
- [External Routes](./external-routes.md)
- [Traffic Plane Configuration](./traffic.md)
- [Configuration Reference](./reference.md)
