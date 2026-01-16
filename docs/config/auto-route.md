# Auto Route Configuration

Gordon supports two methods for automatic route creation:

1. **Image Name Detection** - Routes from domain-like image names
2. **Image Labels** - Routes from Dockerfile labels (recommended)

## Configuration

```toml
[auto_route]
enabled = true
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `false` | Enable automatic route creation |

## How It Works

When auto-route is enabled, Gordon automatically creates routes for images with domain-like names:

```bash
# Push an image with a domain name
docker push registry.mydomain.com/app.example.com:latest
```

Gordon automatically:
1. Detects `app.example.com` looks like a domain
2. Creates a route: `app.example.com` → `app.example.com:latest`
3. Deploys the container
4. Routes traffic to it

## Domain Detection

Gordon recognizes image names as domains when they:

- Contain at least one dot (`.`)
- Don't start or end with a dot
- Look like valid hostnames

| Image Name | Detected as Domain? |
|------------|-------------------|
| `app.example.com` | Yes |
| `myapp.dev` | Yes |
| `staging.api.io` | Yes |
| `myapp` | No (no dots) |
| `.invalid` | No (starts with dot) |
| `myapp.` | No (ends with dot) |

## Use Cases

### Simplified Deployment

Without auto-route:
```toml
# Must define each route in config
[routes]
"app.example.com" = "app.example.com:latest"
```

With auto-route:
```toml
[auto_route]
enabled = true
# No routes needed - just push!
```

```bash
docker push registry.mydomain.com/app.example.com:latest
# Route automatically created
```

### Development Workflow

```bash
# Create a new app instantly
docker build -t myapp .
docker tag myapp registry.mydomain.com/newapp.mydomain.com:latest
docker push registry.mydomain.com/newapp.mydomain.com:latest
# newapp.mydomain.com is now live!
```

### Multi-Tenant SaaS

```bash
# Quickly provision customer subdomains
docker push registry.mydomain.com/acme.saas.com:latest
docker push registry.mydomain.com/beta.saas.com:latest
docker push registry.mydomain.com/gamma.saas.com:latest
```

## Combining with Manual Routes

Auto-route works alongside manual routes:

```toml
[auto_route]
enabled = true

[routes]
# Pinned versions override auto-route
"api.mydomain.com" = "myapi:v2.1.0"
```

- `api.mydomain.com` uses the manually configured `myapi:v2.1.0`
- Other domain-named images create automatic routes

## Priority Order

1. **Manual routes** in `[routes]` take precedence
2. **Auto-routes** are created for domain-named images without manual routes

## Examples

### Enable Auto-Route

```toml
[auto_route]
enabled = true
```

### Development with Auto-Route

```toml
[server]
gordon_domain = "gordon.local"

[auto_route]
enabled = true

# No routes defined - all come from image names
```

Usage:
```bash
docker push gordon.local/myapp.local:latest
docker push gordon.local/api.local:latest
# Both routes created automatically
```

### Production with Auto-Route

```toml
[auto_route]
enabled = true

[routes]
# Critical services with pinned versions
"api.company.com" = "company-api:v2.1.0"
"app.company.com" = "company-app:v1.5.0"

# Other services can use auto-route
```

## Limitations (Image Name Detection)

- Auto-routes always use the pushed image tag
- No support for HTTP-only routes (all HTTPS)
- No automatic attachment configuration
- Cannot pin versions (always uses pushed tag)

For more control, define routes manually in `[routes]` or use image labels.

---

## Image Labels (Recommended)

The preferred method for automatic routing uses Dockerfile labels. This gives you full control over routing behavior directly in your application.

### Supported Labels

| Label | Description | Example |
|-------|-------------|---------|
| `gordon.domains` | Comma-separated list of domains | `app.example.com,www.app.example.com` |
| `gordon.port` | Container port to proxy | `3000` |
| `gordon.health` | Health check endpoint path | `/health` |
| `gordon.env-file` | Path to .env file in image | `/app/.env.example` |

### Dockerfile Example

```dockerfile
FROM node:20-alpine
WORKDIR /app
COPY . .
RUN npm install

# Gordon routing labels
LABEL gordon.domains="myapp.example.com,www.myapp.example.com"
LABEL gordon.port="3000"
LABEL gordon.health="/api/health"
LABEL gordon.env-file="/app/.env.example"

EXPOSE 3000
CMD ["npm", "start"]
```

### How It Works

When you push an image with Gordon labels:

1. Gordon reads the `gordon.domains` label
2. Creates routes for each domain automatically
3. Uses `gordon.port` for the proxy target (or first EXPOSE port)
4. Configures health checks if `gordon.health` is set
5. Extracts environment variables from `gordon.env-file` if specified

### Label Priority

Labels take precedence over image name detection:

1. **Labels present** → Use `gordon.domains` for routing
2. **No labels** → Fall back to image name detection (if enabled)
3. **Manual routes** → Always take highest priority

### Multi-Domain Example

```dockerfile
# Single image serving multiple domains
LABEL gordon.domains="api.example.com,api.staging.example.com"
```

Both domains will route to the same container.

### Environment File Extraction

The `gordon.env-file` label tells Gordon where to find a template `.env` file:

```dockerfile
# Include a .env.example in your image
COPY .env.example /app/.env.example
LABEL gordon.env-file="/app/.env.example"
```

Gordon will:
1. Extract the file from the image
2. Store it in the env directory
3. Use the variables for container deployment

### Best Practices

1. **Always set `gordon.port`** - Don't rely on EXPOSE detection
2. **Use `gordon.health`** - Enables reliable deployment verification
3. **Include `.env.example`** - Self-documenting environment requirements
4. **Separate domains with commas** - No spaces around commas

### Example: Complete Setup

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o server

FROM alpine:3.21
WORKDIR /app
COPY --from=builder /app/server .
COPY .env.example .

# Gordon labels
LABEL gordon.domains="api.mycompany.com"
LABEL gordon.port="8080"
LABEL gordon.health="/health"
LABEL gordon.env-file="/app/.env.example"

EXPOSE 8080
CMD ["./server"]
```

```bash
# Push and Gordon handles everything
docker push registry.mycompany.com/api:latest
# Route automatically created: api.mycompany.com → container:8080
```

## Related

- [Routes](./routes.md)
- [Configuration Overview](./index.md)
