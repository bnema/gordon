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
ci_domain = ""                               # Optional HTTPS CI host for push, set-image, and lookup
data_dir = "~/.gordon"                       # Data directory (varies by install type)
max_blob_chunk_size = "95MB"                 # Max size per registry blob upload chunk
max_blob_size = "1GB"                        # Max cumulative size per registry blob/layer upload
registry_allowed_ips = []                    # IPs or CIDR ranges allowed to access the registry (empty = allow all)
proxy_allowed_ips = []                       # IPs or CIDR ranges allowed to reach HTTP proxy paths (empty = allow all, e.g. Cloudflare IPs)
registry_listen_address = ""                 # Bind address for registry (empty = all interfaces, "127.0.0.1" = loopback only)

[server.http]                                # Registry HTTP server timeouts (Go durations)
read_header_timeout = "10s"                  # Max time to read request headers
read_timeout = "5m"                          # Max time to read a whole request
write_timeout = "5m"                         # Max time to write a whole response
idle_timeout = "120s"                        # Keep-alive idle timeout

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

[traffic.http]                               # HTTP servers behind entrypoints (apps + admin API)
read_header_timeout = "5s"                   # Max time to read request headers
idle_timeout = "30s"                         # Keep-alive idle timeout
transfer_idle_timeout = "2m"                 # Cut a transfer only after this long without data (no fixed read/write timeout)

[traffic.handover]                           # Traffic holding during single-writer handovers (read at startup)
max_hold = "90s"                             # Upper bound on holding traffic; "0s" withdraws instead
max_held_requests = 1024                     # Max HTTP requests held per host

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

# Workload logs are streamed from the container runtime with
# `gordon apps logs APP --service SERVICE`. The accepted
# logging.container_logs fields are not connected to a production file sink.

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
logs = true                                  # Export Gordon, access, and app logs
trace_sample_rate = 1.0                      # Fraction of traces to sample (0.0–1.0)

# =============================================================================
# CONTAINERS
# =============================================================================
[containers]
security_profile = "compat"                  # "compat" or "strict"

# =============================================================================
# NETWORK ISOLATION
# =============================================================================
[network_isolation]
enabled = true                               # Installation network policy for Gordon-managed networks
network_prefix = "gordon"                    # Prefix filter for `gordon daemon networks`
internal = false                             # Create Docker internal networks (blocks direct egress)

# =============================================================================
# VOLUMES
# =============================================================================
[volumes]
auto_create = true                           # Auto-create volumes from Dockerfile VOLUME
prefix = "gordon"                            # Volume name prefix
preserve = true                              # Keep volumes when containers are removed

# =============================================================================
# ADMINISTRATIVE APP BIND MOUNTS
# =============================================================================
# A named policy is the only way an app manifest may reference a host path.
# App manifests declare [[services.<name>.bind]] with name = "<mount>"; direct host
# paths are always rejected, and the policy's allowlists are exact and
# non-empty. read_only on the policy and readonly on the bind both force
# read-only: either side wins and a bind never weakens its policy.
# [app_mounts.app-logs]
# source = "/srv/gordon/host-logs"           # Required; absolute, normalized
# read_only = true                           # Force read-only for this mount
# allowed_apps = ["metrics-agent"]           # Required; exact, non-empty
# allowed_services = ["web"]                 # Required; exact, non-empty
# root = "/srv/gordon"                       # Optional; default: parent of source

# =============================================================================
# ADMINISTRATIVE APP DEVICES (CDI)
# =============================================================================
# A named device grant is the only way an app manifest may request host
# devices. Manifests declare devices = ["<name>"]; raw /dev paths are
# always rejected, and the policy's allowlists are exact and non-empty.
# Aggregate "=all" CDI selectors are rejected: name every device
# explicitly. Device-bearing creates require Podman 5.4+ or Docker 28.3+
# with native CDI configured.
# [app_devices.transcode-gpu]
# cdi = ["example.com/gpu=GPU-device-uuid"]  # Required; explicit, non-empty
# allowed_apps = ["video"]                   # Required; exact, non-empty
# allowed_services = ["transcoder"]          # Required; exact, non-empty

