# Configuring Gordon in a Container

This document explains how to configure Gordon when running inside a Docker container using environment variables.

## Container Detection

Gordon automatically detects when it's running inside a container by checking for the presence of `/.iscontainer` or `/.dockerenv` files. When Gordon is running in a container, it:

1. Uses the root directory (`/`) as its configuration directory
2. Looks for a directly mounted `/config.yml` file
3. Reads configuration from environment variables provided through Docker or docker-compose

## Using docker-compose for Configuration

The recommended way to configure Gordon in a container is through a `docker-compose.yml` file. Here's an example:

```yaml
version: '3'

services:
  gordon:
    image: ghcr.io/bnema/gordon:latest
    container_name: gordon
    volumes:
      - ./data:/data
      - ./data/config.yml:/config.yml  # Mount config.yml directly at the root
      - /var/run/docker.sock:/var/run/docker.sock # Required for Docker management
    ports:
      - "8080:8080"  # HTTP API (Gordon main server)
      - "443:443"    # HTTPS for reverse proxy
      - "80:80"      # HTTP for reverse proxy
    environment:
      # General Configuration
      - GORDON_STORAGE_DIR=/data
      - GORDON_TOKEN=your_secure_token
      
      # HTTP Configuration  
      - GORDON_HTTP_PORT=8080       # API port must be different from proxy HTTP port
      - GORDON_HTTP_DOMAIN=example.com
      - GORDON_HTTP_SUBDOMAIN=gordon
      - GORDON_HTTP_HTTPS=true
      
      # Container Engine Configuration
      - GORDON_CONTAINER_SOCK=/var/run/docker.sock
      - GORDON_CONTAINER_NETWORK=gordon
      
      # Reverse Proxy Configuration
      - GORDON_PROXY_PORT=443       # HTTPS port for reverse proxy
      - GORDON_PROXY_HTTP_PORT=80   # HTTP port for reverse proxy
      - GORDON_PROXY_CERT_DIR=/certs
      - GORDON_PROXY_EMAIL=admin@example.com
      
      # Session Secret for security
      - SESSION_SECRET=replace_with_secure_random_string
      
      # Runtime Environment
      - RUN_ENV=prod
    restart: always
    networks:
      - gordon

networks:
  gordon:
    external: false
```

## Port Configuration

Gordon runs two HTTP servers:

1. **API Server**: Defaults to port 8080 in containers (configurable with `GORDON_HTTP_PORT`)
2. **Reverse Proxy**: Uses ports 80 (HTTP) and 443 (HTTPS) for the reverse proxy (configurable with `GORDON_PROXY_HTTP_PORT` and `GORDON_PROXY_PORT`)

⚠️ **Important**: The API server port (`GORDON_HTTP_PORT`) must be different from the reverse proxy HTTP port (`GORDON_PROXY_HTTP_PORT`) to avoid conflicts. By default, they are set to 8080 and 80 respectively.

## Using with Podman

Gordon now automatically detects Podman based on the socket path. You can use Podman in one of two ways:

### Method 1: Implicit Podman Detection (Recommended)

Simply mount the Podman socket and set the container socket path:

```yaml
volumes:
  - /run/user/1000/podman/podman.sock:/var/run/docker.sock
environment:
  - GORDON_CONTAINER_SOCK=/run/user/1000/podman/podman.sock
```

Gordon will automatically detect that this is a Podman socket and enable Podman support without requiring additional configuration.

### Method 2: Explicit Podman Configuration

Alternatively, you can explicitly enable Podman mode with:

```yaml
environment:
  - GORDON_CONTAINER_PODMAN=true
  - GORDON_CONTAINER_PODMAN_SOCK=/run/podman/podman.sock
  - GORDON_CONTAINER_SOCK=/run/podman/podman.sock
```

### Socket Path Auto-Detection

Gordon will automatically try different common socket paths if the specified path doesn't work:
- `/run/podman/podman.sock` (Standard path)
- `/var/run/podman/podman.sock` (Alternative standard path)
- `$HOME/.local/podman/podman.sock` (Some distros)
- `/run/user/1000/podman/podman.sock` (User-specific socket)
- `/var/run/docker.sock` (Docker fallback)

### Common Podman Issues

If you encounter errors like `no such file or directory` when accessing the Podman socket:

1. **Check socket permissions**: Make sure your user has permission to access the Podman socket
2. **Verify socket exists**: Run `ls -la /run/podman/podman.sock` to verify the socket exists
3. **Enable Podman socket service**: Run `systemctl --user enable --now podman.socket`

