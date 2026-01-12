# Gordon

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL%203.0-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/bnema/gordon)](https://goreportcard.com/report/github.com/bnema/gordon)

Self-hosted web app deployments. Push to your registry, Gordon does the rest.

- Website: https://github.com/bnema/gordon
- Documentation: [Configuration](examples/) | [Setup Guide](#detailed-setup-guide-podman-rootless-mode)
- Discuss: [GitHub Discussions](https://github.com/bnema/gordon/discussions)

---

Gordon is a private container registry + HTTP reverse proxy for your VPS. Push a container image exposing a web port, it deploys automatically with zero downtime.

```bash
docker build -t myapp .
docker push registry.your-server.com/myapp:latest
# → Live at https://app.your-server.com
```

Build on your machine, push to deploy. Works from your laptop or CI.

**What it does:**
- Runs a private Docker registry on your VPS
- Routes domains to containers via HTTP reverse proxy
- Deploys automatically when you push a new image
- Zero downtime updates, persistent volumes, environment merging
- Single binary, ~15MB RAM

## Quick Start (5 minutes)

### Prerequisites
- Ubuntu/Debian VPS with root access
- Domain pointing to your VPS
- Cloudflare account (free tier works) - **Required for HTTPS**

> **HTTPS Architecture:** Cloudflare terminates HTTPS and proxies to Gordon over HTTP internally. Gordon doesn't handle TLS certificates directly (no Let's Encrypt support yet).

### Installation

```bash
# Download Gordon from the releases page
# https://github.com/bnema/gordon/releases/latest
# Example: wget https://github.com/bnema/gordon/releases/download/v2.0.0/gordon_2.0.0_linux_amd64.tar.gz
# Then extract:
tar -xzf gordon_*.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/

# Create initial config
gordon start
# Press Ctrl+C after config is created
```

**Important:** This is just the binary installation. For a complete working setup including networking, firewall, and systemd service, follow the [detailed setup guide](#detailed-setup-guide-podman-rootless-mode) below.

## Core Concepts

### Local-First Development
Your dev machine likely has 8-16 cores and 16-32GB RAM. Your VPS has 1-2 cores and 1-4GB RAM. Why build containers on the weak machine? Gordon lets you build locally and deploy the finished product.

### Push-to-Deploy Workflow
```bash
# Initial deployment
docker build -t myapp .
docker push registry.mydomain.com/myapp:latest
# Visit https://app.mydomain.com

# Update deployment
docker build -t myapp .
docker push registry.mydomain.com/myapp:latest
# Zero-downtime update applied automatically
```

### Automatic Volume Management
```dockerfile
# In your Dockerfile
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]  # Gordon creates persistent storage

FROM node:18
VOLUME ["/app/uploads", "/app/data"]  # Multiple volumes supported
```

### Environment Configuration
Gordon automatically creates and loads env files based on domain names:
```bash
# Auto-created on first run: ~/.local/share/gordon/env/app_mydomain_com.env
# Just add your variables:
DATABASE_URL=postgresql://localhost:5432/myapp
API_KEY=sk-1234567890
NODE_ENV=production
```
Supports secrets from `pass` and `SOPS`. See [examples/](examples/) for advanced usage.

### Network Isolation
Each app runs in its own isolated network by default. Attach services for internal communication:
```toml
[attachments]
"app.mydomain.com" = ["my-postgres:latest", "my-redis:latest"]
# Access services via hostname: my-postgres:5432, my-redis:6379
```
Gordon also supports network groups, see [examples/](examples/) for advanced usage.

### Persistent Storage
- **Volumes**: Automatically created from Dockerfile VOLUME directives, persist across updates

### Production ready logging
- **Logs**: All container output saved to `~/.local/share/gordon/logs/containers/`
- **Rotation**: Logs rotate at 100MB, keeping 3 backups for 28 days

## Configuration

Gordon uses a single TOML file for all configuration:

```toml
# ~/.config/gordon/gordon.toml
[server]
registry_domain = "registry.mydomain.com"  # Required

# Secrets backend for sensitive data (tokens, passwords)
[secrets]
backend = "pass"  # "pass", "sops", or "unsafe" (plain text in data_dir)

# Registry authentication (choose password or token type)
[registry_auth]
enabled = true
type = "password"                          # "password" or "token"

# Password auth: bcrypt hash stored in secrets backend
username = "deploy"
password_hash = "gordon/registry/password_hash"  # path in secrets backend

# Token auth: JWT-based authentication
# token_secret = "gordon/registry/token_secret"  # path in secrets backend

[routes]
"app.mydomain.com" = "myapp:latest"        # Domain → Image mapping
"api.mydomain.com" = "myapi:v2.1.0"        # Pin specific versions

# Optional: Attach services to apps
[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
# Services accessible via internal DNS: postgres:5432, redis:6379
```

See [examples/](examples/) for advanced configurations including network groups and more.

### Authentication Types

**Password Authentication** (simple):
```bash
# Generate a bcrypt hash for your password
gordon auth password hash
# Store the hash in your secrets backend, then reference it in config
```

**Token Authentication** (recommended for CI/CD):
```bash
# Generate a never-expiring token for CI
gordon auth token generate --subject ci-bot --scopes push,pull --expiry 0

# List all tokens
gordon auth token list

# Revoke a compromised token
gordon auth token revoke <token-id>
```

Tokens are stored in your configured secrets backend and are unique to each Gordon instance (different `token_secret` = incompatible tokens).

### GitHub Actions Integration

Gordon provides an official GitHub Action for automated deployments on tag push:

```yaml
# .github/workflows/deploy.yml
name: Deploy to Gordon

on:
  push:
    tags:
      - 'v*'

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      - name: Deploy to Gordon
        uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: ${{ secrets.GORDON_REGISTRY }}
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
```

**Setup:**
1. Generate a CI token: `gordon auth token generate --subject github-actions --scopes push,pull --expiry 0`
2. Add secrets to your GitHub repository: `GORDON_REGISTRY`, `GORDON_USERNAME`, `GORDON_TOKEN`
3. Push a tag to deploy: `git tag v1.0.0 && git push origin v1.0.0`

See [examples/github-workflow.yml](examples/github-workflow.yml) and [.github/actions/deploy/README.md](.github/actions/deploy/README.md) for advanced options.

## Detailed Setup Guide (Podman Rootless Mode)

> **Note:** This guide uses Podman in rootless mode for enhanced security. Docker users can follow similar steps - Gordon works with any OCI-compatible runtime. Refer to Quick Start for the binary installation.

### 1. VPS Preparation
```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install podman and firewall
sudo apt install -y podman ufw

# Configure firewall
sudo ufw allow 22/tcp 80/tcp 443/tcp
sudo default deny
sudo ufw --force enable
```

### 2. Enable Port Forwarding
```bash
# Forward 80/443 to unprivileged port 8080
sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8080
sudo iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 8080

# Make persistent
sudo apt install -y iptables-persistent
sudo netfilter-persistent save
```

### 3. Configure Rootless Containers
```bash
# Enable user namespaces
echo 'user.max_user_namespaces=28633' | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Setup subuid/subgid
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER

# Enable podman socket
systemctl --user enable --now podman.socket
```

### 4. Registry Configuration
```bash
mkdir -p ~/.config/containers
cat > ~/.config/containers/registries.conf <<EOF
[registries.insecure]
registries = ['localhost:5000']
EOF
```

### 5. Create Systemd Service
```bash
# Create user service
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/gordon.service <<EOF
[Unit]
Description=Gordon Container Platform
After=podman.socket

[Service]
Type=simple
Restart=always
RestartSec=5
ExecStart=/usr/local/bin/gordon start
WorkingDirectory=%h

[Install]
WantedBy=default.target
EOF

# Enable and start service
systemctl --user daemon-reload
systemctl --user enable --now gordon
sudo loginctl enable-linger $USER

# Verify it's running
systemctl --user status gordon
```

## Deployment Strategies

### Simple Deployment
```bash
# Build locally
docker build -t myapp .

# Test locally
docker run -p 8080:8080 myapp

# Push to registry
docker tag myapp registry.mydomain.com/myapp:latest
docker push registry.mydomain.com/myapp:latest

# Profit!
```

### Versioned Deployment
```bash
# Deploy specific version
docker tag myapp:v1.2.0 registry.mydomain.com/myapp:v1.2.0
docker push registry.mydomain.com/myapp:v1.2.0

# Update route in gordon.toml
# "app.mydomain.com" = "myapp:v1.2.0"
```

### Advanced: Manifest Annotations
```bash
# Deploy with metadata
export VERSION=v1.2.0
docker manifest create myapp:latest
docker manifest add myapp:latest registry.mydomain.com/myapp:$VERSION
docker manifest annotate myapp:latest --annotation version=$VERSION registry.mydomain.com/myapp:$VERSION
docker manifest push myapp:latest registry.mydomain.com/myapp:latest
```

## Community

Gordon is open source and welcomes contributions:
- [Report bugs](https://github.com/bnema/gordon/issues)
- [Suggest features](https://github.com/bnema/gordon/discussions)
- [Submit PRs](https://github.com/bnema/gordon/pulls)

## Why Gordon?

Most deployment tools require a CI/CD pipeline before you can ship anything. Gordon works the other way: push from your laptop on day one, add GitHub Actions when the project grows.

Your dev machine has 16 cores and 32GB RAM. Your VPS has 2 cores and 2GB. Why rebuild everything on the weak machine? Build locally, push the result.

## License

GPL-3.0 - Use freely, contribute back.

---

If your deployment process has more YAML than application code, something went wrong.