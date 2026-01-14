# Configuration Overview

Gordon uses a single TOML configuration file located at `~/.config/gordon/gordon.toml`.

## Configuration File Location

| Location | Purpose |
|----------|---------|
| `~/.config/gordon/gordon.toml` | Default configuration file |
| Custom path via `--config` flag | `gordon start --config /path/to/config.toml` |

## Minimal Configuration

```toml
[server]
port = 8080
registry_port = 5000
registry_domain = "registry.mydomain.com"

[routes]
"app.mydomain.com" = "myapp:latest"
```

## Full Configuration Reference

> **Note:** This example shows production-style paths. Default paths use `~/.gordon/` for user installations.

```toml
# Server settings
[server]
port = 8080                              # HTTP proxy port (default: 80)
registry_port = 5000                     # Registry port (default: 5000)
registry_domain = "registry.mydomain.com" # Required: registry domain
# data_dir = "~/.gordon"                 # Default for user installations

# Secrets backend
[secrets]
backend = "pass"                         # "pass", "sops", or "unsafe"

# Registry authentication
[registry_auth]
enabled = true
type = "token"                           # "password" or "token"
# Password auth:
# username = "deploy"
# password_hash = "gordon/registry/password_hash"
# Token auth:
token_secret = "gordon/registry/token_secret"
token_expiry = "720h"                    # Duration or 0 for never

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
| `[secrets]` | Secrets backend configuration | [Secrets](./secrets.md) |
| `[registry_auth]` | Registry authentication | [Registry Auth](./registry-auth.md) |
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
| `secrets.backend` | `"unsafe"` |
| `registry_auth.enabled` | `false` |
| `registry_auth.type` | `"password"` |
| `registry_auth.token_expiry` | `"720h"` |
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
- `secrets.backend`
- `registry_auth` settings

## Environment Variable Override

Configuration values can be overridden with environment variables:

```bash
GORDON_SERVER_PORT=8080 gordon start
GORDON_LOGGING_LEVEL=debug gordon start
```

Pattern: `GORDON_SECTION_KEY` (uppercase, underscores instead of dots)

## Related

- [Server Configuration](./server.md)
- [Routes Configuration](./routes.md)
- [External Routes](./external-routes.md)
- [Registry Authentication](./registry-auth.md)
