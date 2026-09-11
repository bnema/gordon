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
registry_port = 5000
gordon_domain = "gordon.mydomain.com"

[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
```

Application workloads live in standalone app files, not in `gordon.toml` — see [App Manifest](./apps.md). The pre-v2.50 `[routes]`, `[attachments]`, `[network_groups]`, `[[services]]`-as-apps, `[service_routes]`, `[auto_route]`, and `[previews]` keys were removed; Gordon refuses to start when any of them is present.
```

> **Note:** `gordon_domain` is the canonical key. Migrate older `registry_domain` values before restarting.
>
> For a staged registry host rename, set the new `server.gordon_domain` and keep old Gordon registry hosts in `server.legacy_registry_domains` until clients move. See [Server](./server.md#gordon-domain) and [Upgrading](../upgrading.md#staged-registry-host-rename).

## Full Configuration Reference

For a complete list of all configuration options with their default values, see the [Configuration Reference](./reference.md).

> **Note:** This example shows production-style paths. Default paths use `~/.gordon/` for user installations.

```toml
# Server settings
[server]
registry_port = 5000                     # Registry port (default: 5000)
gordon_domain = "gordon.mydomain.com"    # Required: Gordon domain (registry + API)
# data_dir = "~/.gordon"                 # Default for user installations

[entrypoints.edge]
address = ":443"                         # Deployment-selected public TCP socket
protocol = "smart_tcp"

# Authentication (includes secrets backend)
[auth]
enabled = true                           # Enable registry authentication (default: true)
secrets_backend = "pass"                 # "pass", "sops", or "unsafe"
token_secret = "gordon/auth/token_secret"  # Required: JWT signing secret
token_expiry = "30d"                     # Duration (1y, 30d, 2w) or 0 for never
# Token-only authentication (password login was removed in v2.30.0)

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
readiness_mode = "auto"                  # auto, docker-health, delay
health_timeout = "90s"                   # Max wait for health-based readiness
readiness_delay = "5s"                   # Wait after running before ready
drain_mode = "auto"                      # auto, inflight, delay
drain_timeout = "30s"                    # Max wait for in-flight drain
drain_delay = "2s"                       # Wait after proxy invalidation before stopping the old container

# Container runtime profile
[containers]
security_profile = "compat"              # compat or strict

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

# Telemetry (OpenTelemetry)
[telemetry]
enabled = true                           # Enable OTLP export (default: false)
endpoint = "http://localhost:5080/api/default"  # OTLP HTTP endpoint
auth_token = ""                          # Base64 user:password for Basic auth
traces = true                            # Export traces
metrics = true                           # Export metrics
logs = true                              # Bridge zerolog to OTLP logs
trace_sample_rate = 1.0                  # 0.0 = none, 1.0 = all

# Installation secret store location
[env]
dir = "~/.gordon/env"                    # Default location

# Volume settings
[volumes]
auto_create = true                       # Auto-create from Dockerfile VOLUME
prefix = "gordon"                        # Volume name prefix
preserve = true                          # Keep volumes on container removal

# Installation network policy (prefix filter for `gordon networks list`)
[network_isolation]
enabled = true                           # Gordon-managed network policy
network_prefix = "gordon"                # Network name prefix
internal = false                          # Set true to block direct egress from isolated networks

# REMOVED in v2.50 (declare apps in standalone files, see ./apps.md):
# [routes], [attachments], [network_groups], [[services]]-as-apps,
# [service_routes], [auto_route], [previews]

# Backups
[backups]
enabled = true
schedule = "daily"                        # "hourly", "daily", "weekly", "monthly"
storage_dir = "~/.gordon/backups"

[backups.retention]
hourly = 24
daily = 7
weekly = 4
monthly = 12

# Images
[images]
allowed_registries = []                   # Explicit external registries allowed for app images
require_digest = false                    # Require digests for allowlisted external registries

[images.prune]
enabled = false
schedule = "daily"
keep_last = 3
```

## Configuration Sections

| Section | Description | Documentation |
|---------|-------------|---------------|
| `[server]` | Core server settings | [Server](./server.md) |
| `[auth]` | Authentication and secrets backend | [Auth](./auth.md) |
| `[api.rate_limit]` | Rate limiting configuration | [Rate Limiting](./rate-limiting.md) |
| `[deploy]` | Deployment behavior | [Deploy](./deploy.md) |
| `[logging]` | Logging configuration | [Logging](./logging.md) |
| `[telemetry]` | OpenTelemetry observability export | [Telemetry](./telemetry.md) |
| `[env]` | Installation secret store location | [Environment](./env.md) |
| `[volumes]` | Volume management | [Volumes](./volumes.md) |
| `[network_isolation]` | Installation network policy | [Network Isolation](./network-isolation.md) |
| `[external_routes]` | Non-containerized service proxying | [External Routes](./external-routes.md) |
| `[entrypoints]`, `[traffic]`, `[[network_services]]`, `[[services]]` | L4 and TLS passthrough traffic plane | [Traffic](./traffic.md) |
| App files (`<app>.toml`) | Declarative apps: services, hosts, secrets, volumes | [App Manifest](./apps.md) |
| `[backups]` | Database backups | [Backups](./backups.md) |
| `[images.prune]` | Scheduled image cleanup | [Images](./images.md) |
| Security hardening | Security controls and recommended knobs | [Security Hardening](./security-hardening.md) |

## Default Values

| Setting | Default |
|---------|---------|
| `server.registry_port` | `5000` |
| `server.data_dir` | `~/.gordon` |
| `server.max_blob_chunk_size` | `"95MB"` |
| `server.max_blob_size` | `"1GB"` |
| `auth.enabled` | `true` |
| `auth.secrets_backend` | `"unsafe"` |
| `auth.token_expiry` | `"30d"` |
| `api.rate_limit.enabled` | `true` |
| `api.rate_limit.global_rps` | `500` |
| `api.rate_limit.per_ip_rps` | `50` |
| `api.rate_limit.burst` | `100` |
| `api.rate_limit.trusted_proxies` | `[]` |
| `deploy.pull_policy` | `"if-tag-changed"` |
| `deploy.readiness_mode` | `"auto"` |
| `deploy.health_timeout` | `"90s"` |
| `deploy.readiness_delay` | `"5s"` |
| `deploy.drain_mode` | `"auto"` |
| `deploy.drain_timeout` | `"30s"` |
| `deploy.drain_delay` | `"2s"` |
| `containers.security_profile` | `"compat"` |
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
| `network_isolation.enabled` | `true` |
| `network_isolation.internal` | `false` |
| `backups.enabled` | `false` |
| `backups.enabled` | `false` |
| `backups.schedule` | `"daily"` (`"hourly"`, `"daily"`, `"weekly"`, `"monthly"`) |
| `images.allowed_registries` | `[]` |
| `images.require_digest` | `false` |
| `images.prune.enabled` | `false` |
| `images.prune.schedule` | `"daily"` |
| `images.prune.keep_last` | `3` |
| `telemetry.enabled` | `false` |
| `telemetry.endpoint` | `""` |
| `telemetry.auth_token` | `""` |
| `telemetry.traces` | `true` |
| `telemetry.metrics` | `true` |
| `telemetry.logs` | `true` |
| `telemetry.trace_sample_rate` | `1.0` |

When `auth.enabled=false`, Gordon runs in local-only mode: `/admin/*` is not registered on the TCP listener and `/v2/*` is loopback-only. Local `gordon apps` commands use the daemon's owner-only admin socket (`$XDG_RUNTIME_DIR/gordon/admin.sock`, falling back to `~/.gordon/run/admin.sock`); see [Authentication](./auth.md#local-only-mode).

## Hot Reload

Gordon watches the configuration file and reloads automatically when changes are detected. You can also trigger a manual reload:

```bash
gordon reload
```

### Hot-reloaded (no restart needed)

| Setting |
|---------|
| `server.gordon_domain` (registry domain) |
| `server.registry_port` |
| `server.max_proxy_body_size` |
| `server.max_proxy_response_size` |
| `server.max_concurrent_conns` |


### Requires restart

| Setting |
|---------|
| `entrypoints.edge.address` |
| `server.data_dir` |
| `server.max_blob_chunk_size` |
| `server.max_blob_size` |
| `auth.*` |
| `deploy.readiness_mode` |
| `deploy.readiness_delay` |
| `deploy.health_timeout` |
| `deploy.drain_mode` |
| `deploy.drain_timeout` |

## Environment Variable Override

Configuration values can be overridden with environment variables:

```bash
GORDON_LOGGING_LEVEL=debug gordon serve
```

Pattern: `GORDON_SECTION_KEY` (uppercase, underscores instead of dots)

## Related

- [Server Configuration](./server.md)
- [App Manifest](./apps.md)
- [External Routes](./external-routes.md)
- [Standalone Services](./services.md)
- [Traffic Plane](./traffic.md)
- [Authentication](./auth.md)
- [Telemetry](./telemetry.md)
- [Backups](./backups.md)
- [Images](./images.md)
