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

### Build Locally, Deploy Instantly

```bash
# 1. Build & test on YOUR machine
podman build -t myapp .
podman run -p 8080:8080 myapp  # Works? Great!

# 2. Push to deploy
podman tag myapp registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest

# 3. That's it. Gordon deploys it automatically.
```

**No build servers. No CI/CD complexity. If it runs on your machine, it runs in production.**

### Instant Rollbacks

Something broke? Just change your config:

```toml
# Before (in gordon.toml)
"app.mydomain.com" = "myapp:latest"

# After - instant rollback!
"app.mydomain.com" = "myapp:v1.2.3"
```

Save the file. Gordon redeploys the previous version. Problem solved in seconds.

## Key Features

### Local-First Development
**Your machine is the build server.** Test locally with your favorite container engine, push when ready. No waiting for remote builds.

### Built-in Container Registry
Your VPS becomes a private container registry (Docker/Podman compatible). No Docker Hub subscription needed.

### Push-to-Deploy Magic
Gordon watches for new images and deploys them instantly to configured domains.

### Version Control Built-In
Keep multiple versions in your registry. Switch between them instantly by editing the config.

### Multi-Domain Routing
Run unlimited apps on one server. Each gets its own domain with automatic HTTPS via Cloudflare.

### Zero-Downtime Updates
Push new versions anytime. Gordon handles graceful container swaps.

### Automatic Deployment
Containers deploy instantly when you push new images. No manual deployment steps needed.

### Auto-Route Creation
Automatically create routes from image names containing domains. Push an image named like `myapp.mydomain.com:latest` and Gordon creates the route automatically when enabled.

## Quick Start (5 minutes)

### 1. Get a VPS, Install Podman and UFW
```bash
# Any VPS provider: DigitalOcean, Linode, Vultr, Hetzner
# Ubuntu/Debian recommended

# Install Essentials 
sudo apt update
sudo apt install -y podman ufw

# Configure UFW firewall first
sudo ufw --force enable
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```
### 2. Redirect Ports 80/443 to 8080
```bash
# Simple iptables redirect
sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8080
sudo iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 8080

# Make rules persistent across reboots
sudo apt install -y iptables-persistent
sudo netfilter-persistent save
```
### 3. Enable rootless mode for enhanced security
```
echo 'user.max_user_namespaces=28633' | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Configure user for rootless containers
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER

# Start user services (no root required!)
systemctl --user enable --now podman.socket
```

### 4. Configure registries for your Gordon registry
```
mkdir -p ~/.config/containers
tee ~/.config/containers/registries.conf > /dev/null <<EOF
[registries.search]
registries = ['docker.io', 'registry.mydomain.com']

[registries.insecure]
registries = ['registry.mydomain.com']

[registries.block]
registries = []
EOF
```

### 5. Install Gordon
```bash
wget https://github.com/bnema/gordon/releases/latest/download/gordon-linux-amd64
chmod +x gordon-linux-amd64
sudo mv gordon-linux-amd64 /usr/local/bin/gordon
```

### 6. Create Config file
```toml
# Gordon searches for `gordon.toml` config in: ./ → ~/.config/gordon/ → ~/.gordon/ → ~/ → /etc/gordon/
# (or use --config flag)
[server]
port = 8080
registry_domain = "registry.mydomain.com"
runtime = "podman-rootless"  # Can be auto, docker, podman, podman-rootless

[registry_auth]
enabled = true
username = "admin"
password = "your-secure-password"

[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "api:v1"
"blog.mydomain.com" = "wordpress:latest"

[auto_route]
enabled = false  # Enable automatic route creation

# Custom config file location (optional):
# gordon --config /path/to/custom.toml start
```

### 7. Point Cloudflare DNS
```
A    *.mydomain.com    →    YOUR_VPS_IP
A    mydomain.com      →    YOUR_VPS_IP
```

