# External Routes Configuration

External routes allow proxying to independently managed, non-containerized services on public network addresses. They are not a bridge to Gordon-managed standalone services.

## Configuration

```toml
[external_routes]
"service.mydomain.com" = "198.51.100.10:3000"
"cache.mydomain.com" = "backend.example.net:6379"
```

## Syntax

```toml
[external_routes]
"<domain>" = "<host>:<port>"
```

| Component | Description |
|-----------|-------------|
| `domain` | Fully qualified domain name |
| `host` | Target hostname or IP address |
| `port` | Target port number |

## Use Cases

Proxy to an independently managed public backend:

```toml
[external_routes]
"legacy-api.mydomain.com" = "api.example.net:8080"
```

For a local or private backend owned by Gordon, define it as `[[services]]` and select its HTTP port with `[service_routes]` instead.

## How It Works

1. Request arrives for `service.mydomain.com`
2. Gordon checks if domain matches an external route
3. If matched, proxies directly to the configured `host:port`
4. No container lookup is performed

```
Client ─> Gordon Proxy ─> External Service
          (port 80)       (configured host:port)
```

## Limitations

- Loopback, private, link-local, and otherwise internal targets are blocked by SSRF protection. Use `[service_routes]` for an HTTP hostname targeting a Gordon-managed `[[services]]` port.
- HTTP only (no HTTPS upstream)
- No health checks
- No load balancing

## Hot Reload

External routes reload automatically when the config file changes:

```bash
# Edit config
vim ~/.config/gordon/gordon.toml

# Add external route
[external_routes]
"newservice.mydomain.com" = "service.example.net:9000"

# Save - Gordon reloads automatically
```

## Related

- [Routes Configuration](./routes.md)
- [Configuration Overview](./index.md)
