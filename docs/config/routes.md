# Routes Configuration

Routes map domains to container images.

## Configuration

```toml
[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "myapi:v2.1.0"
"admin.mydomain.com" = "admin-panel:v1.0.0"
```

## Syntax

```toml
[routes]
"<domain>" = "<image>:<tag>"
```

| Component | Description |
|-----------|-------------|
| `domain` | Fully qualified domain name |
| `image` | Container image name (as pushed to Gordon's registry) |
| `tag` | Image tag (version, `latest`, etc.) |

## Route Types

### HTTPS Routes (Default)

Standard routes expect HTTPS traffic (terminated by Cloudflare or similar):

```toml
[routes]
"app.mydomain.com" = "myapp:latest"
```

### HTTP-Only Routes

For internal or development routes without HTTPS:

```toml
[routes]
"http://internal.local" = "internal-app:latest"
```

The `http://` prefix tells Gordon not to expect HTTPS.

## Version Strategies

### Latest Tag

Always deploy the most recent push:

```toml
[routes]
"app.mydomain.com" = "myapp:latest"
```

### Pinned Version

Deploy a specific version:

```toml
[routes]
"app.mydomain.com" = "myapp:v2.1.0"
```

Update the config to deploy a new version:

```toml
[routes]
"app.mydomain.com" = "myapp:v2.2.0"  # Changed
```

### Semantic Versioning

Use different routes for different versions:

```toml
[routes]
"app.mydomain.com" = "myapp:v2.1.0"        # Production
"staging.mydomain.com" = "myapp:staging"    # Staging
"canary.mydomain.com" = "myapp:canary"      # Canary
```

## Multiple Routes

### Same Image, Different Domains

```toml
[routes]
"app.mydomain.com" = "myapp:latest"
"www.mydomain.com" = "myapp:latest"
"mydomain.com" = "myapp:latest"
```

### Multiple Services

```toml
[routes]
"app.mydomain.com" = "frontend:latest"
"api.mydomain.com" = "backend:v2.1.0"
"docs.mydomain.com" = "documentation:latest"
"status.mydomain.com" = "status-page:v1.0.0"
```

## How Routing Works

1. Request arrives for `app.mydomain.com`
2. Gordon looks up the route in configuration
3. Finds running container for `myapp:latest`
4. Proxies request to container's exposed port
5. Returns response to client

```
Client ─> Gordon Proxy ─> Container
          (port 80)       (exposed port)
```

## Deployment Flow

When you push an image:

```bash
docker push registry.mydomain.com/myapp:latest
```

1. Gordon receives the image
2. Checks routes for any that use `myapp:latest`
3. Deploys new container for each matching route
4. Updates proxy to route traffic to new container
5. Stops old container

## Hot Reload

Routes reload automatically when the config file changes:

```bash
# Edit config
vim ~/.config/gordon/gordon.toml

# Add new route
[routes]
"newapp.mydomain.com" = "newapp:latest"

# Save - Gordon reloads automatically
```

Or trigger manually:

```bash
gordon reload
```

## Examples

### Development Setup

```toml
[routes]
"app.local" = "myapp:latest"
"api.local" = "myapi:latest"
```

Add to `/etc/hosts`:
```
127.0.0.1  app.local api.local registry.local
```

### Production Setup

```toml
[routes]
"app.company.com" = "company-app:v2.1.0"
"api.company.com" = "company-api:v1.5.2"
"admin.company.com" = "admin-panel:v1.0.1"
"docs.company.com" = "company-docs:latest"
```

### Multi-Tenant SaaS

```toml
[routes]
# Platform services
"app.saas-platform.com" = "saas-frontend:v2.1.0"
"api.saas-platform.com" = "saas-api:v3.2.1"

# Customer subdomains
"acme.saas-platform.com" = "saas-app:v2.1.0"
"beta.saas-platform.com" = "saas-app:v2.1.0"

# Customer custom domains
"portal.acme-corp.com" = "saas-app:v2.1.0"
```

## External Services

For non-containerized services (databases, legacy apps, etc.), see [External Routes](./external-routes.md).

## Related

- [Configuration Overview](./index.md)
- [Auto Route](./auto-route.md)
- [Attachments](./attachments.md)
- [External Routes](./external-routes.md)
