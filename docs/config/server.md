# Server Configuration

Core server settings for Gordon.

## Configuration

```toml
[server]
port = 8080                              # HTTP proxy port
registry_port = 5000                     # Container registry port
registry_domain = "registry.mydomain.com" # Registry domain (required)
# data_dir = "~/.gordon"                 # Data storage directory (default)
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `port` | int | `80` | HTTP proxy port for routing traffic to containers |
| `registry_port` | int | `5000` | Docker registry port for image push/pull |
| `registry_domain` | string | **required** | Domain for the container registry |
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

## Registry Domain

The `registry_domain` is required and must match your DNS configuration:

```toml
[server]
registry_domain = "registry.mydomain.com"
```

This domain is used for:
- Docker login authentication
- Image push/pull operations
- Internal registry references

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
registry_domain = "registry.local"
data_dir = "./dev-data"
```

### Production Configuration

```toml
[server]
port = 8080
registry_port = 5000
registry_domain = "registry.company.com"
data_dir = "/var/lib/gordon"
```

### Rootless Container Setup

```toml
[server]
port = 8080      # High port for rootless
registry_port = 5000
registry_domain = "registry.mydomain.com"
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
