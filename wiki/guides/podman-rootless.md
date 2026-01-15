# Podman Rootless Setup

Set up Gordon with Podman in rootless mode for enhanced security.

## What You'll Learn

- Installing Podman for rootless containers
- Configuring firewall port forwarding
- Running Gordon as a user service
- Troubleshooting common issues

## Prerequisites

- Ubuntu 24.04 LTS or Debian 13
- VPS with root access (for initial setup)
- Domain pointing to your VPS

## Why Rootless?

Rootless containers run without root privileges, providing:

- **Security**: Container escapes don't grant root access
- **Isolation**: Each user has separate container namespace
- **Simplicity**: No Docker daemon required

## Steps

### 1. System Preparation

```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install Podman and firewalld
sudo apt install -y podman firewalld

# Enable firewalld
sudo systemctl enable --now firewalld
```

### 2. Configure User Namespaces

```bash
# Enable user namespaces
echo 'user.max_user_namespaces=28633' | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Setup subuid/subgid for your user
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER
```

### 3. Configure Firewall Port Forwarding

Rootless containers can't bind to ports below 1024. Forward 80/443 to high ports:

```bash
# Allow HTTP/HTTPS traffic
sudo firewall-cmd --permanent --add-service=http
sudo firewall-cmd --permanent --add-service=https

# Forward privileged ports to unprivileged
sudo firewall-cmd --permanent --add-forward-port=port=80:proto=tcp:toport=8080
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=8443

# Apply changes
sudo firewall-cmd --reload

# Verify
sudo firewall-cmd --list-all
```

### 4. Enable Podman Socket

```bash
# Enable user Podman socket
systemctl --user enable --now podman.socket

# Verify
systemctl --user status podman.socket
```

### 5. Configure Registry Access

Allow pushing to localhost registry without TLS:

```bash
mkdir -p ~/.config/containers
cat > ~/.config/containers/registries.conf <<EOF
[registries.insecure]
registries = ['localhost:5000']
EOF
```

### 6. Install Gordon

```bash
# Download (choose your architecture)
wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64.tar.gz
# or for ARM64:
# wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_arm64.tar.gz

tar -xzf gordon_linux_*.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/
```

### 7. Configure Gordon

Generate initial config:

```bash
gordon start
# Press Ctrl+C after config is created
```

Edit `~/.config/gordon/gordon.toml`:

```toml
[server]
port = 8080                              # Must match firewall forward (80 → 8080)
registry_port = 5000
registry_domain = "registry.mydomain.com"

[routes]
"app.mydomain.com" = "myapp:latest"
```

### 8. Create Systemd User Service

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
ExecStart=/usr/local/bin/gordon start

[Install]
WantedBy=default.target
EOF

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now gordon
```

### 9. Enable Linger

Keep services running after logout:

```bash
sudo loginctl enable-linger $USER
```

### 10. Verify Setup

```bash
# Check Gordon status
systemctl --user status gordon

# Check logs
journalctl --user -u gordon -f

# Test registry
curl -v http://localhost:5000/v2/
```

## Tailscale Integration

If using Tailscale for management:

```bash
# Add Tailscale interface to trusted zone
sudo firewall-cmd --permanent --zone=trusted --add-interface=tailscale0

# Allow Tailscale WireGuard port
sudo firewall-cmd --permanent --add-port=41641/udp

# Apply
sudo firewall-cmd --reload
```

## Common Issues

### "permission denied" on podman

```bash
# Ensure podman socket is running
systemctl --user status podman.socket

# Restart if needed
systemctl --user restart podman.socket
```

### Containers can't access network

```bash
# Check slirp4netns is installed
which slirp4netns

# Install if missing
sudo apt install slirp4netns
```

### Port forwarding not working

```bash
# Verify rules
sudo firewall-cmd --list-all

# Check Gordon is using correct port
grep "port =" ~/.config/gordon/gordon.toml
```

### Service stops after logout

```bash
# Enable linger
sudo loginctl enable-linger $USER

# Verify
loginctl show-user $USER | grep Linger
```

## Next Steps

- [Configure Cloudflare](./cloudflare-setup.md)
- [Set up secrets](./secrets-pass.md)
- [Deploy your first app](/wiki/tutorials/first-deploy.md)