# =============================================================================
# ADMINISTRATIVE APP CAPABILITIES
# =============================================================================
# A named capability grant is the only way an app manifest may add Linux
# capabilities. The manifest declares capabilities = ["<name>"] (a policy
# name, not a capability). Granted capabilities are added on top of Gordon's
# defaults; cap_drop = ALL, no-new-privileges, and non-privileged mode always
# remain. Allowed names (no CAP_ prefix): AUDIT_WRITE, CHOWN, DAC_OVERRIDE,
# FOWNER, FSETID, IPC_LOCK, KILL, MKNOD, NET_ADMIN, NET_BIND_SERVICE,
# NET_RAW, SETFCAP, SETGID, SETPCAP, SETUID, SYS_CHROOT, SYS_NICE,
# SYS_RESOURCE. ALL, SYS_ADMIN, SYS_PTRACE, SYS_MODULE and the rest are
# rejected. allowed_apps and allowed_services are exact and non-empty.
# [app_capabilities.vm-net]
# add = ["NET_ADMIN"]                        # Required; allowed set only
# allowed_apps = ["myapp"]             # Required; exact, non-empty
# allowed_services = ["worker"]              # Required; exact, non-empty

# REMOVED in v3: [routes], [attachments], [network_groups],
# app-like [[services]], [service_routes], [auto_route], [previews].
# Declare apps in standalone files (see ./apps.md). The standalone
# [[services]] table below stays valid for installation-level L4 workloads.

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
# BACKUPS
# =============================================================================
[backups.databases]
enabled = false                              # Enable PostgreSQL backups
schedule = "daily"                          # "hourly", "daily", "weekly", "monthly"
time = ""                                   # "HH:MM" server local time (empty: preset default)
storage_dir = ""                            # Backup root (default: {data_dir}/backups)

[backups.databases.retention]
hourly = 0                                   # Keep N hourly backups per DB
daily = 0                                    # Keep N daily backups per DB
weekly = 0                                   # Keep N weekly backups per DB
monthly = 0                                  # Keep N monthly backups per DB

[backups.volumes]
enabled = false                              # Enable S3 volume archives (see backups.md)
schedule = "daily"                          # "hourly", "daily", "weekly", "monthly"
time = ""                                   # "HH:MM" server local time (empty: preset default)
max_concurrency = 2                          # Volumes archived in parallel

