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

# Configure insecure localhost registry (required — Podman blocks HTTP registries by default)
mkdir -p ~/.config/containers/registries.conf.d
cat > ~/.config/containers/registries.conf.d/gordon.conf <<EOF
[[registry]]
location = "localhost:5000"
insecure = true
EOF
```

## Firewall Configuration

Gordon needs ports accessible for the registry and the public edge entrypoint.

### Using firewalld

```bash
# Install and enable firewalld
sudo apt install -y firewalld
sudo systemctl enable --now firewalld

# Allow HTTP/HTTPS
sudo firewall-cmd --permanent --add-service=http
sudo firewall-cmd --permanent --add-service=https

# For rootless services, bind entrypoints.edge.address to a high port (for example :9000)
# and redirect the external TCP edge ports your deployment uses.
sudo firewall-cmd --permanent --add-forward-port=port=80:proto=tcp:toport=9000
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=9000

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
registry_port = 5000
gordon_domain = "gordon.yourdomain.com"

[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"

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
# Required for Podman rootless — tells Gordon where to find the Podman socket
Environment=XDG_RUNTIME_DIR=/run/user/%U
Environment=DOCKER_HOST=unix:///run/user/%U/podman/podman.sock
ExecStart=/usr/local/bin/gordon serve

[Install]
WantedBy=default.target
EOF

> **Docker users:** Omit the two `Environment=` lines above — Docker's socket path is set automatically.

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

## Cloudflare SSL Mode

Gordon serves public application traffic through HTTP-capable entrypoints such as `entrypoints.edge` with `protocol = "smart_tcp"`. Choose the Cloudflare mode that matches how you want Cloudflare to reach your origin.

| Mode | How it works | Use when |
|------|-------------|----------|
| **Flexible** | Cloudflare terminates TLS; connects to Gordon with cleartext HTTP | You want edge HTTPS only |
| **Full (Strict)** | Cloudflare connects to Gordon over HTTPS with a valid cert | You want end-to-end HTTPS using Gordon's public ACME certs or a static cert (`tls_cert_file` / `tls_key_file`) |

Wrong mode causes: **521** (Cloudflare can't connect) or **525** (TLS handshake failed).

> **Important:** For Cloudflare-proxied HTTP paths, set `proxy_allowed_ips` with Cloudflare edge IPs — see [Proxy Origin Allowlist](#proxy-origin-allowlist) below.

> **Rootless note:** Unprivileged users can't bind privileged ports. Bind `entrypoints.edge.address` to a high port (for example `:9000`) and forward/map external ports to it via firewall or container settings.

## Proxy Origin Allowlist

When Gordon serves HTTP paths through a smart TCP edge, direct non-localhost HTTP requests can be restricted to certificate onboarding paths. Cloudflare and other reverse proxies must be listed in `proxy_allowed_ips` to reach your applications.

Add Cloudflare edge IPs to your `gordon.toml`:

```toml
[server]
proxy_allowed_ips = [
  "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
  "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
  "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
  "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
  "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
]
```

Without this setting, Cloudflare traffic receives `403 Forbidden: Only certificate onboarding is available over HTTP`.

> **Note:** This is separate from `[api.rate_limit] trusted_proxies`, which controls IP extraction from `X-Forwarded-For`. Both should list your proxy IPs. See [Proxy Origin IP Allowlist](./config/server.md#proxy-origin-ip-allowlist) for details.

## Installing a Specific Version or Pre-Release

```bash
# Install an exact version
curl -fsSL https://gordon.bnema.dev/install | GORDON_VERSION=v2.30.1 bash

# Install the latest pre-release instead of the latest stable release
curl -fsSL https://gordon.bnema.dev/install | GORDON_PRERELEASE=1 bash
```

By default, the installer resolves `latest` to the newest stable release and verifies the downloaded checksum before installing.

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
