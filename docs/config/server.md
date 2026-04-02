# Server Configuration

Core server settings for Gordon.

## Configuration

```toml
[server]
port = 8088                              # HTTP proxy port
registry_port = 5000                     # Container registry port
tls_port = 8443                          # HTTPS listener port (0 = disabled)
# tls_cert_file = ""                       # Optional: PEM certificate for static TLS
# tls_key_file = ""                        # Optional: PEM key for static TLS
# force_https_redirect = false             # Redirect all HTTP traffic to HTTPS
gordon_domain = "gordon.mydomain.com"    # Gordon domain (required)
# data_dir = "~/.gordon"                 # Data storage directory (default)
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `port` | int | `8088` | HTTP proxy port for routing traffic to containers |
| `registry_port` | int | `5000` | Docker registry port for image push/pull |
| `tls_port` | int | `8443` | HTTPS listener port with internal CA. Set to `0` to disable TLS entirely |
| `tls_cert_file` | string | `""` | Path to PEM certificate file. When set (with `tls_key_file`), this cert is served for matching SNI domains; unmatched domains fall through to the internal CA |
| `tls_key_file` | string | `""` | Path to PEM private key file. Must be set together with `tls_cert_file` |
| `force_https_redirect` | bool | `false` | Redirect all HTTP requests to the HTTPS port. For direct-access setups without a TLS-terminating proxy |
| `gordon_domain` | string | **required** | Domain for Gordon (registry + admin API) |
| `registry_domain` | string | - | Deprecated alias for `gordon_domain` |
| `data_dir` | string | `~/.gordon` | Directory for registry data, logs, and env files |
| `max_proxy_body_size` | string | `"512MB"` | Maximum request body size for proxied requests |
| `max_blob_chunk_size` | string | `"95MB"` | Maximum request body size for a single registry blob upload chunk |
| `registry_allowed_ips` | []string | `[]` | IPs or CIDR ranges allowed to access the registry (empty = allow all) |
| `proxy_allowed_ips` | []string | `[]` | IPs or CIDR ranges allowed to reach the proxy (empty = allow all) |
| `registry_listen_address` | string | `""` | Bind address for registry (empty = all interfaces) |

## Port Configuration

### Proxy Port

The `port` setting controls where Gordon listens for HTTP traffic:

```toml
[server]
port = 8080  # Use 8080 for rootless containers with port forwarding
```

For rootless containers, you'll typically use a high port and configure firewall port forwarding:

```bash
# Forward port 80 to 8080
sudo firewall-cmd --permanent --add-forward-port=port=80:proto=tcp:toport=8080
```

### Internal CA and TLS

Gordon includes an internal certificate authority that issues on-demand TLS certificates for proxied domains. The HTTPS listener is enabled by default on `tls_port` (8443).

```toml
[server]
tls_port = 8443   # Default. Set to 0 to disable TLS entirely.
```

With TLS enabled:
- Gordon runs an internal CA (root + intermediate) stored in `{data_dir}/pki/`
- Leaf certificates are issued on-demand per domain (SNI-based) and cached in memory
- The intermediate CA auto-renews before expiry
- An onboarding page at `https://<gordon-host>:<tls_port>/ca` lets clients download the root CA certificate
- The root CA is stable across restarts (generated once, persisted to disk)

#### Custom certificates

