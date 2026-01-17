# Configuration Reference

Complete configuration reference with all options and their default values.

## Full Configuration Example

```toml
# =============================================================================
# SERVER
# =============================================================================
[server]
port = 80                                    # HTTP proxy port
registry_port = 5000                         # Container registry port
gordon_domain = ""                           # Required: Gordon domain (registry + API)
data_dir = "~/.gordon"                       # Data directory (varies by install type)

# =============================================================================
# AUTHENTICATION (required - Gordon won't start without credentials configured)
# =============================================================================
[auth]
enabled = true                               # Enable registry authentication (default: true)
type = "password"                            # "password" or "token"
secrets_backend = "unsafe"                   # "pass", "sops", or "unsafe"
username = ""                                # Username for password auth
password_hash = ""                           # Path in secrets backend to bcrypt hash (REQUIRED for password type)
token_secret = ""                            # Path in secrets backend to JWT signing key (REQUIRED for token type)
token_expiry = "720h"                        # Token expiry duration (720h = 30 days)

# =============================================================================
# LOGGING
# =============================================================================
[logging]
level = "info"                               # "debug", "info", "warn", "error"
format = "console"                           # "console" or "json"

[logging.file]
enabled = false                              # Enable file logging
path = ""                                    # Log file path (default: {data_dir}/logs/gordon.log)
max_size = 100                               # Max size in MB before rotation
max_backups = 3                              # Number of old files to keep
max_age = 28                                 # Days to keep old files

[logging.container_logs]
enabled = true                               # Enable container log collection
dir = ""                                     # Log directory (default: {data_dir}/container-logs)
max_size = 100                               # Max size in MB before rotation
max_backups = 3                              # Number of old files to keep
max_age = 28                                 # Days to keep old files

# =============================================================================
# ENVIRONMENT
# =============================================================================
[env]
dir = ""                                     # Env files directory (default: {data_dir}/env)

# =============================================================================
# DEPLOYMENT
# =============================================================================
[deploy]
pull_policy = "if-tag-changed"               # "always", "if-tag-changed", "never"

# =============================================================================
# AUTO-ROUTE
# =============================================================================
[auto_route]
enabled = false                              # Create routes from image labels automatically

# =============================================================================
# NETWORK ISOLATION
# =============================================================================
[network_isolation]
enabled = false                              # Enable per-app Docker networks
network_prefix = "gordon"                    # Prefix for created networks
dns_suffix = ".internal"                     # DNS suffix for internal resolution

# =============================================================================
# VOLUMES
# =============================================================================
[volumes]
auto_create = true                           # Auto-create volumes from Dockerfile VOLUME
prefix = "gordon"                            # Volume name prefix
preserve = true                              # Keep volumes when containers are removed

# =============================================================================
# ROUTES
# =============================================================================
[routes]
# "domain.com" = "image:tag"
# "http://insecure.domain.com" = "image:tag"   # HTTP-only (no HTTPS redirect)

# =============================================================================
# EXTERNAL ROUTES
# =============================================================================
[external_routes]
# "domain.com" = "host:port"                 # Proxy to non-container services

# =============================================================================
# NETWORK GROUPS
# =============================================================================
[network_groups]
# "group-name" = ["domain1.com", "domain2.com"]

# =============================================================================
# ATTACHMENTS
# =============================================================================
[attachments]
# "domain-or-group" = ["image1:tag", "image2:tag"]
```

## Default Values Summary

| Setting | Default | Description |
|---------|---------|-------------|
| `server.port` | `80` | HTTP proxy port |
| `server.registry_port` | `5000` | Container registry port |
| `server.gordon_domain` | `""` | **Required** - Gordon domain |
| `server.data_dir` | `~/.gordon` | Data directory |
| `auth.enabled` | `true` | Enable authentication |
| `auth.type` | `"password"` | Auth type |
| `auth.secrets_backend` | `"unsafe"` | Secrets storage |
| `auth.token_expiry` | `"720h"` | 30 days |
| `logging.level` | `"info"` | Log level |
| `logging.format` | `"console"` | Log format |
| `logging.file.enabled` | `false` | File logging disabled |
| `logging.file.max_size` | `100` | 100 MB |
| `logging.file.max_backups` | `3` | Keep 3 old files |
| `logging.file.max_age` | `28` | 28 days |
| `logging.container_logs.enabled` | `true` | Container logs enabled |
| `logging.container_logs.max_size` | `100` | 100 MB |
| `logging.container_logs.max_backups` | `3` | Keep 3 old files |
| `logging.container_logs.max_age` | `28` | 28 days |
| `deploy.pull_policy` | `"if-tag-changed"` | Pull on tag change |
| `auto_route.enabled` | `false` | Auto-route disabled |
| `network_isolation.enabled` | `false` | Isolation disabled |
| `network_isolation.network_prefix` | `"gordon"` | Network prefix |
| `network_isolation.dns_suffix` | `".internal"` | DNS suffix |
| `volumes.auto_create` | `true` | Auto-create volumes |
| `volumes.prefix` | `"gordon"` | Volume prefix |
| `volumes.preserve` | `true` | Keep volumes |

## Environment Variables

All configuration options can be set via environment variables using the pattern:

```
GORDON_<SECTION>_<KEY>=value
```

Examples:
```bash
GORDON_SERVER_PORT=8080
GORDON_SERVER_GORDON_DOMAIN=gordon.example.com
GORDON_AUTH_ENABLED=true
GORDON_AUTH_TYPE=token
GORDON_LOGGING_LEVEL=debug
GORDON_NETWORK_ISOLATION_ENABLED=true
```

Nested keys use underscores:
```bash
GORDON_LOGGING_FILE_ENABLED=true
GORDON_LOGGING_FILE_MAX_SIZE=200
GORDON_LOGGING_CONTAINER_LOGS_ENABLED=false
```

## Pull Policy Options

| Value | Behavior |
|-------|----------|
| `"always"` | Always pull image before deploying |
| `"if-tag-changed"` | Pull only if image tag differs from running container |
| `"never"` | Never pull, use local image only |

## Auth Type Options

| Value | Description |
|-------|-------------|
| `"password"` | HTTP Basic authentication with username/password |
| `"token"` | JWT token authentication |

## Secrets Backend Options

| Value | Description |
|-------|-------------|
| `"pass"` | Unix password manager (recommended for production) |
| `"sops"` | Mozilla SOPS encrypted files |
| `"unsafe"` | Plain text files (development only) |

## Log Level Options

| Value | Description |
|-------|-------------|
| `"debug"` | Verbose debugging information |
| `"info"` | General operational information |
| `"warn"` | Warning conditions |
| `"error"` | Error conditions only |

## Related

- [Configuration Overview](./index.md)
- [Authentication](./auth.md)
- [Network Isolation](./network-isolation.md)
- [Volumes](./volumes.md)