### 8. Create Systemd Service (Rootless)
```bash
# Create user systemd service (no root privileges needed!)
mkdir -p ~/.config/systemd/user

tee ~/.config/systemd/user/gordon.service > /dev/null <<EOF
[Unit]
Description=Gordon Container Platform (Rootless)
After=podman.socket
Requires=podman.socket

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

# Enable and start the user service
systemctl --user daemon-reload
systemctl --user enable --now gordon

# Enable lingering to start service on boot
sudo loginctl enable-linger $USER

# Check service status
systemctl --user status gordon
```

### 8. Deploy Your First App
```bash
# Now from your local development machine:
# Authenticate with your Gordon registry
podman login registry.mydomain.com
# Use the username/password from your gordon.toml config

# Deploy your first app (using podman)
podman tag myapp:latest registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest
# Visit https://app.mydomain.com
```

## Real-World Examples

### Deploy a Node.js App (The "Push-to-Deploy" Way)
This is the simplest way to deploy. Your production environment is always in sync with your `latest` tag.

```bash
# 1. Build and test locally first
podman build -t myapp .
podman run -p 3000:3000 myapp  # Test it!

# 2. In gordon.toml, point your domain to the 'latest' tag
# "app.mydomain.com" = "myapp:latest"

# 3. Tag your image as 'latest' for your registry
podman tag myapp registry.mydomain.com/myapp:latest

# 4. Push to deploy
podman push registry.mydomain.com/myapp:latest

# Gordon automatically detects the push and deploys the new 'latest' version.
```

### Smart Versioning Strategy
For more control, especially in production, you can use a combination of version tags and the `latest` tag. This makes rollbacks trivial and allows for staging environments.

```bash
# 1. Always push a specific version tag first. This creates a history.
podman tag myapp registry.mydomain.com/myapp:v1.0.1
podman push registry.mydomain.com/myapp:v1.0.1

# 2. Test the new version on a staging domain.
# In gordon.toml:
"staging.mydomain.com" = "myapp:v1.0.1"
# Get your production route ready :
"app.mydomain.com" = "myapp:latest"

# 3. Once tested and confirmed, promote it to production by updating 'latest'.
podman tag myapp registry.mydomain.com/myapp:latest
podman push registry.mydomain.com/myapp:latest
```

### Instant Rollback When Things Break
```toml
# Production broke after the latest push?
# Just edit gordon.toml to point to a previously pushed, stable version tag:

# From:
"app.mydomain.com" = "myapp:latest"

# To:
"app.mydomain.com" = "myapp:v1.0.0"

# Save the file. Gordon redeploys the stable version in seconds. No scripts, no drama.
```

### Auto-Route Creation for Testing

Gordon can automatically create routes from image names that contain valid domain names. This is perfect for testing deployments without manually editing config files.

#### Enable Auto-Routes
```toml
[auto_route]
enabled = true  # Default: false
```

#### How It Works
When you push an image with a domain name as the image name, Gordon automatically creates a route:

```bash
# Build your app normally
podman build -t myapp .

# Tag with domain name as the image name
podman tag myapp:latest registry.mydomain.com/myapp.mydomain.dev:latest
podman push registry.mydomain.com/myapp.mydomain.dev:latest

# Gordon automatically creates:
# "myapp.mydomain.dev" = "myapp.mydomain.dev:latest"
# The route is added to your config file and deployed instantly!
```

#### Perfect for Testing Workflows
```bash
# Test different domains for the same app
podman build -t myapp .

# Push to different test domains using image names
podman tag myapp:latest registry.mydomain.com/staging.example.com:latest
podman push registry.mydomain.com/staging.example.com:latest

podman tag myapp:latest registry.mydomain.com/demo.example.com:v1.0.0
podman push registry.mydomain.com/demo.example.com:v1.0.0

# Both automatically get their own routes:
# staging.example.com -> staging.example.com:latest
# demo.example.com -> demo.example.com:v1.0.0
```

