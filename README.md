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
- **Network Isolation**: Each app gets its own isolated network with attached services (databases, caches, etc.)
- **Auto-Route Creation** (Optional): Push `myapp.mydomain.com:latest` → route created automatically
- **Auto-Volume Management**: Zero-config persistent storage from Dockerfile VOLUME directives
- **Auto-Environment Injection**: Dockerfile ENV directives automatically merge with your custom environment variables

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

Gordon supports multiple rollback strategies. Choose the approach that fits your workflow:

#### Method 1: Tag-Based Rollbacks (Simple)
```bash
# Tag and push a specific version
podman tag myapp:v1.0.1 registry.mydomain.com/myapp:v1.0.1
podman push registry.mydomain.com/myapp:v1.0.1

# Tag and push latest (v1.0.2 in this example)
podman tag myapp:v1.0.2 registry.mydomain.com/myapp:v1.0.2 && podman tag myapp:v1.0.2 registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:v1.0.2 && podman push registry.mydomain.com/myapp:latest

# Rollback: Update gordon.toml and reload
# From: "app.mydomain.com" = "myapp:latest"
# To:   "app.mydomain.com" = "myapp:v1.0.1"
# Then: systemctl --user reload gordon
```

#### Method 2: Manifest Annotation Deployment (Advanced)
```bash
# Build and push your versioned images first
export VERSION=v1.0.1
podman build --tag myapp:$VERSION --tag registry.mydomain.com/myapp:$VERSION .
podman push registry.mydomain.com/myapp:$VERSION

export VERSION=v1.0.2
podman build --tag myapp:$VERSION --tag registry.mydomain.com/myapp:$VERSION .
podman push registry.mydomain.com/myapp:$VERSION

# Deploy v1.0.2 with version annotation
export VERSION=v1.0.2
podman manifest create myapp:latest
podman manifest add myapp:latest registry.mydomain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.mydomain.com/myapp:$VERSION
podman manifest push myapp:latest registry.mydomain.com/myapp:latest

# Rollback to v1.0.1 (just change the version)
export VERSION=v1.0.1
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.mydomain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.mydomain.com/myapp:$VERSION
podman manifest push myapp:latest registry.mydomain.com/myapp:latest
# Gordon automatically deploys the specified version
```

**Recommendation**: Use Method 2 for instant deployments without config file edits. Method 1 is best for simple workflows.

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

## Persistent Data with Volumes

Gordon automatically detects `VOLUME` directives in your Dockerfiles and creates persistent named volumes for your containers. **Zero configuration required** - just add `VOLUME` instructions to your Dockerfile.

### How It Works

```dockerfile
# In your Dockerfile
FROM node:18-alpine
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .

# Gordon automatically creates volumes for these paths
VOLUME ["/app/data", "/app/uploads"]

EXPOSE 3000
CMD ["npm", "start"]
```

When you deploy this image, Gordon will:
1. **Detect** the `VOLUME ["/app/data", "/app/uploads"]` directive
2. **Create** named volumes like `gordon-app-example-com-a1b2c3d4` (one per path)
3. **Mount** them to your container automatically
4. **Preserve** data across container restarts and updates

### Volume Features

- **Automatic Detection**: No config needed - Gordon reads VOLUME directives from your images
- **Persistent Storage**: Data survives container restarts, updates, and VPS reboots
- **Zero Downtime**: Volumes re-attach instantly when containers restart
- **Predictable Naming**: `gordon-{domain}-{path-hash}` format for easy identification
- **Cross-Platform**: Works with Docker and Podman

### Example Workflows

#### Database Container
```dockerfile
FROM postgres:15
# Gordon auto-creates persistent volume for PostgreSQL data
VOLUME ["/var/lib/postgresql/data"]
```

#### Web App with File Uploads
```dockerfile
FROM nginx:alpine
COPY dist/ /usr/share/nginx/html/
# Gordon auto-creates persistent volume for user uploads
VOLUME ["/usr/share/nginx/html/uploads"]
```

#### Multi-Volume Application
```dockerfile
FROM myapp:latest
# Gordon creates separate volumes for each path
VOLUME ["/app/data", "/app/logs", "/app/cache"]
```

### Volume Configuration (Optional)

```toml
[volumes]
auto_create = true    # Default: true (set to false to disable)
prefix = "gordon"     # Default: "gordon" (volume name prefix)
preserve = true       # Default: true (keep volumes when removing containers)
```

**Most users never need to touch this configuration** - the defaults work perfectly for 99% of use cases.

## Smart Environment & Volume Management

Gordon automatically handles both environment variables and persistent storage with zero configuration:

### Environment Variables
- **Auto-Detection**: Reads `ENV` directives from your Dockerfile
- **Smart Merging**: Your `.env` files override Dockerfile ENV values
- **Secret Integration**: Supports `pass` and `SOPS` for sensitive data

