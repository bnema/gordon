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
