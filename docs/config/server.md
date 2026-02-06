# Server Configuration

Core server settings for Gordon.

## Configuration

```toml
[server]
port = 8080                              # HTTP proxy port
registry_port = 5000                     # Container registry port
gordon_domain = "gordon.mydomain.com"    # Gordon domain (required)
# data_dir = "~/.gordon"                 # Data storage directory (default)
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `port` | int | `80` | HTTP proxy port for routing traffic to containers |
| `registry_port` | int | `5000` | Docker registry port for image push/pull |
| `gordon_domain` | string | **required** | Domain for Gordon (registry + admin API) |
| `registry_domain` | string | - | Deprecated alias for `gordon_domain` |
| `data_dir` | string | `~/.gordon` | Directory for registry data, logs, and env files |
| `max_proxy_body_size` | string | `"512MB"` | Maximum request body size for proxied requests |
| `max_blob_chunk_size` | string | `"95MB"` | Maximum request body size for a single registry blob upload chunk |

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
- Docker/Podman may send a full layer in one request, so set this high enough for your base images
- Default (`95MB`) is compatible with Cloudflare and most CDN proxies; increase if pushing very large layers directly

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
