# Gordon

> Deploy containers to your VPS in seconds, not hours. One push, zero complexity.

<div align="center">
  <img src="assets/gordon-mascot-hq-trsp.png" alt="Gordon Mascot" width="200">
  <h3>The Smart Way to Deploy Containers on Your VPS</h3>
  <p><em>Push image → Auto-deploy → Zero downtime</em></p>
</div>

## What is Gordon?

Gordon is a lightweight deployment system that turns any VPS into a container hosting platform with push-to-deploy capabilities. It combines a private container registry with an intelligent reverse proxy to create a self-hosted alternative to complex orchestration systems.

### Key Features

- **Built-in Container Registry**: Your VPS becomes a private registry
- **Push-to-Deploy**: Pushing an image triggers automatic deployment
- **Smart Routing**: Multi-domain support with automatic HTTPS
- **Zero-Downtime Updates**: Graceful container swaps on new pushes
- **Network Isolation**: Each app gets its own network with attached services
- **Auto-Volume Management**: Persistent storage from Dockerfile VOLUME directives
- **Environment Merging**: Dockerfile ENV + your custom variables
- **Minimal Footprint**: ~15MB RAM usage, single binary

### Perfect For

- **Solo developers** running multiple projects on one VPS
- **Small teams** wanting simple, reliable deployments
- **Agencies** managing client applications across servers
- **Anyone** tired of overengineered deployment solutions

## How It Works

```bash
# 1. Build & test locally
podman build -t myapp .
podman run -p 8080:8080 myapp  # Works? Great!

# 2. Push to deploy
podman tag myapp registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest

# 3. That's it. Gordon handles the rest.
```

**Your machine is the build server. If it runs locally, it runs in production.**

## Quick Start (5 minutes)

### Prerequisites
- Ubuntu/Debian VPS with root access
- Domain pointing to your VPS
- Cloudflare account (free tier works)

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

**Important:** This is just the binary installation. For a complete working setup including networking, firewall, and systemd service, follow the [detailed setup guide](#detailed-setup-guide) below.

## Core Concepts

### Local-First Development
Your dev machine likely has 8-16 cores and 16-32GB RAM. Your VPS has 1-2 cores and 1-4GB RAM. Why build containers on the weak machine? Gordon lets you build locally and deploy the finished product.

### Push-to-Deploy Workflow
```bash
# Initial deployment
podman build -t myapp .
podman push registry.mydomain.com/myapp:latest
# Visit https://app.mydomain.com

# Update deployment
podman build -t myapp .
podman push registry.mydomain.com/myapp:latest
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

[registry_auth]
username = "admin"                         # Choose your credentials
password = "your-secure-password"          

[routes]
"app.mydomain.com" = "myapp:latest"        # Domain → Image mapping
"api.mydomain.com" = "myapi:v2.1.0"        # Pin specific versions

# Optional: Attach services to apps
[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
# Services accessible via internal DNS: postgres:5432, redis:6379
```

See [examples/](examples/) for advanced configurations including network groups and more.

## Detailed Setup Guide (Using podman in rootless mode)
Note: Refer to Quick Start for the binary installation.

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
podman build -t myapp .

# Test locally
podman run -p 8080:8080 myapp

# Push to registry
podman tag myapp registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest

# Profit!
```

### Versioned Deployment
```bash
# Deploy specific version
podman tag myapp:v1.2.0 registry.mydomain.com/myapp:v1.2.0
podman push registry.mydomain.com/myapp:v1.2.0

# Update route in gordon.toml
# "app.mydomain.com" = "myapp:v1.2.0"
```

### Advanced: Manifest Annotations
```bash
# Deploy with metadata
export VERSION=v1.2.0
podman manifest create myapp:latest
podman manifest add myapp:latest registry.mydomain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.mydomain.com/myapp:$VERSION
podman manifest push myapp:latest registry.mydomain.com/myapp:latest
```

## Community

Gordon is open source and welcomes contributions:
- [Report bugs](https://github.com/bnema/gordon/issues)
- [Suggest features](https://github.com/bnema/gordon/discussions)
- [Submit PRs](https://github.com/bnema/gordon/pulls)

## Philosophy

Traditional deployment pipelines recreate your development environment in CI/CD systems, adding complexity and points of failure. Gordon takes a different approach: **your development machine is already a perfect build environment**.

```bash
# Traditional: Code → CI/CD → Build → Test → Deploy → Hope
# Gordon:     Code → Build → Test → Push → Done
```

## License

GPL-3.0 - Use freely, contribute back.

---

**Built for developers who ship.** If Gordon helps you deploy faster, [give it a star](https://github.com/bnema/gordon).