For rootless Podman, the socket might be at `/run/user/$(id -u)/podman/podman.sock`. Check with:
```bash
echo "/run/user/$(id -u)/podman/podman.sock"
```

## Config File Persistence

To ensure your configuration persists between container restarts, mount the `config.yml` file directly:

```yaml
volumes:
  - ./data/config.yml:/config.yml
```

This is important as Gordon looks for a file at `/config.yml` in the container.

## Available Environment Variables

Gordon supports the following environment variables for configuration:

### General Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `GORDON_STORAGE_DIR` | Directory for Gordon to store data | `~/.gordon` |
| `GORDON_TOKEN` | Authentication token | Empty |

### HTTP Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `GORDON_HTTP_PORT` | HTTP port for Gordon API | `8080` |
| `GORDON_HTTP_DOMAIN` | Domain name | `localhost` |
| `GORDON_HTTP_SUBDOMAIN` | Subdomain | Empty |
| `GORDON_HTTP_BACKEND_URL` | Backend URL | Empty |
| `GORDON_HTTP_HTTPS` | Enable HTTPS | `true` |

### Admin Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `GORDON_ADMIN_PATH` | Admin panel path | `/admin` |

### Container Engine Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `GORDON_CONTAINER_SOCK` | Docker socket path | `/var/run/docker.sock` |
| `GORDON_CONTAINER_PODMAN_SOCK` | Podman socket path | `/run/podman/podman.sock` |
| `GORDON_CONTAINER_PODMAN` | Use Podman instead of Docker | `true` |
| `GORDON_CONTAINER_NETWORK` | Container network | `gordon` |

### Reverse Proxy Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `GORDON_PROXY_PORT` | HTTPS port for reverse proxy | `443` |
| `GORDON_PROXY_HTTP_PORT` | HTTP port for reverse proxy | `80` |
| `GORDON_PROXY_CERT_DIR` | Directory for Let's Encrypt certificates | `/certs` |
| `GORDON_PROXY_AUTO_RENEW` | Auto-renew certificates | `true` |
| `GORDON_PROXY_RENEW_BEFORE` | Days before expiry to renew certificates | `30` |
| `GORDON_PROXY_LETSENCRYPT_MODE` | Let's Encrypt mode (`staging` or `production`) | `staging` |
| `GORDON_PROXY_EMAIL` | Email for Let's Encrypt | Empty |
| `GORDON_PROXY_CACHE_SIZE` | Certificate cache size | `1000` |
| `GORDON_PROXY_GRACE_PERIOD` | Shutdown grace period in seconds | `30` |
| `GORDON_PROXY_ENABLE_LOGS` | Controls whether HTTP request logs are enabled for the proxy | `true` |

### Other Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `SESSION_SECRET` | Secret for session encryption | Required |
| `RUN_ENV` | Runtime environment (`dev` or `prod`) | `prod` |

## Default Values for Configuration

Gordon now automatically applies default values for critical configuration settings that are missing or set to zero values in your config.yml. This ensures that your reverse proxy and Let's Encrypt integration work properly without requiring you to manually specify every field.

### Important Default Values Applied

If not specified in your config.yml, Gordon will automatically apply these recommended values:

| Setting | Default Value | Why It's Important |
|---------|--------------|---------------------|
| `ReverseProxy.RenewBefore` | 30 days | Ensures certificates are renewed well before expiry |
| `ReverseProxy.CacheSize` | 1000 entries | Optimizes performance when serving multiple domains |
| `ReverseProxy.GracePeriod` | 30 seconds | Allows for graceful server shutdowns |
| `ReverseProxy.AutoRenew` | true | Prevents certificate expiration issues |
| `ReverseProxy.EnableLogs` | true | Controls whether HTTP request logs are enabled for the proxy |
| `ReverseProxy.LetsEncryptMode` | staging | Safe default for testing; change to "production" for real certificates |

This behavior prevents issues that could occur with zero values like:
- Certificates only being renewed at the last moment (or not at all)
- Poor performance due to disabled caching
- Abrupt connection termination during shutdowns

These defaults are applied when:
1. A new configuration file is created
2. An existing configuration file is loaded but is missing these values
3. The values are explicitly set to zero or empty in your configuration

You can always override these defaults by explicitly setting values in your config.yml or through environment variables.

## Secrets Management

For sensitive values like `GORDON_TOKEN` and `SESSION_SECRET`, consider using Docker secrets or a proper secrets management system in production deployments.

## Persistence

To ensure your data persists between container restarts, mount a volume to the `/data` directory or the path specified in `GORDON_STORAGE_DIR`:

```