To use your own certificate (e.g. from Tailscale, Let's Encrypt, or a corporate CA) alongside the internal CA:

```toml
[server]
tls_cert_file = "/etc/gordon/tls/cert.pem"
tls_key_file = "/etc/gordon/tls/key.pem"
```

The static certificate is served for SNI-matching domains (including wildcards). All other domains get on-demand certs from the internal CA.

#### Direct HTTP Onboarding

When `tls_port` is non-zero, direct HTTP clients (those not arriving through a trusted proxy) are restricted to CA onboarding paths only:

- `/`, `/ca`, `/ca.crt`, `/ca.mobileconfig` — serve the onboarding page and CA certificate downloads
- `/.well-known/acme-challenge/*` — returns `404 Not Found` (reserved for future ACME support)
- All other paths return `403 Forbidden`

This lets new clients discover and trust the internal CA over plain HTTP without exposing the full application. Trusted proxy traffic (e.g. from Cloudflare) continues through the normal HTTP proxy path unaffected.

The onboarding page is also available on HTTPS at `/ca`.

#### HTTP to HTTPS redirect

When clients connect directly (no Cloudflare or reverse proxy in front), enable `force_https_redirect` to redirect all HTTP traffic to the HTTPS port. Direct HTTP onboarding paths are still served even with this flag enabled.

```toml
[server]
force_https_redirect = true
```

When `proxy_allowed_ips` is configured, non-proxy clients are redirected automatically even without this flag — trusted proxy IPs pass through to serve Cloudflare-proxied traffic.

#### Disabling TLS

For setups where TLS is handled entirely by an external proxy (Cloudflare, nginx, etc.):

```toml
[server]
tls_port = 0   # No HTTPS listener, no internal CA generated
```

#### Firewall port forwarding

For rootless user services, forward 443 to the high port at the firewall:

```bash
# Forward 443 -> 8443
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=8443
sudo firewall-cmd --reload
```

With firewalld and Tailscale:

```bash
# Verify tailscale0 is in trusted zone
sudo firewall-cmd --get-zone-of-interface=tailscale0

# If needed, place tailscale0 in trusted
sudo firewall-cmd --permanent --zone=trusted --add-interface=tailscale0
sudo firewall-cmd --reload

# Forward 443 -> 8443 permanently (IPv4 + IPv6)
sudo firewall-cmd --permanent --zone=trusted --add-rich-rule='rule family=ipv4 forward-port port=443 protocol=tcp to-port=8443'
sudo firewall-cmd --permanent --zone=trusted --add-rich-rule='rule family=ipv6 forward-port port=443 protocol=tcp to-port=8443'
sudo firewall-cmd --reload
```

### Registry Port

The `registry_port` setting controls where Gordon accepts image pushes:

```toml
[server]
registry_port = 5000
```

Docker/Podman clients push to this port:

```bash
docker push registry.mydomain.com:5000/myapp:latest
```

If the registry is served over plain HTTP (no TLS), clients must be configured to allow insecure access. See [Troubleshooting: insecure registry](../reference/troubleshooting.md#server-gave-http-response-to-https-client) for Docker and Podman instructions.

## Gordon Domain

The `gordon_domain` is required and must match your DNS configuration:

```toml
[server]
gordon_domain = "gordon.mydomain.com"
```

This domain is used for:
- Docker login and image push/pull operations
- Admin API access (`/admin/*` endpoints)
- CLI remote targeting (`gordon routes --remote https://gordon.mydomain.com`)
- Authentication endpoints (`/auth/*`)

When requests arrive on the proxy port with this domain as the Host header, Gordon routes them to the backend services (registry and admin API).

Security note:
- Direct access to `/admin/*` on `registry_port` is blocked for non-loopback clients.
- Admin API traffic should go through the main proxy listener (`server.port`/`server.tls_port`).

> **Note:** `registry_domain` is supported as a deprecated alias for backwards compatibility.

## Data Directory

The `data_dir` setting controls where Gordon stores data:

```toml
[server]
data_dir = "~/.gordon"  # Default for user installations
```

### Directory Structure

```
~/.gordon/
├── registry/           # Container images and manifests
│   ├── blobs/
│   └── manifests/
├── env/                # Environment files per domain
│   ├── app_mydomain_com.env
│   └── api_mydomain_com.env
├── logs/               # Application and container logs
│   ├── gordon.log
│   ├── proxy.log
│   └── containers/
└── secrets/            # Secrets (unsafe backend only)
```

### Permissions

 Gordon creates directories with secure permissions:
 - Directories: `0700` (owner only)
 - Files: `0600` (owner only)

 ## Maximum Proxy Body Size

 The `max_proxy_body_size` setting limits the size of request bodies that Gordon will forward to backend containers:

 ```toml
 [server]
 max_proxy_body_size = "512MB"  # Default
 ```

 **Purpose:** Prevents resource exhaustion attacks from extremely large uploads through the proxy.

 **Supported units:** B, KB, MB, GB, TB (case-insensitive, decimal/1024-based)

 **Examples:**
 ```toml
 max_proxy_body_size = "100MB"   # Stricter limit
 max_proxy_body_size = "1GB"     # Allow larger uploads
 max_proxy_body_size = "0"       # No limit (not recommended)
 ```

 **Behavior:**
 - When a request exceeds the limit, Gordon returns `413 Request Entity Too Large`
 - The limit is applied before the request reaches the backend container
 - Uploads in progress are terminated when the limit is exceeded

 **Use cases:**
 - File upload services: increase to `1GB` or higher
 - CI/CD artifact uploads: adjust based on your largest artifacts
 - Tight security: reduce to `100MB` or lower
 - Default (512MB): suitable for most applications

 > **Warning:** Setting this to `0` (no limit) allows unlimited upload sizes, which can lead to disk exhaustion and DoS vulnerabilities. Always set a reasonable limit for production deployments.

## Maximum Registry Blob Chunk Size

The `max_blob_chunk_size` setting limits the size of a single blob upload request to the registry (`PATCH`/`PUT` under `/v2/*/blobs/uploads/*`):

```toml
[server]
max_blob_chunk_size = "95MB"  # Default
```

**Purpose:** Prevents excessive memory usage during blob uploads while allowing large real-world image layers.

**Supported units:** B, KB, MB, GB, TB (case-insensitive, decimal/1024-based)

**Examples:**

```toml
max_blob_chunk_size = "256MB"  # Stricter limit
max_blob_chunk_size = "1GB"    # Allow larger layers
```

**Behavior:**
- When a blob chunk exceeds the limit, Gordon returns `413 Request Entity Too Large`
- Gordon CLI uploads image layers in 50MB chunks by default, and this setting caps the maximum chunk accepted per request
- Keep this below Cloudflare's 100MB per-request limit when operating behind Cloudflare; the default (`95MB`) is fine
- Increase it only if you expect larger direct registry uploads from other clients

## Registry IP Allowlist

The `registry_allowed_ips` setting restricts registry access to specific IPs or IP ranges. Accepts both CIDR notation (`100.64.0.0/10`) and individual IPs (`203.0.113.50`). When set, only requests from listed addresses (plus localhost) can reach registry and auth endpoints. An empty list allows all traffic (default).

```toml
[server]
registry_allowed_ips = [
  "100.64.0.0/10",   # Tailscale CGNAT range
  "203.0.113.50",    # single IP (treated as /32)
]
```

**Behavior:**
- Single IPs are automatically converted to `/32` (IPv4) or `/128` (IPv6)
- Denied requests receive `403 Forbidden`
- Localhost (`127.0.0.0/8`, `::1`) is always allowed so Gordon can pull from its own registry
- Respects `trusted_proxies` for accurate client IP extraction behind reverse proxies
- Applied before rate limiting and authentication
- Changes require a Gordon server restart; not picked up by config hot-reload

**Use cases:**
- Restrict registry to Tailscale network: `["100.64.0.0/10"]`
- Restrict to private subnets: `["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]`
- Allow a specific CI server: `["100.64.0.0/10", "203.0.113.50"]`

Via environment variable:

```bash
GORDON_SERVER_REGISTRY_ALLOWED_IPS=100.64.0.0/10
```

## Proxy Origin IP Allowlist

The `proxy_allowed_ips` setting restricts which IPs can reach the proxy server. This prevents direct-to-origin attacks that bypass CDN protections (e.g. Cloudflare WAF, DDoS mitigation). When set, only connections from listed CIDRs (plus localhost) are accepted.

```toml
[server]
# Only accept proxy traffic from Cloudflare edge IPs
proxy_allowed_ips = [
  "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
  "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
  "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
  "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
  "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
]
```

**Behavior:**
- Checks the direct connection IP (`RemoteAddr`), not `X-Forwarded-For`
- Localhost is always allowed
- Denied requests receive `403 Forbidden`
- An empty list allows all traffic (default)

**Important:** Cloudflare publishes their IP ranges at [cloudflare.com/ips](https://www.cloudflare.com/ips/). Keep your allowlist updated when Cloudflare adds new ranges.

## Registry Listen Address

The `registry_listen_address` controls which interface the registry server binds to. By default it binds to all interfaces. Set to `127.0.0.1` to restrict registry access to localhost only, preventing containers from reaching the registry via the Docker bridge gateway.

```toml
[server]
registry_listen_address = "127.0.0.1"  # Loopback only
```

## HSTS

HSTS headers are sent automatically on the HTTPS listener (when `r.TLS != nil`). When Gordon runs behind a TLS-terminating proxy (Cloudflare, Tailscale) and only receives plain HTTP, HSTS is not sent — the edge proxy is expected to handle it.

## Examples

### Development Configuration

```toml
[server]
port = 8080
registry_port = 5000
gordon_domain = "gordon.local"
data_dir = "./dev-data"
```

### Production Configuration

```toml
[server]
port = 8080
registry_port = 5000
gordon_domain = "gordon.company.com"
data_dir = "/var/lib/gordon"
```

### Rootless Container Setup

```toml
[server]
port = 8080      # High port for rootless
registry_port = 5000
gordon_domain = "gordon.mydomain.com"
```

With firewall port forwarding:
```bash
sudo firewall-cmd --permanent --add-forward-port=port=80:proto=tcp:toport=8080
sudo firewall-cmd --reload
```

## Related

- [Configuration Overview](./index.md)
- [Installation Guide](../installation.md)
- [Routes Configuration](./routes.md)