# =============================================================================
# IMAGES
# =============================================================================
[images]
# Defaults: docker.io/registry-1.docker.io, ghcr.io, quay.io, Gordon registry.
# Add private or other registries as exact hostname+port entries.
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
| `server.http.read_header_timeout` | `"10s"` | Registry servers: max time to read request headers (env `GORDON_SERVER_HTTP_READ_HEADER_TIMEOUT`) |
| `server.http.read_timeout` | `"5m"` | Registry servers: max time to read a whole request (env `GORDON_SERVER_HTTP_READ_TIMEOUT`) |
| `server.http.write_timeout` | `"5m"` | Registry servers: max time to write a whole response (env `GORDON_SERVER_HTTP_WRITE_TIMEOUT`) |
| `server.http.idle_timeout` | `"120s"` | Registry servers: keep-alive idle timeout (env `GORDON_SERVER_HTTP_IDLE_TIMEOUT`) |
| `traffic.http.read_header_timeout` | `"5s"` | Entrypoint HTTP servers: max time to read request headers (env `GORDON_TRAFFIC_HTTP_READ_HEADER_TIMEOUT`) |
| `traffic.http.idle_timeout` | `"30s"` | Entrypoint HTTP servers: keep-alive idle timeout (env `GORDON_TRAFFIC_HTTP_IDLE_TIMEOUT`) |
| `traffic.http.transfer_idle_timeout` | `"2m"` | Entrypoint HTTP servers: inactivity timeout for request bodies and responses (env `GORDON_TRAFFIC_HTTP_TRANSFER_IDLE_TIMEOUT`) |
| `traffic.handover.max_hold` | `"90s"` | Upper bound on how long traffic is held while a stateful service is replaced; `"0s"` disables holding (env `GORDON_TRAFFIC_HANDOVER_MAX_HOLD`, read at startup) |
| `traffic.handover.max_held_requests` | `1024` | Maximum HTTP requests held per host during a handover (env `GORDON_TRAFFIC_HANDOVER_MAX_HELD_REQUESTS`, read at startup) |
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
| `auth.enabled` | `true` | Enable authentication; when `false`, run local-only mode (loopback-only `/v2/*`, TCP `/admin/*` not registered, owner-only admin socket for local `gordon apps`) |
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
| `telemetry.logs` | `true` | Export Gordon, proxy access, and app container logs to OTLP |
| `telemetry.trace_sample_rate` | `1.0` | Fraction of traces to sample (0.0–1.0) |
| `containers.security_profile` | `"compat"` | Runtime hardening profile: `compat` preserves existing behavior, `strict` enables read-only rootfs and narrower capabilities |
| `network_isolation.enabled` | `true` | Network isolation enabled |
| `network_isolation.network_prefix` | `"gordon"` | Network prefix |
| `network_isolation.internal` | `false` | Create Docker internal networks without direct external egress |
| `volumes.auto_create` | `true` | Auto-create volumes |
| `volumes.prefix` | `"gordon"` | Volume prefix |
| `volumes.preserve` | `true` | Keep volumes |
| `app_mounts.<name>.source` | none | Required host source of a named administrative bind; absolute and normalized. The only host path that mount may come from |
| `app_mounts.<name>.allowed_apps` | none | Required exact, non-empty app allowlist for this mount (no wildcard or empty-means-all form) |
| `app_mounts.<name>.allowed_services` | none | Required exact, non-empty service allowlist for this mount |
| `app_mounts.<name>.read_only` | `false` | Force every bind resolved under this policy read-only; a manifest bind can never weaken it |
| `app_mounts.<name>.root` | parent of `source` | Optional administrative boundary the resolved source must stay under |
| `app_devices.<name>.cdi` | none | Required explicit, non-empty CDI device IDs granted under this logical name (no `=all` aggregate form) |
| `app_devices.<name>.allowed_apps` | none | Required exact, non-empty app allowlist for this grant (no wildcard or empty-means-all form) |
| `app_devices.<name>.allowed_services` | none | Required exact, non-empty service allowlist for this grant |
| `app_capabilities.<name>.add` | none | Required explicit, non-empty Linux capability names (no `CAP_` prefix) added on top of Gordon's defaults. Only the documented safe set is accepted; `ALL` and `SYS_ADMIN` are rejected |
| `app_capabilities.<name>.allowed_apps` | none | Required exact, non-empty app allowlist for this grant |
| `app_capabilities.<name>.allowed_services` | none | Required exact, non-empty service allowlist for this grant |
| `services[].name` | none | Standalone service name used by `service:<service>:<port-name>` traffic refs |
| `services[].image` | none | Container image for enabled standalone services |
| `services[].enabled` | `false` | Whether Gordon creates, starts, and reconciles the service container |
| `services[].env` | `[]` | Inline `KEY=value` environment entries |
| `services[].env_file` | `""` | Env file loaded before inline entries |
| `services[].ports[].name` | none | Port name used by traffic service refs |
| `services[].ports[].container` | none | Container port number |
| `services[].ports[].protocol` | none | `tcp` or `udp` |
| `services[].ports[].publish` | `""` | Host-side bind address, usually loopback, that the traffic manager dials |
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
| `backups.databases.enabled` | `false` | PostgreSQL backups disabled |
| `backups.databases.schedule` | `"daily"` | `hourly`, `daily`, `weekly`, or `monthly` |
| `backups.databases.time` | `""` | `HH:MM` server local time; empty uses the preset default |
| `backups.databases.storage_dir` | `""` | Uses `{server.data_dir}/backups` when empty |
| `backups.databases.retention.hourly` | `0` | Keep no hourly backups by default |
| `backups.databases.retention.daily` | `0` | Keep no daily backups by default (recommend `7`) |
| `backups.databases.retention.weekly` | `0` | Keep no weekly backups by default |
| `backups.databases.retention.monthly` | `0` | Keep no monthly backups by default |
| `backups.volumes.enabled` | `false` | S3 volume archives disabled |
| `backups.volumes.schedule` | `"daily"` | `hourly`, `daily`, `weekly`, or `monthly` |
| `backups.volumes.time` | `""` | `HH:MM` server local time; empty uses the preset default |
| `backups.volumes.max_concurrency` | `2` | Volumes archived in parallel |
| `backups.volumes.retention.keep` | `14` | Archives kept per app + volume |
| `images.allowed_registries` | `[]` | Additional exact hostname+port entries. Defaults allow Docker Hub (`docker.io` and `registry-1.docker.io`), `ghcr.io`, `quay.io`, and Gordon's registry. This allowlist does not configure registry credentials and is not DNS/IP or runtime egress enforcement. |
| `images.require_digest` | `false` | Require valid SHA-256 digest-pinned references for every registry, including Gordon |
| `images.prune.enabled` | `false` | Scheduled image cleanup disabled |
| `images.prune.schedule` | `"daily"` | Cleanup schedule preset |
| `images.prune.keep_last` | `3` | Number of recent tags kept per repository |

