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
tls_enabled = false                          # Enable native HTTPS listener
tls_port = 443                               # HTTPS proxy port when enabled
tls_cert_file = ""                           # PEM cert path (auto-generated if empty and TLS enabled)
tls_key_file = ""                            # PEM key path (auto-generated if empty and TLS enabled)
gordon_domain = ""                           # Required: Gordon domain (registry + API)
data_dir = "~/.gordon"                       # Data directory (varies by install type)
max_blob_chunk_size = "512MB"                # Max size per registry blob upload chunk
registry_allowed_ips = []                    # IPs or CIDR ranges allowed to access the registry (empty = allow all)

# =============================================================================
# AUTHENTICATION (required - Gordon won't start without credentials configured)
# =============================================================================
[auth]
enabled = true                               # Enable registry authentication (default: true)
secrets_backend = "unsafe"                   # "pass", "sops", or "unsafe"
token_secret = ""                            # Path in secrets backend to JWT signing key (REQUIRED)
token_expiry = "720h"                        # Token expiry duration (720h = 30 days)
# Optional: enable password authentication (for interactive login)
# username = ""                              # Username for password auth
# password_hash = ""                         # Path in secrets backend to bcrypt hash

# =============================================================================
# API (applies to both Registry and Admin endpoints)
# =============================================================================
[api.rate_limit]
enabled = true                               # Enable rate limiting (default: true)
global_rps = 500                             # Max requests/second globally
per_ip_rps = 50                              # Max requests/second per client IP
burst = 100                                  # Burst size for rate limiters
trusted_proxies = []                         # IPs/CIDRs trusted to set X-Forwarded-For

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
readiness_mode = "auto"                      # "auto", "docker-health", "delay"
health_timeout = "90s"                       # Max wait for health-based readiness
readiness_delay = "5s"                       # Wait after running before considered ready
drain_mode = "auto"                          # "auto", "inflight", "delay"
drain_timeout = "30s"                        # Max wait for in-flight request drain
drain_delay = "2s"                           # Wait after cache invalidation before old stop

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

# =============================================================================
# BACKUPS
# =============================================================================
[backups]
enabled = false                              # Enable backup service
schedule = "daily"                          # "hourly", "daily", "weekly", "monthly"
storage_dir = ""                            # Backup root (default: {data_dir}/backups)

[backups.retention]
hourly = 0                                   # Keep N hourly backups per DB
daily = 0                                    # Keep N daily backups per DB
weekly = 0                                   # Keep N weekly backups per DB
monthly = 0                                  # Keep N monthly backups per DB

# =============================================================================
# IMAGES
# =============================================================================
[images.prune]
enabled = false                              # Enable scheduled image cleanup
schedule = "daily"                          # "hourly", "daily", "weekly", "monthly"
keep_last = 3                                # Keep N newest tags per repository

# Note: retention values set to 0 keep no backups for that tier.
# For practical defaults, consider setting daily = 7.
```

## Default Values Summary

| Setting | Default | Description |
|---------|---------|-------------|
| `server.port` | `80` | HTTP proxy port |
| `server.registry_port` | `5000` | Container registry port |
| `server.tls_enabled` | `false` | Enable native HTTPS listener |
| `server.tls_port` | `443` | HTTPS listener port |
| `server.tls_cert_file` | `""` | TLS cert path (auto-generated when empty) |
| `server.tls_key_file` | `""` | TLS key path (auto-generated when empty) |
| `server.gordon_domain` | `""` | **Required** - Gordon domain |
| `server.data_dir` | `~/.gordon` | Data directory |
| `server.max_blob_chunk_size` | `"512MB"` | Max size per registry blob upload chunk |
| `server.registry_allowed_ips` | `[]` | IPs or CIDR ranges allowed to access the registry (empty = allow all) |
| `auth.enabled` | `true` | Enable authentication |
| `auth.secrets_backend` | `"unsafe"` | Secrets storage |
| `auth.token_expiry` | `"720h"` | 30 days |
| `api.rate_limit.enabled` | `true` | Enable rate limiting |
| `api.rate_limit.global_rps` | `500` | Global requests/second |
| `api.rate_limit.per_ip_rps` | `50` | Per-IP requests/second |
| `api.rate_limit.burst` | `100` | Burst size |
| `api.rate_limit.trusted_proxies` | `[]` | IPs/CIDRs trusted for X-Forwarded-For |
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
| `deploy.readiness_mode` | `"auto"` | Readiness strategy (`auto`, `docker-health`, `delay`) |
| `deploy.health_timeout` | `"90s"` | Max wait for health-based readiness before deploy fails |
| `deploy.readiness_delay` | `"5s"` | Delay before container is considered ready |
| `deploy.drain_mode` | `"auto"` | Drain strategy (`auto`, `inflight`, `delay`) |
| `deploy.drain_timeout` | `"30s"` | Max wait for in-flight request drain before old stop |
| `deploy.drain_delay` | `"2s"` | Delay before stopping previous container after cache invalidation |
| `auto_route.enabled` | `false` | Auto-route disabled |
| `network_isolation.enabled` | `false` | Isolation disabled |
| `network_isolation.network_prefix` | `"gordon"` | Network prefix |
| `network_isolation.dns_suffix` | `".internal"` | DNS suffix |
| `volumes.auto_create` | `true` | Auto-create volumes |
| `volumes.prefix` | `"gordon"` | Volume prefix |
| `volumes.preserve` | `true` | Keep volumes |
| `backups.enabled` | `false` | Backup service disabled |
| `backups.schedule` | `"daily"` | Backup scheduler preset |
| `backups.storage_dir` | `""` | Uses `{server.data_dir}/backups` when empty |
| `backups.retention.hourly` | `0` | Keep no hourly backups by default |
| `backups.retention.daily` | `0` | Keep no daily backups by default (recommend `7`) |
| `backups.retention.weekly` | `0` | Keep no weekly backups by default |
| `backups.retention.monthly` | `0` | Keep no monthly backups by default |
| `images.prune.enabled` | `false` | Scheduled image cleanup disabled |
| `images.prune.schedule` | `"daily"` | Cleanup schedule preset |
| `images.prune.keep_last` | `3` | Number of recent tags kept per repository |

Note: for all `backups.retention.*` keys, `0` means keep no backups for that retention tier.

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
GORDON_LOGGING_LEVEL=debug
GORDON_NETWORK_ISOLATION_ENABLED=true
```

Nested keys use underscores:
```bash
GORDON_LOGGING_FILE_ENABLED=true
GORDON_LOGGING_FILE_MAX_SIZE=200
GORDON_LOGGING_CONTAINER_LOGS_ENABLED=false
```

### Security Environment Variables

These special environment variables take priority over config file values:

| Variable | Description |
|----------|-------------|
| `GORDON_AUTH_TOKEN_SECRET` | JWT signing secret (avoids storing secret on disk) |

Example:
```bash
export GORDON_AUTH_TOKEN_SECRET="your-secure-32-char-secret-here"
gordon serve
```

## Pull Policy Options

| Value | Behavior |
|-------|----------|
| `"always"` | Always pull image before deploying |
| `"if-tag-changed"` | Pull only if image tag differs from running container |
| `"never"` | Never pull, use local image only |

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
- [Images](./images.md)
