# Installation

Detailed installation guide for production environments.

## System Requirements

- **OS**: Linux (Ubuntu 24.04 LTS, Debian 13 recommended) or macOS
- **Architecture**: x86_64 (amd64) or ARM64
- **Memory**: 512MB minimum, 1GB recommended
- **Disk**: 10GB minimum for registry storage
- **Runtime**: Docker or Podman

## Download Gordon

### Quick Install (Recommended)

```bash
curl -fsSL https://gordon.bnema.dev/install | bash
```

This script automatically detects your OS (Linux/macOS) and architecture (amd64/arm64), downloads the appropriate binary from GitHub releases, and installs it to `/usr/local/bin`.

### Manual Installation

**Linux (x86_64)**
```bash
wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64.tar.gz
tar -xzf gordon_linux_amd64.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/
```

**Linux (ARM64)** - for Raspberry Pi 4, AWS Graviton, Oracle Ampere, etc.
```bash
wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_arm64.tar.gz
tar -xzf gordon_linux_arm64.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/
```

**macOS (Apple Silicon)**
```bash
curl -LO https://github.com/bnema/gordon/releases/latest/download/gordon_darwin_arm64.tar.gz
tar -xzf gordon_darwin_arm64.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/
```

**macOS (Intel)**
```bash
curl -LO https://github.com/bnema/gordon/releases/latest/download/gordon_darwin_amd64.tar.gz
tar -xzf gordon_darwin_amd64.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/
```

Verify installation:
```bash
gordon version
```

### From Source

```bash
# Requires Go 1.21+
git clone https://github.com/bnema/gordon.git
cd gordon
make build
sudo mv gordon /usr/local/bin/
```

## Container Runtime Setup

### Docker

```bash
# Install Docker
curl -fsSL https://get.docker.com | sh

# Add user to docker group
sudo usermod -aG docker $USER
newgrp docker

# Verify
docker run hello-world
```

### Podman (Rootless)

Podman rootless mode provides enhanced security by running containers without root privileges.

```bash
# Install Podman
sudo apt update
sudo apt install -y podman

# Enable user namespaces
echo 'user.max_user_namespaces=28633' | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Setup subuid/subgid
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER

# Enable Podman socket
systemctl --user enable --now podman.socket

# Configure insecure localhost registry
mkdir -p ~/.config/containers
cat > ~/.config/containers/registries.conf <<EOF
[registries.insecure]
registries = ['localhost:5000']
EOF
```

## Firewall Configuration

Gordon needs ports accessible for the registry and proxy.

### Using firewalld

```bash
# Install and enable firewalld
sudo apt install -y firewalld
sudo systemctl enable --now firewalld

# Allow HTTP/HTTPS
sudo firewall-cmd --permanent --add-service=http
sudo firewall-cmd --permanent --add-service=https

# For rootless containers, redirect privileged ports
sudo firewall-cmd --permanent --add-forward-port=port=80:proto=tcp:toport=8080
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=8443

# Apply changes
sudo firewall-cmd --reload

# Verify
sudo firewall-cmd --list-all
```

### Using ufw

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 5000/tcp  # Registry port (if accessing directly)
sudo ufw enable
```

## Configuration

Gordon creates a default configuration on first run:

```bash
# Run once to generate config, then stop
gordon serve
# Press Ctrl+C

# Edit configuration
nano ~/.config/gordon/gordon.toml
```

Minimum required configuration:

```toml
[server]
port = 8080
registry_port = 5000
gordon_domain = "gordon.yourdomain.com"

[routes]
"app.yourdomain.com" = "myapp:latest"
```

See [Configuration Reference](./config/index.md) for all options.

## Systemd Service

### User Service (Recommended for Rootless)

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/gordon.service <<EOF
[Unit]
Description=Gordon Container Platform
After=podman.socket

[Service]
Type=simple
Restart=always
RestartSec=5
ExecStart=/usr/local/bin/gordon serve

[Install]
WantedBy=default.target
EOF

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now gordon

# Enable linger (keep service running after logout)
sudo loginctl enable-linger $USER

# Check status
systemctl --user status gordon
```

### System Service (Root Mode)

```bash
sudo cat > /etc/systemd/system/gordon.service <<EOF
[Unit]
Description=Gordon Container Platform
After=docker.service
Requires=docker.service

[Service]
Type=simple
Restart=always
RestartSec=5
ExecStart=/usr/local/bin/gordon serve
User=root

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now gordon
```

## DNS Configuration

Point your domains to your server:

| Type | Name | Content | Proxy |
|------|------|---------|-------|
| A | `app` | `SERVER_IP` | Yes (Cloudflare) |
| A | `registry` | `SERVER_IP` | Yes (Cloudflare) |

> **Note:** Enable Cloudflare proxy (orange cloud) for automatic HTTPS. Gordon receives HTTP traffic from Cloudflare.

## Verify Installation

```bash
# Check Gordon is running
systemctl --user status gordon

# View logs
journalctl --user -u gordon -f

# Test registry (from local machine)
docker login registry.yourdomain.com
docker pull alpine
docker tag alpine registry.yourdomain.com/test:latest
docker push registry.yourdomain.com/test:latest
```

## Data Directories

Gordon stores data in the following locations:

| Path | Purpose |
|------|---------|
| `~/.config/gordon/gordon.toml` | Configuration file |
| `~/.gordon/` | Default data directory |
| `~/.gordon/registry/` | Container images |
| `~/.gordon/env/` | Environment files |
| `~/.gordon/logs/` | Application logs |
| `~/.gordon/secrets/` | Secrets (unsafe backend only) |

## Related

- [Getting Started](./getting-started.md)
- [Configuration Reference](./config/index.md)
- [Podman Rootless Setup](/wiki/guides/podman-rootless.md)
