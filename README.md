# Gordon

<div align="center">
  <img src="assets/gordon-mascot-hq-trsp.png" alt="Gordon Mascot" width="200">
  <h3>The Smart Way to Deploy Containers on Your VPS</h3>
  <p><em>Push image → Auto-deploy → Zero complexity</em></p>
</div>

## Why Gordon?

**You have a $5 VPS. You want to run multiple apps. You don't want complex CI/CD pipelines, expensive PaaS solutions, or overcomplicated orchestration.**

Gordon is the missing piece that makes container deployment on budget VPS servers as simple as expensive PaaS solutions. One binary, one config file, unlimited apps.

### Perfect For
- **Solo developers** with multiple side projects on one VPS
- **Small teams** who want Heroku-like simplicity without the cost
- **Agencies** deploying client apps across VPS servers
- **Anyone** tired of complex deployment pipelines

## How It Works

```bash
# 1. Build & test locally
podman build -t myapp .
podman run -p 8080:8080 myapp  # Works? Great!

# 2. Push to deploy
podman tag myapp registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest

# 3. That's it. Gordon deploys it automatically.
```

**No build servers. No CI/CD complexity. If it runs on your machine, it runs in production.**

## Key Features

- **Local-First Development**: Your machine is the build server
- **Built-in Container Registry**: Your VPS becomes a private registry
- **Push-to-Deploy**: New images deploy instantly
- **Multi-Domain Routing**: Unlimited apps with automatic HTTPS via Cloudflare
- **Zero-Downtime Updates**: Graceful container swaps
- **Auto-Route Creation**: Push `myapp.mydomain.com:latest` → route created automatically

## Quick Start (5 minutes)

### 1. Setup VPS
```bash
# Install essentials on Ubuntu/Debian
sudo apt update && sudo apt install -y podman ufw

# Configure firewall
sudo ufw --force enable
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp 80/tcp 443/tcp
sudo ufw enable
```

### 2. Redirect Ports
```bash
# Redirect 80/443 to 8080
sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8080
sudo iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 8080
sudo apt install -y iptables-persistent
sudo netfilter-persistent save
```

### 3. Enable Rootless Containers
```bash
echo 'user.max_user_namespaces=28633' | sudo tee -a /etc/sysctl.conf
sudo sysctl -p
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER
systemctl --user enable --now podman.socket
```

### 4. Configure Registry
```bash
mkdir -p ~/.config/containers
tee ~/.config/containers/registries.conf > /dev/null <<EOF
[registries.search]
registries = ['docker.io', 'registry.mydomain.com']

[registries.insecure]
registries = ['registry.mydomain.com']
EOF
```

### 5. Install Gordon
```bash
wget https://github.com/bnema/gordon/releases/latest/download/gordon-linux-amd64
chmod +x gordon-linux-amd64
sudo mv gordon-linux-amd64 /usr/local/bin/gordon
```

### 6. Configure Gordon
```bash
# Gordon creates a default config file on first run
# Edit it to set your registry domain and credentials:
# gordon.toml location: ./ → ~/.config/gordon/ → ~/.gordon/ → ~/ → /etc/gordon/

# Key settings to update:
# - registry_domain = "registry.mydomain.com"
# - registry_auth username/password
# - routes for your domains
```

### 7. Point DNS
```
A    *.mydomain.com    →    YOUR_VPS_IP
A    mydomain.com      →    YOUR_VPS_IP
```

### 8. Create Service
```bash
mkdir -p ~/.config/systemd/user
tee ~/.config/systemd/user/gordon.service > /dev/null <<EOF
[Unit]
Description=Gordon Container Platform
After=podman.socket

[Service]
Type=simple
Restart=always
RestartSec=5
Environment=CONTAINER_HOST=unix://%t/podman/podman.sock
ExecStart=/usr/local/bin/gordon start
WorkingDirectory=%h

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now gordon
sudo loginctl enable-linger $USER
```

### 9. Deploy Your First App
```bash
# From your dev machine:
podman login registry.mydomain.com
# (Use the credentials from your gordon.toml)

podman tag myapp:latest registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest
# Visit https://app.mydomain.com
```

## Real-World Examples

### Push-to-Deploy Workflow
```bash
# Build locally
podman build -t myapp .

# Push latest = instant deploy
podman push registry.mydomain.com/myapp:latest
```

### Version Control & Rollbacks
```bash
# Push versioned releases
podman push registry.mydomain.com/myapp:v1.0.1

# Instant rollback in gordon.toml:
# From: "app.mydomain.com" = "myapp:latest"
# To:   "app.mydomain.com" = "myapp:v1.0.0"
```

### Auto-Route Creation
```toml
[auto_route]
enabled = true
```

```bash
# Image name becomes route automatically
podman push registry.mydomain.com/staging.example.com:latest
# Creates: "staging.example.com" = "staging.example.com:latest"
```

## Advanced Configuration

### Full Config Structure
```toml
# gordon.toml - Auto-generated on first run
[server]
port = 8080
registry_domain = "registry.mydomain.com"
runtime = "podman-rootless"  # auto, docker, podman, podman-rootless
# runtime auto-detects: docker → podman-rootless → podman
# Override with env: CONTAINER_HOST=unix:///custom/socket

[registry_auth]
enabled = true
username = "admin"
password = "your-secure-password"

[routes]
"app.mydomain.com" = "myapp:latest"

[auto_route]
enabled = false  # Auto-create routes from image names

[env]
dir = "./data/env"  # Gordon auto-creates env files here
providers = ["pass", "sops"]  # Optional secret management
# Example env file: ./data/env/app_mydomain_com.env
# NODE_ENV=production
# DATABASE_URL=postgresql://localhost:5432/prod
# API_KEY=${pass:company/api-key}      # Unix pass integration
# JWT_SECRET=${sops:secrets.yaml:jwt}  # SOPS integration

[logging]
enabled = false
level = "info"              # trace, debug, info, warn, error
dir = "./logs"              # Log directory
main_log_file = "gordon.log"     # Main app log
proxy_log_file = "proxy.log"     # HTTP traffic log
container_log_dir = "containers" # Container logs subdirectory
max_size = 100              # MB before rotation
max_backups = 3             # Number of old files to keep
max_age = 28                # Days to keep old files
compress = true             # Gzip rotated logs
# Creates: gordon.log, proxy.log, containers/*.log
```

## FAQ

**Q: How is this different from Traefik/Nginx Proxy Manager?**  
A: Those are just reverse proxies. Gordon is a complete deployment platform with built-in registry, automatic deployment, and container lifecycle management.

**Q: Where do builds happen?**  
A: On YOUR machine. If it runs locally, it runs in production.

**Q: How do I rollback?**  
A: Edit gordon.toml to point to a previous version. Takes seconds.

**Q: Do I need Cloudflare?**  
A: Yes, for SSL certificates and DDoS protection.

**Q: Resource requirements?**  
A: Runs on 1GB RAM VPS. Gordon uses <15MB RAM.

## Philosophy

```bash
# Traditional CI/CD
push code → wait for build → hope it works → debug remotely → repeat

# The Gordon Way
build locally → test locally → push image → instant deploy
```

Your machine already works. Why replicate it in CI/CD?

## License

GPL-3.0 - Use freely, contribute back.

---

<div align="center">
  <p><strong>Stop overcomplicating container deployment.</strong></p>
  <p>Star this repo if Gordon saves you money!</p>
</div>