# External Routes Configuration

External routes allow proxying to non-containerized services running on the host or network.

## Configuration

```toml
[external_routes]
"service.mydomain.com" = "localhost:3000"
"cache.mydomain.com" = "192.168.1.100:6379"
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

### Database Admin Tools

Proxy to database admin interfaces:

```toml
[external_routes]
"pgadmin.mydomain.com" = "localhost:5050"
"redis-commander.mydomain.com" = "localhost:8081"
```

### Legacy Services

Proxy to services that can't be containerized:

```toml
[external_routes]
"legacy-api.mydomain.com" = "192.168.1.50:8080"
```

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
"newservice.mydomain.com" = "localhost:9000"

# Save - Gordon reloads automatically
```

## Related

- [Routes Configuration](./routes.md)
- [Configuration Overview](./index.md)
