# Running Gordon in a Container

Run Gordon itself inside a Docker or Podman container for isolated deployments.

## What You'll Learn

- Running Gordon in Docker with socket access
- Running Gordon in Podman (rootless)
- Configuration via environment variables
- Volume management for persistence

## Prerequisites

- Docker or Podman installed on the host
- Domain name pointing to your server
- Basic understanding of container networking

## Why Run Gordon in a Container?

- **Isolation**: Gordon runs in its own environment
- **Portability**: Same setup across different hosts
- **Updates**: Easy version management with image tags
- **Orchestration**: Can be managed by Docker Compose, Kubernetes, etc.

## Docker

### Quick Start

```bash
docker run -d \
  --name gordon \
  -p 80:8080 \
  -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v gordon-data:/data \
  -e GORDON_SERVER_REGISTRY_DOMAIN=registry.mydomain.com \
  ghcr.io/bnema/gordon:latest
```

### With Configuration File

Create `gordon.toml`:

```toml
[server]
port = 8080
registry_port = 5000
registry_domain = "registry.mydomain.com"

[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "myapi:latest"

[logging]
level = "info"
```

Run with mounted config:

```bash
docker run -d \
  --name gordon \
  -p 80:8080 \
  -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v gordon-data:/data \
  -v $(pwd)/gordon.toml:/etc/gordon/gordon.toml:ro \
  ghcr.io/bnema/gordon:latest
```

### Docker Compose

```yaml
services:
  gordon:
    image: ghcr.io/bnema/gordon:latest
    container_name: gordon
    restart: unless-stopped
    ports:
      - "80:8080"
      - "5000:5000"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - gordon-data:/data
      - ./gordon.toml:/etc/gordon/gordon.toml:ro
    environment:
      - GORDON_LOGGING_LEVEL=info
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://localhost:8080/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s

volumes:
  gordon-data:
```

Start with:

```bash
docker compose up -d
```

## Podman

### Socket Setup

Podman uses a different socket path. Enable the Podman socket first:

```bash
# For rootless Podman (user service)
systemctl --user enable --now podman.socket

# Socket path: /run/user/$(id -u)/podman/podman.sock
```

For system-wide (root) Podman:

```bash
sudo systemctl enable --now podman.socket

# Socket path: /run/podman/podman.sock
```

### Quick Start (Rootless)

```bash
podman run -d \
  --name gordon \
  -p 8080:8080 \
  -p 5000:5000 \
  --security-opt label=disable \
  -v /run/user/$(id -u)/podman/podman.sock:/var/run/docker.sock \
  -v gordon-data:/data \
  -e GORDON_SERVER_REGISTRY_DOMAIN=registry.mydomain.com \
  -e DOCKER_HOST=unix:///var/run/docker.sock \
  ghcr.io/bnema/gordon:latest
```

> **Note**: `--security-opt label=disable` is required for socket access with SELinux.

### Quick Start (Root/System)

```bash
sudo podman run -d \
  --name gordon \
  -p 80:8080 \
  -p 5000:5000 \
  --security-opt label=disable \
  -v /run/podman/podman.sock:/var/run/docker.sock \
  -v gordon-data:/data \
  -e GORDON_SERVER_REGISTRY_DOMAIN=registry.mydomain.com \
  -e DOCKER_HOST=unix:///var/run/docker.sock \
  ghcr.io/bnema/gordon:latest
```

### Podman Compose (Quadlet)

Create `~/.config/containers/systemd/gordon.container`:

```ini
[Unit]
Description=Gordon Container Platform
After=podman.socket

[Container]
Image=ghcr.io/bnema/gordon:latest
ContainerName=gordon
PublishPort=8080:8080
PublishPort=5000:5000
Volume=/run/user/%U/podman/podman.sock:/var/run/docker.sock
Volume=gordon-data:/data
Environment=GORDON_SERVER_REGISTRY_DOMAIN=registry.mydomain.com
Environment=DOCKER_HOST=unix:///var/run/docker.sock
SecurityLabelDisable=true

[Service]
Restart=always

[Install]
WantedBy=default.target
```

Enable and start:

```bash
systemctl --user daemon-reload
systemctl --user enable --now gordon
```

## Configuration Reference

### Required Volumes

| Mount | Purpose |
|-------|---------|
| `/var/run/docker.sock` | Access to container runtime (Docker or Podman socket) |
| `/data` | Registry storage, logs, environment files |

### Optional Volumes

| Mount | Purpose |
|-------|---------|
| `/etc/gordon/gordon.toml` | Configuration file |

### Ports

| Port | Purpose |
|------|---------|
| `8080` | HTTP proxy (map to 80 on host) |
| `5000` | Container registry |

### Environment Variables

All configuration options can be set via environment variables with `GORDON_` prefix:

| Variable | Description | Example |
|----------|-------------|---------|
| `GORDON_SERVER_PORT` | Proxy port | `8080` |
| `GORDON_SERVER_REGISTRY_PORT` | Registry port | `5000` |
| `GORDON_SERVER_REGISTRY_DOMAIN` | Registry domain (required) | `registry.mydomain.com` |
| `GORDON_SERVER_DATA_DIR` | Data directory | `/data` |
| `GORDON_LOGGING_LEVEL` | Log level | `debug`, `info`, `warn`, `error` |
| `GORDON_REGISTRY_AUTH_ENABLED` | Enable registry auth | `true` |
| `GORDON_REGISTRY_AUTH_USERNAME` | Registry username | `admin` |

> **Note**: For registry passwords, using plain `password` is deprecated. Use `password_hash` with a secrets backend instead. See [Registry Auth](/docs/config/registry-auth.md) for secure setup.

## Security Considerations

### Docker Socket Access

Mounting the Docker/Podman socket grants Gordon full access to the container runtime. This is required for Gordon to manage containers but has security implications:

- Gordon can create, stop, and remove any container
- Gordon can access volumes and networks
- Consider network policies to restrict Gordon's container access

### Recommendations

1. **Use a dedicated host**: Run Gordon on a dedicated server or VM
2. **Limit network exposure**: Only expose ports 80/443 publicly
3. **Use TLS**: Put Gordon behind a reverse proxy with TLS (Cloudflare, Caddy, nginx)
4. **Enable registry auth**: Always enable authentication for the registry

## Troubleshooting

### "Cannot connect to Docker daemon"

Verify socket mount:

```bash
# Docker
docker exec gordon ls -la /var/run/docker.sock

# Podman - check socket exists
ls -la /run/user/$(id -u)/podman/podman.sock
```

### "Permission denied" on socket

For Podman, ensure `--security-opt label=disable` is set.

For Docker, ensure the `gordon` user inside the container can access the socket:

```bash
# Check socket permissions on host
ls -la /var/run/docker.sock
```

### Container can't bind to port 80

The Gordon container runs as non-root user `gordon`. Use port 8080 inside the container and map to 80 on the host:

```bash
-p 80:8080  # Host port 80 -> Container port 8080
```

### Health check failing

```bash
# Check Gordon logs
docker logs gordon

# Test health endpoint manually
docker exec gordon wget -q -O- http://localhost:8080/health
```

## Next Steps

- [Configure routes](/docs/config/routes.md)
- [Set up network groups](/docs/config/network-groups.md)
- [Enable registry authentication](/docs/config/registry-auth.md)
