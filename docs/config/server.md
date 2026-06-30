# Server Configuration

Core server settings for Gordon.

## Configuration

```toml
[server]
registry_port = 5000                     # Container registry port
gordon_domain = "gordon.mydomain.com"    # Gordon domain (required)
# tls_cert_file = ""                       # Optional: PEM certificate for static TLS
# tls_key_file = ""                        # Optional: PEM key for static TLS
# force_https_redirect = false             # Redirect cleartext HTTP requests to HTTPS
# legacy_registry_domains = ["registry.example.com:5000"]
# data_dir = "~/.gordon"                 # Data storage directory (default)
max_blob_chunk_size = "95MB"             # Max registry blob upload chunk
max_blob_size = "1GB"                    # Max cumulative registry blob/layer upload

[entrypoints.edge]
address = ":443"                         # Deployment-selected public TCP socket
protocol = "smart_tcp"
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `registry_port` | int | `5000` | Docker registry port for image push/pull |
| `tls_cert_file` | string | `""` | Path to PEM certificate file. When set with `tls_key_file`, matching SNI domains use this static cert before ACME or internal CA certs |
| `tls_key_file` | string | `""` | Path to PEM private key file. Must be set together with `tls_cert_file` |
| `force_https_redirect` | bool | `false` | Redirect cleartext HTTP requests to HTTPS on HTTP-capable edge entrypoints |
| `gordon_domain` | string | **required** | Domain for Gordon (registry + admin API) |
| `registry_domain` | string | - | Deprecated migration key. Set `gordon_domain` instead. |
| `legacy_registry_domains` | []string | `[]` | Additional Gordon registry hosts treated as aliases during staged migration. See [Upgrading: Staged Registry Host Rename](../upgrading.md#staged-registry-host-rename). |
| `data_dir` | string | `~/.gordon` | Directory for registry data, logs, and env files |
| `max_proxy_body_size` | string | `"512MB"` | Maximum request body size for proxied requests |
| `max_blob_chunk_size` | string | `"95MB"` | Maximum request body size for a single registry blob upload chunk |
| `max_blob_size` | string | `"1GB"` | Maximum cumulative size for one registry blob/layer upload |
| `registry_allowed_ips` | []string | `[]` | IPs or CIDR ranges allowed to access the registry (empty = allow all) |
| `proxy_allowed_ips` | []string | `[]` | IPs or CIDR ranges allowed to reach HTTP proxy paths (empty = allow all) |
| `registry_listen_address` | string | `""` | Bind address for registry (empty = all interfaces) |

Public application traffic is configured under `[entrypoints]`. The standard edge listener is a `smart_tcp` socket:

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
```

`entrypoints.edge.address` is not an HTTP or HTTPS port. It is the TCP address Gordon sniffs for cleartext HTTP, h2c, TLS passthrough, normal HTTPS fallback, or explicit raw TCP fallback. Gordon does not provide a built-in default public port; choose `:443`, a high port such as `:9000`, or container/firewall mappings to match your deployment.

## Internal CA and TLS

Gordon includes an internal certificate authority that issues on-demand TLS certificates for proxied domains served through HTTPS fallback on a TLS-capable entrypoint such as `smart_tcp`.

With TLS-capable edge traffic:
- Gordon runs an internal CA (root + intermediate) stored in `{data_dir}/pki/`
- Leaf certificates are issued on-demand per domain (SNI-based) and cached in memory
- The intermediate CA auto-renews before expiry
- An onboarding page at `https://<gordon-host>/.well-known/gordon/` lets clients download the root CA certificate
- The root CA is stable across restarts (generated once, persisted to disk)

#### Custom certificates