#### Domain Validation
Gordon only creates auto-routes for valid domain names in image names:
- ✅ `api.example.com:latest` → Valid subdomain name, route created
- ✅ `myapp.dev:v1.0.0` → Valid domain name, route created  
- ❌ `myapp:latest` → Not a domain, ignored
- ❌ `myapp:v1.0.0` → Not a domain, ignored
- ❌ `localhost:latest` → Not a valid domain, ignored

#### Route Precedence
- Existing routes in `[routes]` are never overwritten
- Auto-routes are saved to your config file permanently
- You can manually edit or remove auto-created routes anytime


## FAQ

**Q: How is this different from Traefik/Nginx Proxy Manager?**  
A: Those are just reverse proxies - you still need to manually start/stop containers. Gordon is a complete deployment platform: built-in registry, automatic deployment on push, and container lifecycle management. It's the difference between a router and a full PaaS.

**Q: Where do builds happen?**  
A: On YOUR machine. If it runs locally, it'll run in production. No mysterious build server issues.

**Q: How do I handle broken deployments?**  
A: Just edit gordon.toml to point to a previous version. Rollback takes seconds, not minutes.

**Q: Do I need CI/CD?**  
A: Nope! Your computer is the CI/CD. Build, test locally, push when ready. Keep it stupid simple.

**Q: Do I need Cloudflare?**  
A: Yes for SSL certificates. Gordon doesn't handle Let's Encrypt yet, so Cloudflare provides the SSL termination and DDoS protection.

**Q: Can I run databases?**  
A: You can in theory, but exposing databases on the internet is risky. Database/app isolation is coming soon for safer database deployments.

**Q: Resource requirements?**  
A: Runs comfortably on 1GB RAM VPS. Gordon itself uses <15MB of RAM.

**Q: What about secrets?**  
A: Gordon provides built-in environment variable support with secret management via `pass` and `sops`. See the Environment Variables section below.

**Q: How does auto-route creation work?**  
A: When enabled, Gordon watches for pushed images with domain names as image names (like `staging.example.com:latest`) and automatically creates routes. Perfect for testing deployments without manual config edits.

## Philosophy: Build Local, Deploy Simple

### Why No Build Servers?

1. **Your machine already works** - Why replicate your dev environment in CI/CD?
2. **Instant feedback** - Build errors show up immediately, not after pushing
3. **Perfect reproducibility** - "Works on my machine" becomes a feature, not a bug
4. **Zero build queues** - Deploy as fast as your internet can push

### The Gordon Way

```bash
# Traditional CI/CD
push code → wait for build → hope it works → debug remotely → repeat

# The Gordon Way (with Podman)
podman build locally → test locally → push image → instant deploy
```

## Architecture

- **Event-Driven**: Push events trigger deployments automatically
- **Config Hot-Reload**: Edit gordon.toml, changes apply instantly
- **Single Binary**: No dependencies except Docker
- **Stateless**: Configuration is the only state
- **Fast**: Written in Go for minimal overhead

## Advanced Usage

### Container Runtime Configuration
```toml
[server]
runtime = "auto"  # auto, docker, podman, podman-rootless
socket_path = ""  # optional custom socket path

# Examples:
# runtime = "docker"
# runtime = "podman"
# socket_path = "unix:///run/user/1000/podman/podman.sock"
```

### Environment Override
```bash
# Override container socket with environment variable
export CONTAINER_HOST=unix:///custom/path/container.sock
export CONTAINER_HOST=tcp://remote-docker:2376

# Works for both Docker and Podman
gordon start
```

### Runtime Auto-Detection
Gordon automatically detects available container runtimes in this order:
1. **Docker** (`/var/run/docker.sock`)
2. **Podman rootless** (`$XDG_RUNTIME_DIR/podman/podman.sock`)
3. **Podman root** (`/run/podman/podman.sock`) 

### Environment Variables & Secret Management

Gordon supports per-route environment variables with secure secret management.

#### Configuration
```toml
[env]
# Directory where .env files are stored for each route
dir = "./data/env"  # Default location
# Secret providers for secure credential management
providers = ["pass", "sops"]  # Unix password manager and SOPS
```

