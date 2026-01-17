# Configuration Examples

Annotated Gordon configuration examples for common scenarios.

## Examples

- [Minimal Configuration](./minimal.md) - Simplest working setup
- [Production Configuration](./production.md) - Full production setup
- [Development Configuration](./development.md) - Local development setup

## Quick Reference

### Minimal

```toml
[server]
gordon_domain = "gordon.local"

[routes]
"app.local" = "myapp:latest"
```

### Development

```toml
[server]
port = 8080
gordon_domain = "gordon.local"

[registry_auth]
enabled = false

[auto_route]
enabled = true

[routes]
"app.local" = "myapp:latest"
```

### Production

```toml
[server]
port = 8080
gordon_domain = "gordon.company.com"

[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "token"
token_secret = "gordon/registry/token_secret"

[network_isolation]
enabled = true

[routes]
"app.company.com" = "company-app:v2.1.0"

[attachments]
"app.company.com" = ["postgres:latest", "redis:latest"]
```

## Related

- [Configuration Reference](/docs/config/index.md)
- [Installation Guide](/docs/installation.md)