To use your own certificate (e.g. from Tailscale, Let's Encrypt, or a corporate CA) alongside ACME/internal CA fallback:

```toml
[server]
tls_cert_file = "/etc/gordon/tls/cert.pem"
tls_key_file = "/etc/gordon/tls/key.pem"
```

Certificate priority for normal HTTPS fallback is: static certificates first, then public ACME certificates, then Gordon's internal CA.

#### Public ACME certificates

Gordon can obtain public certificates through Let's Encrypt-compatible ACME when `[tls.acme]` is enabled. Normal HTTPS fallback must be available on a TLS-capable entrypoint.

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"

[dns]
resolvers = ["1.1.1.1:53", "8.8.8.8:53"]
propagation_timeout = "5m"
polling_interval = "5s"

[tls.acme]
enabled = true
email = "admin@example.com"
challenge = "auto"       # auto, http-01, or cloudflare-dns-01
obtain_batch_size = 1    # maximum new certificate orders per reconcile run
```

Challenge behavior:
- `cloudflare-dns-01` uses a Cloudflare API token from `pass`, `GORDON_CLOUDFLARE_API_TOKEN_FILE`, or `GORDON_CLOUDFLARE_API_TOKEN`; it does not need a special public port 80 edge.
- `http-01` serves `/.well-known/acme-challenge/<token>` through Gordon's HTTP handler and requires an HTTP-capable `smart_tcp` entrypoint reachable on external port 80 for each hostname being validated.
- `auto` selects Cloudflare DNS-01 when a token is available, otherwise falls back to HTTP-01.
- `tls-alpn-01` is not supported.

HTTP-01 requires public access to external port 80 for each hostname being validated. If you use Cloudflare in DNS-only/gray-cloud mode, your firewall/NAT must allow direct public traffic to the HTTP-capable smart TCP entrypoint. A firewall rule that only allows Cloudflare source IPs on port 80 is compatible with orange-cloud proxying, but it blocks gray-cloud HTTP-01 validation; use DNS-01 or temporarily open port 80 for direct validation.

Gordon limits new ACME certificate orders to `obtain_batch_size` per reconcile run (default `1`) so enabling ACME on an existing multi-route server does not burst through every route and hit Let's Encrypt rate limits. Later reloads, restarts, or other explicit reconcile runs continue issuing remaining certificates.

If the initial ACME reconcile fails (e.g. due to a transient network error or misconfiguration), Gordon logs the failure and keeps serving any existing certificates. The renewal loop retries reconcile work periodically, so missing certificates self-heal after the underlying issue is fixed without requiring a restart.

For Cloudflare Full/Strict, Cloudflare terminates browser TLS at the edge and connects to Gordon over HTTPS. A public ACME certificate served by Gordon is the preferred origin certificate because Cloudflare Strict can validate it without custom origin trust. Cloudflare Flexible mode (HTTPS at the edge, HTTP to Gordon) is not end-to-end HTTPS.

Static certificates have priority, then public ACME certificates, then Gordon's internal CA fallback. Gordon uses the `go-acme/lego` ACME client, including its DNS-provider support for Cloudflare DNS-01.

DNS-01 uses the Cloudflare API to create TXT records, then checks public DNS visibility through `[dns].resolvers`. These resolvers are recursive resolvers (for example Cloudflare DNS or Google DNS), not the authoritative DNS provider. A domain can be hosted at Cloudflare while Gordon verifies propagation through Google DNS.

The defaults avoid relying on host-local DNS. This matters on hosts using Tailscale MagicDNS, split-horizon corporate DNS, or Pi-hole, where `/etc/resolv.conf` may not reflect public DNS visibility as Let's Encrypt sees it.

Because Gordon's ACME challenge mode is global, `cloudflare-dns-01` requires a Cloudflare token that can read zones and edit DNS records for every zone used by configured HTTPS routes. If that is not desirable, use `http-01` until Gordon supports per-route or per-zone challenge policy.

#### Direct HTTP Onboarding

When Gordon is serving TLS-capable edge traffic, direct cleartext HTTP clients (those not arriving through a trusted proxy) are restricted to CA onboarding paths only:

- `/.well-known/gordon/`, `/.well-known/gordon/ca`, `/.well-known/gordon/ca.crt`, `/.well-known/gordon/ca.mobileconfig` — serve the onboarding page and CA certificate downloads
- `/.well-known/acme-challenge/*` — serves ACME HTTP-01 challenges when public ACME HTTP-01 is enabled, otherwise returns `404 Not Found`
- All other paths return `403 Forbidden`

This lets new clients discover and trust the internal CA over plain HTTP without exposing the full application. Trusted proxy traffic (e.g. from Cloudflare) continues through the normal HTTP proxy path unaffected.

On HTTPS, onboarding paths are served only on `server.gordon_domain`; app hosts can use their own routes without Gordon intercepting them. If `gordon_domain` is empty, HTTPS onboarding paths are not served at all to avoid intercepting app hosts.

#### HTTP to HTTPS redirect

When clients connect directly (no Cloudflare or reverse proxy in front), enable `force_https_redirect` to redirect cleartext HTTP traffic to HTTPS on the same smart TCP edge.

```toml
[server]
force_https_redirect = true
```

When `proxy_allowed_ips` is configured, non-proxy clients are redirected automatically even without this flag — trusted proxy IPs pass through to serve Cloudflare-proxied traffic.

#### Firewall and Container Port Mapping

Choose the bind address in `[entrypoints.edge]` and map external ports to it as needed. For example, a rootless service can bind a high port while the firewall exposes 443:

```toml
[entrypoints.edge]
address = ":9000"
protocol = "smart_tcp"
```

```bash
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=9000
sudo firewall-cmd --reload
```

`:443`, `:9000`, and container `-p 443:9000` mappings are deployment choices, not protocol modes.

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

> **Warning:** If you are upgrading an older config, copy `server.registry_domain` to `server.gordon_domain` before restarting.

For a staged rename, set `gordon_domain` to the new public host and add any old Gordon registry hosts that clients still use to `legacy_registry_domains` (including `host:port` forms). Gordon treats those entries as registry aliases during image matching and internal pulls, then writes canonical refs back to `gordon_domain`. Remote CLI and admin API traffic should use `gordon_domain`.

Without this migration, a Host/remote-target mismatch can break routing or remote CLI token exchange.

When requests arrive on an HTTP-capable edge entrypoint with this domain as the Host header, Gordon routes them to the backend services (registry and admin API).

Security note:
- Direct access to `/admin/*` on `registry_port` is blocked for non-loopback clients.
- Admin API traffic should go through an HTTP-capable edge entrypoint such as `entrypoints.edge` with `protocol = "smart_tcp"`.

> **Note:** `registry_domain` is a deprecated migration key; use `gordon_domain` for new and upgraded configs.

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

## Maximum Registry Blob Size

The `max_blob_size` setting limits the cumulative size of one blob/layer upload across all chunks:

```toml
[server]
max_blob_size = "1GB"  # Default
```

**Purpose:** Prevents disk exhaustion from multi-chunk uploads where each individual chunk is below `max_blob_chunk_size` but the total blob grows without bound.

**Behavior:**
- Gordon tracks the current upload size and rejects appends that would exceed the limit.
- Rejected uploads return an OCI-compatible `413` size error.
- Failed uploads are cleaned up so temporary blob data does not accumulate indefinitely.

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

HSTS headers are sent automatically on TLS requests (when `r.TLS != nil`). When Gordon runs behind a TLS-terminating proxy (Cloudflare, Tailscale) and only receives plain HTTP, HSTS is not sent — the edge proxy is expected to handle it.

## Examples

### Development Configuration

```toml
[server]
registry_port = 5000
gordon_domain = "gordon.local"
data_dir = "./dev-data"

[entrypoints.edge]
address = ":9000"
protocol = "smart_tcp"
```

### Production Configuration

```toml
[server]
registry_port = 5000
gordon_domain = "gordon.company.com"
data_dir = "/var/lib/gordon"

[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
```

### Rootless Container Setup

```toml
[server]
registry_port = 5000
gordon_domain = "gordon.mydomain.com"

[entrypoints.edge]
address = ":9000"      # High port for rootless
protocol = "smart_tcp"
```

With firewall port forwarding:

```bash
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=9000
sudo firewall-cmd --reload
```

## Related

- [Configuration Overview](./index.md)
- [Installation Guide](../installation.md)
- [Routes Configuration](./routes.md)