### Persistent Volumes  
- **Auto-Detection**: Reads `VOLUME` directives from your Dockerfile
- **Zero Configuration**: Creates and manages volumes automatically
- **Data Persistence**: Survives container updates and reboots

See [examples/](examples/) for detailed configuration examples.

## Advanced Configuration

### Full Config Structure (with default values)
```toml
# gordon.toml - Auto-generated on first run
[server]
port = 8080                          # Default: 8080
registry_port = 5000                 # Default: 5000  
registry_domain = "registry.mydomain.com"  # No default (required)
runtime = "auto"                     # Default: "auto" (auto, docker, podman, podman-rootless)
socket_path = ""                     # Default: "" (auto-detected)
data_dir = "~/.local/share/gordon"   # Default: ~/.local/share/gordon (or ./data for root)
ssl_email = ""                       # No default (optional)
# runtime auto-detects: docker → podman-rootless → podman
# Override with env: CONTAINER_HOST=unix:///custom/socket

[registry_auth]
enabled = true                       # Default: true
username = "admin"                   # No default (required when enabled)
password = "your-secure-password"    # No default (required when enabled)

[routes]
"app.mydomain.com" = "myapp:latest"  # Default: {} (empty map)
# Routes default to HTTPS unless prefixed with http://

# Network isolation - each app gets its own isolated network
[attachments]
"app.mydomain.com" = ["my-postgres:latest", "my-redis:latest"]
# Services accessible via: postgres:5432, redis:6379 (internal DNS)
# Create custom Dockerfiles with VOLUME directives for persistence

[auto_route]
enabled = false                      # Default: false

[volumes]
auto_create = true                   # Default: true (automatically create volumes from VOLUME directives)
prefix = "gordon"                    # Default: "gordon" (volume name prefix)
preserve = true                      # Default: true (keep volumes when containers are removed)
# Volumes are auto-created from Dockerfile VOLUME directives
# Naming: gordon-{domain}-{path-hash} (e.g., gordon-app-example-com-a1b2c3d4)

[env]
dir = "{data_dir}/env"              # Default: {data_dir}/env
providers = ["pass", "sops"]        # Default: ["pass", "sops"]
# Example env file: ./data/env/app_mydomain_com.env
# NODE_ENV=production
# DATABASE_URL=postgresql://localhost:5432/prod
# API_KEY=${pass:company/api-key}      # Unix pass integration
# JWT_SECRET=${sops:secrets.yaml:jwt}  # SOPS integration

[logging]
enabled = true                       # Default: true
level = "info"                       # Default: "info" (debug, info, warn, error, fatal, panic)
dir = "{data_dir}/logs"             # Default: {data_dir}/logs
main_log_file = "gordon.log"        # Default: "gordon.log"
proxy_log_file = "proxy.log"        # Default: "proxy.log"
container_log_dir = "containers"    # Default: "containers"
max_size = 100                      # Default: 100 (MB before rotation)
max_backups = 3                     # Default: 3 (old files to keep)
max_age = 28                        # Default: 28 (days to keep old files)
compress = true                     # Default: true (gzip rotated logs)
# Creates: gordon.log, proxy.log, containers/*.log
```

## FAQ

**Q: How is this different from Traefik/Nginx Proxy Manager?**  
A: Those are just reverse proxies. Gordon is a complete deployment platform with built-in registry, automatic deployment, and container lifecycle management.

**Q: Where do builds happen?**  
A: On YOUR machine. If it runs locally, it runs in production.

**Q: How do I rollback?**  
A: Change the version annotation in your manifest and push, or edit gordon.toml. Takes seconds.

**Q: Do I need Cloudflare?**  
A: Yes, for SSL certificates and DDoS protection.

**Q: Resource requirements?**  
A: Runs on 1GB RAM VPS. Gordon uses <15MB RAM.

**Q: How do I check if volumes are working?**  
A: Use `docker volume ls` or `podman volume ls` to see Gordon-managed volumes (named `gordon-*`).

**Q: Can I disable automatic volumes?**  
A: Yes, set `volumes.auto_create = false` in your gordon.toml config.

**Q: What happens to my data when I update containers?**  
A: Volumes are preserved by default. Your data persists across updates and reboots.

**Q: How do I backup volume data?**  
A: Volumes are regular Docker/Podman volumes - use standard backup tools like `docker run --rm -v volume_name:/data -v $(pwd):/backup alpine tar czf /backup/backup.tar.gz -C /data .`

**Q: Do Dockerfile ENV directives work automatically?**  
A: Yes! Gordon reads ENV directives from your images and merges them with your custom .env files.

**Q: What happens if I set the same environment variable in both Dockerfile and .env?**  
A: Your .env file always wins - custom environment variables override Dockerfile ENV directives.

**Q: Can I see what environment variables are being applied?**  
A: Yes, check Gordon's logs - it shows both Dockerfile ENV and final merged environment variables.

**Q: Do I need to rebuild my images to change environment variables?**  
A: No! Just update your .env files and reload Gordon. Dockerfile ENV provides defaults; .env provides overrides.

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