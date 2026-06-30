# Configuration Reference

Complete configuration reference with all options and their default values.

## Full Configuration Example

```toml
# =============================================================================
# SERVER
# =============================================================================
[server]
registry_port = 5000                         # Container registry port
tls_cert_file = ""                           # PEM cert path (optional, for static TLS fallback)
tls_key_file = ""                            # PEM key path (optional, must be set with tls_cert_file)
force_https_redirect = false                 # Redirect all HTTP traffic to HTTPS (for direct-access setups)
gordon_domain = ""                           # Required: Gordon domain (registry + API)
data_dir = "~/.gordon"                       # Data directory (varies by install type)
max_blob_chunk_size = "95MB"                 # Max size per registry blob upload chunk
max_blob_size = "1GB"                        # Max cumulative size per registry blob/layer upload
registry_allowed_ips = []                    # IPs or CIDR ranges allowed to access the registry (empty = allow all)
proxy_allowed_ips = []                       # IPs or CIDR ranges allowed to reach HTTP proxy paths (empty = allow all, e.g. Cloudflare IPs)
registry_listen_address = ""                 # Bind address for registry (empty = all interfaces, "127.0.0.1" = loopback only)

# =============================================================================
# ENTRYPOINTS
# =============================================================================
[entrypoints.edge]
address = ":443"                             # Deployment-selected public TCP socket
protocol = "smart_tcp"                       # Sniff HTTP, h2c, TLS, passthrough, or explicit raw fallback
trusted_cidrs = []                            # Peer socket IP allowlist for all traffic on this entrypoint
# raw_fallback = "ssh-fallback"              # Optional TCP router for unknown non-HTTP/non-TLS bytes
# raw_fallback_trusted_cidrs = ["100.64.0.0/10"]
# allow_public_raw_fallback = false

# =============================================================================
# DNS
# =============================================================================
[dns]
resolvers = ["1.1.1.1:53", "8.8.8.8:53"] # Recursive resolvers for public DNS visibility checks
propagation_timeout = "5m"                 # Max wait for DNS-01 TXT propagation
polling_interval = "5s"                    # Interval between DNS-01 propagation checks

# =============================================================================
# AUTHENTICATION (required - Gordon won't start without credentials configured)
# =============================================================================
[auth]
enabled = true                               # Enable registry authentication (default: true)
secrets_backend = "unsafe"                   # "pass", "sops", or "unsafe"
token_secret = ""                            # Path in secrets backend to JWT signing key (REQUIRED)
token_expiry = "30d"                         # Token expiry duration
access_token_ttl = "15m"                     # Ephemeral access token lifetime (default: 15m)

# =============================================================================
# PUBLIC TLS / ACME
# =============================================================================
[tls.acme]
enabled = false                              # Enable public ACME certificates (requires HTTPS fallback on a TLS-capable entrypoint)
email = ""                                   # ACME account email when enabled
challenge = "auto"                           # "auto", "http-01", or "cloudflare-dns-01"
obtain_batch_size = 1                         # New certificate orders per reconcile run

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

[logging.access_log]
enabled = false                              # Dedicated HTTP access log for reverse-proxy traffic
format = "json"                             # "json", "clf", or "combined"
output = "stdout"                           # "stdout", "file", or "journald"
file_path = ""                               # Required when output = "file"
max_size = 100                               # Max size in MB before rotation (file output)
max_backups = 3                              # Number of old files to keep (file output)
max_age = 28                                 # Days to keep old files (file output)
exclude_health_checks = true                 # Skip noisy readiness/liveness requests
syslog_identifier = "gordon-access"         # Journald identifier when output = "journald"

# =============================================================================
# TELEMETRY (OpenTelemetry)
# =============================================================================
[telemetry]
enabled = false                              # Enable OTLP telemetry export
endpoint = ""                                # OTLP HTTP endpoint URL
auth_token = ""                              # Base64-encoded user:password for Basic auth
traces = true                                # Export distributed traces
metrics = true                               # Export metrics
logs = true                                  # Bridge zerolog output to OTLP logs
trace_sample_rate = 1.0                      # Fraction of traces to sample (0.0–1.0)

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
# CONTAINERS
# =============================================================================
[containers]
security_profile = "compat"                  # "compat" or "strict"

# =============================================================================
# AUTO-ROUTE
# =============================================================================
[auto_route]
enabled = false                              # Create routes from image labels automatically

# =============================================================================
# NETWORK ISOLATION
# =============================================================================
[network_isolation]
enabled = true                               # Enable per-app Docker networks
network_prefix = "gordon"                    # Prefix for created networks
internal = false                             # Create Docker internal networks (blocks direct egress)

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
# "domain.com" = { image = "image:tag" }
# "insecure.domain.com" = { image = "image:tag", https = false }
# Legacy "http://domain.com" keys are read for compatibility and rewritten on save.

# =============================================================================
# EXTERNAL ROUTES
# =============================================================================
[external_routes]
# "domain.com" = "host:port"                 # Proxy to non-container services

# =============================================================================
# STANDALONE SERVICES
# =============================================================================
# [[services]]
# name = "rust"
# image = "registry.example.com:5000/rust:latest"
# enabled = true
# env_file = "/srv/gordon/services/rust.env"
#
# [[services.ports]]
# name = "game"
# container = 28015
# protocol = "udp"
# publish = "127.0.0.1:38015"
#
# [[services.ports]]
# name = "rcon"
# container = 28016
# protocol = "tcp"
# publish = "127.0.0.1:38016"
# trusted_cidrs = ["100.64.0.0/10"]

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
[images]
allowed_registries = []
require_digest = false

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
| `server.registry_port` | `5000` | Container registry port |
| `server.tls_cert_file` | `""` | PEM cert path for static TLS (optional) |
| `server.tls_key_file` | `""` | PEM key path for static TLS (optional) |
| `server.force_https_redirect` | `false` | Redirect all HTTP to HTTPS (for direct-access setups) |
| `server.gordon_domain` | `""` | **Required** - Gordon domain |
| `server.data_dir` | `~/.gordon` | Data directory |
| `server.max_blob_chunk_size` | `"95MB"` | Max size per registry blob upload chunk |
| `server.max_blob_size` | `"1GB"` | Max cumulative size per registry blob/layer upload |
| `server.registry_allowed_ips` | `[]` | IPs or CIDR ranges allowed to access the registry (empty = allow all) |
| `server.proxy_allowed_ips` | `[]` | IPs or CIDR ranges allowed to reach the proxy (empty = allow all) |
| `server.registry_listen_address` | `""` | Bind address for registry (empty = all interfaces) |
| `entrypoints.<name>.address` | none | Deployment-selected listen address; `edge` is conventional for route-capable entrypoints but is not required when exactly one `smart_tcp` or `tls_mux` entrypoint exists |
| `entrypoints.<name>.protocol` | none | Entrypoint protocol: `smart_tcp`, `tls_mux`, `tcp`, or `udp` |
| `entrypoints.<name>.trusted_cidrs` | `[]` | Peer socket IP allowlist for all traffic on the entrypoint |
| `entrypoints.<name>.raw_fallback` | `""` | TCP router used by smart TCP for unknown non-HTTP/non-TLS bytes |
| `entrypoints.<name>.raw_fallback_trusted_cidrs` | `[]` | Peer socket IP allowlist for smart TCP raw fallback |
| `entrypoints.<name>.allow_public_raw_fallback` | `false` | Explicit acknowledgement for public raw fallback exposure |
| `dns.resolvers` | `["1.1.1.1:53", "8.8.8.8:53"]` | Recursive resolvers used for public DNS visibility checks, including ACME DNS-01 propagation |
| `dns.propagation_timeout` | `"5m"` | Maximum time to wait for DNS-01 TXT records to become visible through configured recursive resolvers |
| `dns.polling_interval` | `"5s"` | Interval between DNS-01 propagation checks |
| `tls.acme.enabled` | `false` | Enable public ACME certificates (requires HTTPS fallback on a TLS-capable entrypoint) |
| `tls.acme.email` | `""` | ACME account email when enabled |
| `tls.acme.challenge` | `"auto"` | ACME challenge mode: `auto`, `http-01`, or `cloudflare-dns-01` |
| `tls.acme.obtain_batch_size` | `1` | Maximum new ACME certificate orders per reconcile run |
| `auth.enabled` | `true` | Enable authentication; when `false`, run local-only mode (loopback-only `/v2/*`, `/admin/*` disabled) |
| `auth.secrets_backend` | `"unsafe"` | Secrets storage |
| `auth.token_expiry` | `"30d"` | 30 days |
| `auth.access_token_ttl` | `"15m"` | Ephemeral access token lifetime |
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
| `logging.access_log.enabled` | `false` | Dedicated HTTP access log disabled |
| `logging.access_log.format` | `"json"` | Access log format (`json`, `clf`, `combined`) |
| `logging.access_log.output` | `"stdout"` | Access log sink (`stdout`, `file`, `journald`) |
| `logging.access_log.max_size` | `100` | 100 MB for file output |
| `logging.access_log.max_backups` | `3` | Keep 3 old files for file output |
| `logging.access_log.max_age` | `28` | 28 days for file output |
| `logging.access_log.exclude_health_checks` | `true` | Skip health-check requests |
| `logging.access_log.syslog_identifier` | `"gordon-access"` | Journald identifier |
| `telemetry.enabled` | `false` | Enable OTLP telemetry export |
| `telemetry.endpoint` | `""` | OTLP HTTP endpoint URL |
| `telemetry.auth_token` | `""` | Base64 `user:password` for Basic auth |
| `telemetry.traces` | `true` | Export distributed traces |
| `telemetry.metrics` | `true` | Export metrics |
| `telemetry.logs` | `true` | Bridge zerolog to OTLP logs |
| `telemetry.trace_sample_rate` | `1.0` | Fraction of traces to sample (0.0–1.0) |
| `deploy.pull_policy` | `"if-tag-changed"` | Pull on tag change |
| `deploy.readiness_mode` | `"auto"` | Readiness strategy (`auto`, `docker-health`, `delay`) |
| `deploy.health_timeout` | `"90s"` | Max wait for health-based readiness before deploy fails |
| `deploy.readiness_delay` | `"5s"` | Delay before container is considered ready |
| `deploy.drain_mode` | `"auto"` | Drain strategy (`auto`, `inflight`, `delay`) |
| `deploy.drain_timeout` | `"30s"` | Max wait for in-flight request drain before old stop |
| `deploy.drain_delay` | `"2s"` | Delay before stopping previous container after cache invalidation |
| `containers.security_profile` | `"compat"` | Runtime hardening profile: `compat` preserves existing behavior, `strict` enables read-only rootfs and narrower capabilities |
| `auto_route.enabled` | `false` | Auto-route disabled |
| `network_isolation.enabled` | `true` | Network isolation enabled |
| `network_isolation.network_prefix` | `"gordon"` | Network prefix |
| `network_isolation.internal` | `false` | Create Docker internal networks without direct external egress |
| `volumes.auto_create` | `true` | Auto-create volumes |
| `volumes.prefix` | `"gordon"` | Volume prefix |
| `volumes.preserve` | `true` | Keep volumes |
| `services[].name` | none | Standalone service name used by `service:<name>:<port>` traffic refs |
| `services[].image` | none | Container image for enabled standalone services |
| `services[].enabled` | `false` | Whether Gordon creates, starts, and reconciles the service container |
| `services[].env` | `[]` | Inline `KEY=value` environment entries |
| `services[].env_file` | `""` | Env file loaded before inline entries |
| `services[].ports[].name` | none | Port name used by traffic service refs |
| `services[].ports[].container` | none | Container port number |
| `services[].ports[].protocol` | none | `tcp` or `udp` |
| `services[].ports[].publish` | `""` | Host-side publish address, usually loopback for traffic-manager reachability |
| `services[].ports[].private` | `false` | Require matching service and entrypoint `trusted_cidrs` for this port |
| `services[].ports[].public` | `false` | Explicit public opt-out for admin port names such as `rcon` |
| `services[].ports[].trusted_cidrs` | `[]` | CIDRs allowed for private port routing; must match the target entrypoint |
| `services[].volumes[].source` | `""` | Explicit named volume or bind source; omitted service volumes use image `VOLUME` metadata |
| `services[].volumes[].target` | none | Absolute container mount path |
| `services[].volumes[].read_only` | `false` | Mount explicit volume read-only |
| `services[].readiness.type` | `"none"` | `none`, `tcp`, or `log` |
| `services[].readiness.path` | `""` | Log readiness path inside the container |
| `services[].readiness.contains` | `""` | Text required in the readiness log |
| `services[].readiness.timeout` | default wait | Positive readiness timeout when set |
| `services[].cleanup.preserve_volumes` | `true` | Preserve managed image-discovered volumes on cleanup |
| `services[].cleanup.remove_container` | `true` | Remove old, disabled, or removed service containers |
| `backups.enabled` | `false` | Backup service disabled |
| `backups.schedule` | `"daily"` | Backup scheduler preset |
| `backups.storage_dir` | `""` | Uses `{server.data_dir}/backups` when empty |
| `backups.retention.hourly` | `0` | Keep no hourly backups by default |
| `backups.retention.daily` | `0` | Keep no daily backups by default (recommend `7`) |
| `backups.retention.weekly` | `0` | Keep no weekly backups by default |
| `backups.retention.monthly` | `0` | Keep no monthly backups by default |
| `images.allowed_registries` | `[]` | Explicit external registry allowlist; empty rejects explicit external registries; dangerous local/private registries are always rejected |
| `images.require_digest` | `false` | Require digest-pinned references for allowlisted external registries |
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
GORDON_SERVER_PORT=8088
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
- [Telemetry](./telemetry.md)
- [Network Isolation](./network-isolation.md)
- [Volumes](./volumes.md)
- [Standalone Services](./services.md)
- [Images](./images.md)