Note: for all `backups.databases.retention.*` keys, `0` means keep no backups for that retention tier. The top-level `backups.enabled`, `backups.schedule`, `backups.storage_dir`, and `backups.retention.*` keys are aliases of the `backups.databases.*` keys.

Every volume backup key, including compression, the helper image, and the `backups.volumes.s3.*` settings, is listed in [Backups](./backups.md).

## App Manifest HTTP Interfaces

`[[services.<name>.http]]` declares HTTP interfaces in a standalone app manifest (see [App Manifest](./apps.md)).

| Key | Values | Default | Description |
|-----|--------|---------|-------------|
| `services.<name>.http[].visibility` | `"public"`, `"internal"` | `"public"` | `public` is proxied by host and may terminate TLS; `internal` is reachable only from the app's private network |
| `services.<name>.http[].host` | hostname | none | Required for public interfaces; must be absent for `visibility = "internal"` |
| `services.<name>.http[].port` | integer | none | Required container port for every interface |
| `services.<name>.http[].tls` | `"auto"`, `"always"`, `"never"` | `"auto"` | Public interfaces only; `visibility = "internal"` rejects any declared value |

`visibility = "internal"` creates no proxy route, host reservation, certificate target, or host port publication. A container port declared by both an internal HTTP interface and an externally backed interface (public HTTP or TCP) is rejected, as are duplicate internal HTTP ports.

## Environment Variables

All configuration options can be set via environment variables using the pattern:

```
GORDON_<SECTION>_<KEY>=value
```

Examples:
```bash
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

## CI Identity and Set-image Policies

CI identity and set-image configuration is validated at startup and reload.
`server.ci_domain` enables the restricted CI router. See
[Trusted Issuers](./auth.md#oidc-trusted-issuers) for key defaults and cache
behavior, and [Set-image policies](./set-image.md) for grant semantics.

```toml
[[trusted_issuers]]
issuer = "https://issuer.example.com"          # Exact issuer URL
audience = "https://ci.example.com"            # Required, exact audience
# jwks_uri = "https://issuer.example.com/keys" # Optional override
max_token_lifetime = "1h"                     # Default; positive duration
clock_skew = "60s"                            # Default; non-negative duration
allow_private_network = false                # Permit private issuer network access

[[set_image_policies]]
issuer = "https://issuer.example.com"          # Declared issuer, or "gordon"
subjects = ["repo:acme/web:production"]        # Non-empty exact subjects
app = "web"
service = "web"
repository = "web"                            # Registry repository path
```

Issuer and JWKS URLs must be absolute HTTPS URLs without userinfo, query,
or fragment. Unknown issuer/policy keys, undeclared policy issuers, invalid
app/service/repository names, wildcards (`*`), and duplicate
`(issuer, subject, app, service)` grants are rejected. Duplicate issuer
declarations are also rejected. `ci_domain` requires `auth.enabled` and must
not equal `gordon_domain`, a legacy registry domain, or a served app host.
`ci_domain` must be a public DNS hostname, not an IP, URL, wildcard, or
host with a port or trailing dot (matching route-domain validation).
Surrounding whitespace and case are ignored in host comparisons; collision
checks also ignore a trailing dot on the compared management/app host.
Issuer URLs, unlike routing hosts, are byte-exact: case, trailing slashes,
Unicode hosts, and explicit ports are preserved. Subjects are also exact;
whitespace and Unicode are not normalized.
A rejected reload retains the previous identity and policy snapshot.

## Related

- [Configuration Overview](./index.md)
- [Authentication](./auth.md)
- [Telemetry](./telemetry.md)
- [Network Isolation](./network-isolation.md)
- [Volumes](./volumes.md)
- [App Manifest](./apps.md)
- [Standalone Services](./services.md)
- [Images](./images.md)
