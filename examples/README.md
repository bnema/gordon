# Gordon Configuration Examples

This directory contains configuration examples for different use cases and environments.

## Full Configuration Reference

Here's the complete Gordon configuration with all default values:

```toml
# gordon.toml - Complete configuration reference with defaults

[server]
port = 8080                          # HTTP/HTTPS proxy port (default: 8080)
registry_port = 5000                 # Container registry port (default: 5000)
registry_domain = ""                 # REQUIRED - Your registry domain (no default)
runtime = "auto"                     # Container runtime (default: "auto")
                                    # Options: auto, docker, podman, podman-rootless
socket_path = ""                     # Custom socket path (default: "" - auto-detected)
data_dir = "~/.local/share/gordon"   # Data directory (default: ~/.local/share/gordon)
                                    # Root user default: ./data
ssl_email = ""                       # Email for Let's Encrypt (default: "" - uses Cloudflare)

[registry_auth]
enabled = true                       # Enable registry authentication (default: true)
username = ""                        # REQUIRED when enabled (no default)
password = ""                        # REQUIRED when enabled (no default)

[routes]
# Domain to image mappings (default: empty)
# "app.example.com" = "myapp:latest"
# "api.example.com" = "myapi:v1.0.0"

[network_groups]
# Group domains for shared networking (default: empty)
# "backend" = ["app.example.com", "api.example.com"]
# "monitoring" = ["grafana.example.com", "prometheus.example.com"]

[attachments]
# Attach services to apps or groups (default: empty)
# "app.example.com" = ["postgres:latest", "redis:latest"]
# "backend" = ["shared-cache:latest", "message-queue:latest"]

[auto_route]
enabled = false                      # Auto-create routes from image names (default: false)

[volumes]
auto_create = true                   # Auto-create volumes from VOLUME directives (default: true)
prefix = "gordon"                    # Volume name prefix (default: "gordon")
preserve = true                      # Keep volumes when removing containers (default: true)

[env]
dir = "{data_dir}/env"              # Environment files directory (default: {data_dir}/env)
providers = ["pass", "sops"]        # Secret providers (default: ["pass", "sops"])

[logging]
enabled = true                       # Enable file logging (default: true)
level = "info"                      # Log level (default: "info")
                                    # Options: trace, debug, info, warn, error, fatal, panic
dir = "{data_dir}/logs"             # Log directory (default: {data_dir}/logs)
main_log_file = "gordon.log"        # Main log file (default: "gordon.log")
proxy_log_file = "proxy.log"        # Proxy log file (default: "proxy.log")
container_log_dir = "containers"    # Container logs subdirectory (default: "containers")
max_size = 100                      # Max log size in MB (default: 100)
max_backups = 3                     # Number of old logs to keep (default: 3)
max_age = 28                        # Days to keep old logs (default: 28)
compress = true                     # Compress rotated logs (default: true)
```

## 📁 Available Examples

### 🚀 [`minimal.toml`](minimal.toml)
**Perfect for getting started**
- Single route configuration
- Default settings
- No authentication
- Ideal for testing and learning

### 🏠 [`development.toml`](development.toml)
**Local development setup**
- Multiple .local domains
- No authentication for ease of use
- Third-party development tools
- localhost registry

### 🧪 [`staging.toml`](staging.toml)
**Staging and preview environments**
- Branch-based deployments
- Feature branch testing
- PR preview environments
- Separate staging registry

### 🏭 [`production.toml`](production.toml)
**Production-ready configuration**
- Pinned image versions
- Registry authentication
- Multiple production services
- Monitoring tools included

### 🏢 [`saas-multi-tenant.toml`](saas-multi-tenant.toml)
**Multi-tenant SaaS platform**
- Customer subdomains
- Custom domains
- Shared application architecture
- Enterprise features

### 📊 [`logging.toml`](logging.toml)
**Comprehensive logging configuration**
- Complete logging setup examples
- Different logging levels and configurations
- Production and development logging strategies
- Log rotation and monitoring examples

