# AI Agent Deployment Guide

This guide provides a structured workflow for AI coding assistants to help users deploy applications with Gordon.

## Deployment Workflow

### Key Paths (VPS)

```
~/.config/gordon/gordon.toml    # Main config (routes, registry)
~/.gordon/env/                  # Env files per domain
~/.gordon/logs/containers/      # Container logs
```

### Step 1: Add route in gordon.toml

```toml
[routes]
"app.example.com" = "my-app:latest"
```

### Step 2: Create env file

Naming convention: domain with dots replaced by underscores.

Example: `app.example.com` → `~/.gordon/env/app_example_com.env`

```bash
cat > ~/.gordon/env/app_example_com.env << 'EOF'
NODE_ENV=production
PUBLIC_API_URL=https://api.example.com
EOF
```

### Step 3: Generate registry token (on VPS)

```bash
gordon auth token generate --subject username --expiry 720h
```

### Step 4: Login to registry (local machine)

```bash
echo "TOKEN" | docker login -u username --password-stdin reg.example.com:5000
```

### Step 5: Build and push

Tag images to match git tags + latest for bleeding edge:

```bash
# Get current git tag (falls back to "latest" if no tag)
TAG=$(git describe --tags --exact-match 2>/dev/null || echo "latest")

# Build with version tag and latest
docker buildx build --platform linux/amd64 \
  -t reg.example.com:5000/my-app:$TAG \
  -t reg.example.com:5000/my-app:latest \
  --push .
```

Gordon auto-deploys when it receives the pushed image.

### Step 6: Verify

```bash
curl -I https://app.example.com
ssh user@vps "cat ~/.gordon/logs/containers/app_example_com.log"
```

## Framework Notes

### SvelteKit / Next.js

Static env vars (`$env/static/public`, `NEXT_PUBLIC_*`) must be available at build time:

```dockerfile
ARG PUBLIC_API_URL
ENV PUBLIC_API_URL=$PUBLIC_API_URL
```

Build with:
```bash
docker buildx build --build-arg PUBLIC_API_URL=https://api.example.com --push .
```

## Secrets with pass

Use pass integration for sensitive values:

```bash
DATABASE_PASSWORD=${pass:gordon/app/db_password}
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| HTTPS error on push | Add registry to `insecure-registries` in daemon.json |
| Container not starting | Check env file name matches domain pattern |
| Env vars not working | SvelteKit static env = build args, not runtime env |