#### Creating Environment Files
Gordon automatically creates empty `.env` files for all configured routes when it starts up. The files are named after the domain with special characters replaced by underscores:

```bash
# Gordon automatically creates these files for you:
# For route "app.mydomain.com"
./data/env/app_mydomain_com.env

# For route "api-v2.mydomain.com" 
./data/env/api-v2_mydomain_com.env
```

#### Environment File Format
When Gordon creates env files automatically, they include helpful comments and examples.

**Security:** Gordon creates the env directory with `0700` permissions and env files with `0600` permissions (owner read/write only) to protect sensitive information.

```bash
# ./data/env/app_mydomain_com.env (automatically created by Gordon)
# Environment variables for route: app.mydomain.com
# Image: myapp:latest

NODE_ENV=production
PORT=3000
DATABASE_URL=postgresql://localhost:5432/prod

# Secret references (resolved at container start)
API_KEY=${pass:company/api-key}
JWT_SECRET=${sops:secrets.yaml:app.jwt_secret}
```

#### Secret Providers

**Unix Password Manager (pass)**
```bash
# Store secrets with pass
pass insert company/api-key
pass insert company/database-password

# Reference in .env files
API_KEY=${pass:company/api-key}
DB_PASSWORD=${pass:company/database-password}
```

**SOPS (Secrets OPerationS)**
```bash
# Create encrypted YAML file
sops secrets.yaml

# secrets.yaml content:
# app:
#   jwt_secret: "super-secret-key"
#   database_url: "postgresql://user:pass@db:5432/prod"

# Reference in .env files
JWT_SECRET=${sops:secrets.yaml:app.jwt_secret}
DATABASE_URL=${sops:secrets.yaml:app.database_url}
```

#### How It Works
1. Gordon loads the `.env` file for each route during container deployment
2. Secret references (`${provider:path}`) are resolved using the configured providers
3. Environment variables are passed to the container at startup
4. Missing env files are silently ignored (optional env vars)
5. Secret resolution failures cause deployment to fail for security

#### Examples

**Development Setup**
```toml
# gordon.toml
[env]
dir = "./dev-data/env"
providers = ["pass"]  # Optional for dev secrets

[routes]
"app.local" = "myapp:latest"
```

```bash
# ./dev-data/env/app_local.env
NODE_ENV=development
DEBUG=true
DATABASE_URL=postgresql://localhost:5432/dev
API_KEY=${pass:dev/api-key}  # Optional: use pass for dev secrets
```

**Production Setup**
```toml
# gordon.toml
[env]
dir = "/var/lib/gordon/env"
providers = ["pass", "sops"]

[routes]
"app.company.com" = "company-app:v2.1.0"
```

```bash
# /var/lib/gordon/env/app_company_com.env
NODE_ENV=production
DATABASE_URL=postgresql://user:${pass:company/db-password}@db:5432/prod
API_KEY=${sops:company-secrets.yaml:api.key}
JWT_SECRET=${sops:company-secrets.yaml:jwt.secret}
```

### Port Binding
Gordon requires your Dockerfile to explicitly expose ports using the `EXPOSE` instruction. When multiple ports are exposed, Gordon will use the first exposed port for HTTP traffic.

Example Dockerfile:
```
ENTRYPOINT ["/myapp"]
EXPOSE 8080
```

### Registry Operations
```bash
# List images
curl -u admin:password https://registry.mydomain.com/v2/_catalog

# List tags
curl -u admin:password https://registry.mydomain.com/v2/myapp/tags/list
```

## Contributing

Gordon is open source and we love contributions! Check out our [issues](https://github.com/yourusername/gordon/issues) or submit a PR.

## License

GPL-3.0 - Use freely, contribute back.

---

<div align="center">
  <p><strong>Stop overcomplicating container deployment.</strong></p>
  <p>Star this repo if Gordon saves you money!</p>
</div>