### 🔄 [`rollback-workflow-example.md`](rollback-workflow-example.md)
**Simple version deployment and rollback workflow**
- Manifest annotation-based deployments
- Simple version changes for deployments and rollbacks
- Version tracking and deployment strategies

## 🚀 Quick Start

1. **Choose an example** that matches your use case
2. **Copy the config file** to your Gordon directory:
   ```bash
   cp examples/minimal.toml gordon.toml
   ```
3. **Edit the configuration** with your domains and settings
4. **Start Gordon**:
   ```bash
   gordon start
   ```

## 🔧 Customization Tips

### Domain Configuration
Update the `[routes]` section with your actual domains:
```toml
[routes]
"your-domain.com" = "your-app:latest"
"api.your-domain.com" = "your-api:v1.0.0"
```

### Registry Setup
Configure your registry domain and authentication:
```toml
[server]
registry_domain = "registry.your-domain.com"

[registry_auth]
enabled = true
username = "your-username"
password = "your-secure-password"
```

### Container Runtime Setup
Gordon supports Docker and Podman with automatic detection:
```toml
[server]
runtime = "auto"  # auto, docker, podman, podman-rootless
socket_path = ""  # optional custom socket path

# Examples:
# runtime = "docker"          # Force Docker
# runtime = "podman"          # Force Podman root
# runtime = "podman-rootless" # Force Podman rootless
# socket_path = "unix:///run/user/1000/podman/podman.sock"
```

Override with environment variables:
```bash
# Works for both Docker and Podman
export CONTAINER_HOST=unix:///custom/path/container.sock
export CONTAINER_HOST=tcp://remote-docker:2376
gordon start
```

### Logging Configuration
Enable comprehensive logging to monitor your deployments:
```toml
[logging]
enabled = true                    # Enable file-based logging
level = "info"                   # Log level: trace, debug, info, warn, error
dir = "./logs"                   # Directory for log files
main_log_file = "gordon.log"     # Main application logs
proxy_log_file = "proxy.log"     # HTTP proxy traffic logs
container_log_dir = "containers" # Container logs subdirectory

# Log rotation settings
max_size = 100                   # Max log file size in MB
max_backups = 5                  # Number of old log files to keep
max_age = 30                     # Max age in days
compress = true                  # Compress old log files
```

See [`logging.toml`](logging.toml) for comprehensive logging examples.

### Environment Variables
Gordon intelligently merges environment variables from multiple sources:

1. **Dockerfile ENV directives** (automatically detected)
2. **Your .env files** (override Dockerfile ENV)
3. **Runtime environment** (lowest priority)

#### Dockerfile ENV (Automatic Detection)
```dockerfile
FROM node:18-alpine
WORKDIR /app

# Gordon automatically reads these ENV directives
ENV NODE_ENV=production
ENV PORT=3000
ENV LOG_LEVEL=info
ENV DATABASE_HOST=localhost

COPY package*.json ./
RUN npm install
COPY . .
EXPOSE 3000
CMD ["npm", "start"]
```

#### Custom Environment Files
Create `.env` files for each route: `./data/env/app_yourdomain_com.env`
```bash
# Your custom overrides (these take precedence over Dockerfile ENV)
DATABASE_HOST=db.mydomain.com
DATABASE_USER=myapp
DATABASE_PASSWORD=${pass:myapp/db-password}
API_KEY=${sops:secrets.yaml:api_key}
# NODE_ENV, PORT, LOG_LEVEL from Dockerfile are preserved
```

#### Final Applied Environment
```bash
NODE_ENV=production          # From Dockerfile ENV
PORT=3000                    # From Dockerfile ENV
LOG_LEVEL=info              # From Dockerfile ENV
DATABASE_HOST=db.mydomain.com # From .env (overridden)
DATABASE_USER=myapp         # From .env (new)
DATABASE_PASSWORD=secret123  # From .env via pass
API_KEY=abc123              # From .env via SOPS
```

#### Production Environment Variables
For sensitive production data:
```bash
export GORDON_REGISTRY_PASSWORD="your-secure-password"
export GORDON_SSL_EMAIL="admin@your-domain.com"
```

### Volume Management
Gordon automatically creates persistent volumes from Dockerfile VOLUME directives:

