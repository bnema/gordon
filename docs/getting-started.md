# Getting Started

Deploy your first app with Gordon in under 5 minutes.

## Prerequisites

- A Linux VPS with Docker or Podman installed
- A domain pointing to your VPS (DNS A record)
- Cloudflare account (free tier) for HTTPS termination

## 1. Install Gordon

```bash
# Download the latest release (choose your architecture)
# For x86_64:
wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64.tar.gz
tar -xzf gordon_linux_amd64.tar.gz

# For ARM64 (Raspberry Pi 4, AWS Graviton, etc.):
# wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_arm64.tar.gz
# tar -xzf gordon_linux_arm64.tar.gz

chmod +x gordon
sudo mv gordon /usr/local/bin/
```

## 2. Start Gordon

```bash
# First run creates the default config
gordon start
# Press Ctrl+C to stop
```

Config is created at `~/.config/gordon/gordon.toml`.

## 3. Configure Your Registry Domain

Edit `~/.config/gordon/gordon.toml`:

```toml
[server]
port = 8080                              # Proxy port (use with Cloudflare)
registry_port = 5000                     # Registry port
registry_domain = "registry.mydomain.com"  # Your registry domain

[routes]
"app.mydomain.com" = "myapp:latest"      # Domain → Image mapping
```

## 4. Set Up DNS

In Cloudflare (or your DNS provider):

| Type | Name | Content |
|------|------|---------|
| A | `app` | `YOUR_SERVER_IP` |
| A | `registry` | `YOUR_SERVER_IP` |

Enable Cloudflare proxy (orange cloud) for HTTPS.

## 5. Start Gordon as a Service

```bash
# Create systemd user service
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/gordon.service <<EOF
[Unit]
Description=Gordon Container Platform

[Service]
Type=simple
Restart=always
ExecStart=/usr/local/bin/gordon start

[Install]
WantedBy=default.target
EOF

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now gordon
sudo loginctl enable-linger $USER
```

## 6. Deploy Your First App

On your local machine:

```bash
# Build your app
docker build -t myapp .

# Tag for your registry
docker tag myapp registry.mydomain.com/myapp:latest

# Push to Gordon
docker push registry.mydomain.com/myapp:latest
```

Your app is now live at `https://app.mydomain.com`!

## 7. Update Your App

Push a new image to deploy with zero downtime:

```bash
# Make changes, rebuild
docker build -t myapp .
docker tag myapp registry.mydomain.com/myapp:latest
docker push registry.mydomain.com/myapp:latest
```

Gordon automatically:
1. Starts the new container
2. Waits for it to be ready
3. Routes traffic to the new container
4. Stops the old container

## Next Steps

- [Installation Guide](./installation.md) - Production setup with firewall and rootless containers
- [Configuration Reference](./config/index.md) - All configuration options
- [Registry Authentication](./config/registry-auth.md) - Secure your registry
- [Environment Variables](./config/env.md) - Configure per-app settings

## Related

- [First Deploy Tutorial](/wiki/tutorials/first-deploy.md)
- [Podman Rootless Setup](/wiki/guides/podman-rootless.md)
