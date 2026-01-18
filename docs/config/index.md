# Configuration Overview

Gordon uses a single TOML configuration file located at `~/.config/gordon/gordon.toml`.

## Configuration File Location

| Location | Purpose |
|----------|---------|
| `~/.config/gordon/gordon.toml` | Default configuration file |
| Custom path via `--config` flag | `gordon serve --config /path/to/config.toml` |

## Minimal Configuration

```toml
[server]
port = 8080
registry_port = 5000
gordon_domain = "gordon.mydomain.com"

[routes]
"app.mydomain.com" = "myapp:latest"
```

## Full Configuration Reference

For a complete list of all configuration options with their default values, see the [Configuration Reference](./reference.md).

> **Note:** This example shows production-style paths. Default paths use `~/.gordon/` for user installations.

```toml
# Server settings
[server]
port = 8080                              # HTTP proxy port (default: 80)
registry_port = 5000                     # Registry port (default: 5000)
gordon_domain = "gordon.mydomain.com"    # Required: Gordon domain (registry + API)
# data_dir = "~/.gordon"                 # Default for user installations

# Authentication (includes secrets backend)
[auth]
enabled = true                           # Enable registry authentication (default: true)
type = "token"                           # "password" or "token"
secrets_backend = "pass"                 # "pass", "sops", or "unsafe"
# Password auth:
# username = "deploy"
# password_hash = "gordon/auth/password_hash"
# Token auth:
token_secret = "gordon/auth/token_secret"
token_expiry = "30d"                     # Duration (1y, 30d, 2w) or 0 for never

# API rate limiting
[api.rate_limit]
enabled = true                           # Enable rate limiting (default: true)
global_rps = 500                         # Max requests/second globally
per_ip_rps = 50                          # Max requests/second per IP
burst = 100                              # Burst size
trusted_proxies = []                     # IPs/CIDRs trusted for X-Forwarded-For

# Deploy behavior
[deploy]
pull_policy = "if-tag-changed"           # always, if-not-present, if-tag-changed

# Logging
[logging]
level = "info"                           # trace, debug, info, warn, error
format = "console"                       # console or json

[logging.file]
enabled = true
path = "~/.gordon/logs/gordon.log"       # Default location
max_size = 100                           # MB before rotation
max_backups = 3                          # Old files to keep
max_age = 28                             # Days to keep

[logging.container_logs]
enabled = true
dir = "~/.gordon/logs/containers"        # Default location
max_size = 100
max_backups = 3
max_age = 28

# Environment variables
[env]
dir = "~/.gordon/env"                    # Default location

# Volume settings
[volumes]
auto_create = true                       # Auto-create from Dockerfile VOLUME
prefix = "gordon"                        # Volume name prefix
preserve = true                          # Keep volumes on container removal

# Network isolation
[network_isolation]
enabled = true                           # Per-app isolated networks
network_prefix = "gordon"                # Network name prefix
dns_suffix = ".internal"                 # DNS suffix for services

# Auto-route
[auto_route]
enabled = false                          # Auto-create routes from image names

# Routes (required)
[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "myapi:v2.1.0"

# Network groups
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

# Attachments
[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
"backend" = ["rabbitmq:latest"]
```

## Configuration Sections

| Section | Description | Documentation |
|---------|-------------|---------------|
| `[server]` | Core server settings | [Server](./server.md) |
| `[auth]` | Authentication and secrets backend | [Auth](./auth.md) |
| `[api.rate_limit]` | Rate limiting configuration | [Rate Limiting](./rate-limiting.md) |
| `[deploy]` | Deployment behavior | [Deploy](./deploy.md) |
| `[logging]` | Logging configuration | [Logging](./logging.md) |
| `[env]` | Environment variable settings | [Environment](./env.md) |
| `[volumes]` | Volume management | [Volumes](./volumes.md) |
| `[network_isolation]` | Network isolation settings | [Network Isolation](./network-isolation.md) |
| `[auto_route]` | Automatic route creation | [Auto Route](./auto-route.md) |
| `[routes]` | Domain to image mapping | [Routes](./routes.md) |
| `[external_routes]` | Non-containerized service proxying | [External Routes](./external-routes.md) |
| `[network_groups]` | Shared service networks | [Network Groups](./network-groups.md) |
| `[attachments]` | Service dependencies | [Attachments](./attachments.md) |

## Default Values

| Setting | Default |
|---------|---------|
| `server.port` | `80` |
| `server.registry_port` | `5000` |
| `server.data_dir` | `~/.gordon` |
| `auth.enabled` | `true` |
| `auth.type` | `"password"` |
| `auth.secrets_backend` | `"unsafe"` |
| `auth.token_expiry` | `"30d"` |
| `api.rate_limit.enabled` | `true` |
| `api.rate_limit.global_rps` | `500` |
| `api.rate_limit.per_ip_rps` | `50` |
| `api.rate_limit.burst` | `100` |
| `api.rate_limit.trusted_proxies` | `[]` |
| `deploy.pull_policy` | `"if-tag-changed"` |
| `logging.level` | `"info"` |
| `logging.format` | `"console"` |
| `logging.file.enabled` | `false` |
| `logging.file.max_size` | `100` |
| `logging.file.max_backups` | `3` |
| `logging.file.max_age` | `28` |
| `logging.container_logs.enabled` | `true` |
| `volumes.auto_create` | `true` |
| `volumes.prefix` | `"gordon"` |
| `volumes.preserve` | `true` |
| `network_isolation.enabled` | `false` |
| `auto_route.enabled` | `false` |

## Hot Reload

Gordon watches the configuration file and reloads automatically when changes are detected. You can also trigger a manual reload:

```bash
gordon reload
```

The following settings require a restart to take effect:

- `server.port`
- `server.registry_port`
- `server.data_dir`
- `auth` settings
- `deploy.pull_policy`

## Environment Variable Override

Configuration values can be overridden with environment variables:

```bash
GORDON_SERVER_PORT=8080 gordon serve
GORDON_LOGGING_LEVEL=debug gordon serve
```

Pattern: `GORDON_SECTION_KEY` (uppercase, underscores instead of dots)

## Related

- [Server Configuration](./server.md)
- [Routes Configuration](./routes.md)
- [External Routes](./external-routes.md)
- [Authentication](./auth.md)