#### Dockerfile VOLUME (Automatic Detection)
```dockerfile
FROM postgres:15
# Gordon automatically creates persistent volumes for these paths
VOLUME ["/var/lib/postgresql/data"]

# Multi-volume example
FROM myapp:latest
VOLUME ["/app/data", "/app/logs", "/app/uploads"]
```

#### Volume Features
- **Zero Configuration**: Reads VOLUME directives automatically
- **Persistent Storage**: Data survives container restarts and updates
- **Predictable Naming**: `gordon-{domain}-{path-hash}` format
- **Cross-Platform**: Works with Docker and Podman

#### Volume Configuration (Optional)
```toml
[volumes]
auto_create = true    # Default: true (automatically handle VOLUME directives)
prefix = "gordon"     # Default: "gordon" (volume name prefix)
preserve = true       # Default: true (keep volumes when containers are removed)
```

#### Complete Dockerfile Example
```dockerfile
FROM node:18-alpine
WORKDIR /app

# Environment variables (automatically merged with your .env files)
ENV NODE_ENV=production
ENV PORT=3000
ENV LOG_LEVEL=info

# Install dependencies
COPY package*.json ./
RUN npm install

# Copy application code
COPY . .

# Persistent storage (automatically managed by Gordon)
VOLUME ["/app/data", "/app/logs"]

# Network configuration
EXPOSE 3000

# Start command
CMD ["npm", "start"]
```

When you deploy this image:
- Gordon reads ENV directives and merges with your .env files
- Gordon creates volumes for `/app/data` and `/app/logs`
- Your data persists across deployments and reboots
- Environment variables are intelligently managed

## 🌐 DNS Configuration

### Cloudflare Setup (Recommended)
For all examples except development, set up these DNS records:

```
Type  Name                    Content
A     app.your-domain.com     YOUR_SERVER_IP
A     api.your-domain.com     YOUR_SERVER_IP  
A     registry.your-domain.com YOUR_SERVER_IP
```

Or use a wildcard:
```
Type  Name                    Content
A     *.your-domain.com       YOUR_SERVER_IP
```

### Local Development
For the development example, add to `/etc/hosts`:
```
127.0.0.1  app.local
127.0.0.1  api.local
127.0.0.1  admin.local
```

## 🔄 Workflow Examples

### Simple Deployment
```bash
# Build your app
export VERSION=latest
docker build --tag myapp:$VERSION --tag registry.your-domain.com/myapp:$VERSION .

# Push to deploy
docker push registry.your-domain.com/myapp:$VERSION
```

### Version Management
```bash
# Build and push specific version
export VERSION=v1.2.0
docker build --tag myapp:$VERSION --tag registry.your-domain.com/myapp:$VERSION .
docker push registry.your-domain.com/myapp:$VERSION

# Update config
# "app.your-domain.com" = "myapp:v1.2.0"
```

### Simple Version Deployment with Manifest Annotations
```bash
# Build and push your versions first
export VERSION=v1.1.0
podman build --tag myapp:$VERSION --tag registry.your-domain.com/myapp:$VERSION .
podman push registry.your-domain.com/myapp:$VERSION

export VERSION=v1.2.0
podman build --tag myapp:$VERSION --tag registry.your-domain.com/myapp:$VERSION .
podman push registry.your-domain.com/myapp:$VERSION

# Deploy v1.2.0
export VERSION=v1.2.0
podman manifest create myapp:latest
podman manifest add myapp:latest registry.your-domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.your-domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.your-domain.com/myapp:latest

# Rollback to v1.1.0 (just change the version)
export VERSION=v1.1.0
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.your-domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.your-domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.your-domain.com/myapp:latest
```

See [`rollback-workflow-example.md`](rollback-workflow-example.md) for complete version deployment workflow documentation.

## 🤝 Need Help?

- 📖 **Read the main [README](../README.md)** for detailed documentation
- 🔍 **Check the logs** for deployment issues
- 🐛 **Open an issue** if you find problems with these examples
- 💡 **Contribute** your own configuration examples!

---
*These examples are starting points - customize them for your specific needs!*