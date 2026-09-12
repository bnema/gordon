# Production Configuration

A complete Gordon configuration for production environments.

## When to Use

- Production deployments
- Multiple applications
- Services requiring databases

## Configuration

```toml
# ~/.config/gordon/gordon.toml

# Server settings
[server]
port = 8080                              # Cloudflare forwards 443 → 8080
registry_port = 5000
gordon_domain = "gordon.company.com"

# Authentication with pass backend (recommended)
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"

# File-based logging with rotation
[logging]
level = "info"
format = "json"

[logging.file]
enabled = true
path = "~/.gordon/logs/gordon.log"
max_size = 100
max_backups = 10
max_age = 90

# Workload logs are read from the container runtime with
# `gordon apps logs APP --service SERVICE`.

# Environment directory
[env]
dir = "~/.gordon/env"

# Volume settings (all enabled by default)
[volumes]
auto_create = true
prefix = "gordon"
preserve = true

# Network isolation for security
[network_isolation]
enabled = true
network_prefix = "prod"

# Applications, routes, services, and shared networks are declared in
# separate app manifest files and applied with `gordon apps apply`.
```

## Setup Steps

### 1. Install Pass

```bash
sudo apt install pass gnupg
gpg --gen-key
pass init your-gpg-key-id
```

### 2. Store Token Secret

```bash
# Generate random secret
openssl rand -base64 32 | pass insert -m gordon/auth/token_secret
```

### 3. Generate CI Token

```bash
gordon auth token generate --subject ci-bot --scopes push,pull --expiry 0
```

### 4. Create Environment Files

```bash
# App environment
cat > ~/.gordon/env/app_company_com.env <<EOF
NODE_ENV=production
PORT=3000
DATABASE_URL=postgresql://company-postgres:5432/app
DATABASE_PASSWORD=\${pass:company/db-password}
REDIS_URL=redis://company-redis:6379
EOF

# API environment
cat > ~/.gordon/env/api_company_com.env <<EOF
NODE_ENV=production
PORT=8080
DATABASE_URL=postgresql://company-postgres:5432/api
DATABASE_PASSWORD=\${pass:company/db-password}
REDIS_URL=redis://company-redis:6379
EOF
```

### 5. Configure Cloudflare

| Type | Name | Content | Proxy |
|------|------|---------|-------|
| A | `app` | VPS IP | Yes |
| A | `api` | VPS IP | Yes |
| A | `admin` | VPS IP | Yes |
| A | `registry` | VPS IP | Yes |

### 6. Start Gordon

```bash
systemctl --user enable --now gordon
```

## Features Enabled

| Feature | Status |
|---------|--------|
| Registry | Enabled |
| Token Auth | Enabled |
| File Logging | Enabled with rotation |
| Container Logs | Enabled with rotation |
| Network Isolation | Enabled |
| Attachments | Configured |
| Secrets (pass) | Enabled |

## Deployment Workflow

```bash
# Build locally
docker build -t company-app .

# Tag with version
docker tag company-app gordon.company.com/company-app:v2.2.0

# Push to deploy
docker push gordon.company.com/company-app:v2.2.0

# Update config with new version
vim ~/.config/gordon/gordon.toml
# Change: "app.company.com" = "company-app:v2.2.0"

# Reload to deploy
gordon reload
```

## Related

- [Minimal Configuration](./minimal.md)
- [Secrets Configuration](/docs/config/secrets.md)
- [Network Isolation](/docs/config/network-isolation.md)
