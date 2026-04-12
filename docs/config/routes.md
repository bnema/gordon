# Routes Configuration

Routes map hostnames to container images.

## Configuration

```toml
[routes]
"app.mydomain.com" = { image = "myapp:latest" }
"api.mydomain.com" = { image = "myapi:v2.1.0" }
"admin.mydomain.com" = { image = "admin-panel:v1.0.0" }
```

## Syntax

```toml
[routes]
"<domain>" = { image = "<image>:<tag>" }
```

| Component | Description |
|-----------|-------------|
| `domain` | Public, fully qualified domain name |
| `image` | Full container image reference, including tag |
| `https` | Optional; add `false` for HTTP-only routes |

Legacy `http://...` route keys are still read for backward compatibility and rewritten on the next save.

## Route Types

### HTTPS Routes (Default)

Standard routes expect HTTPS traffic (terminated by Cloudflare or similar):

```toml
[routes]
"app.mydomain.com" = { image = "myapp:latest" }
```

Route domains must be plain hostnames. Gordon rejects `http://` and `https://` prefixes, `.local` and `.internal` suffixes, localhost names, and IP literals.

### Development Routes

For local testing, use a hostname you can resolve yourself:

```toml
[routes]
"dev-app.example.com" = { image = "internal-app:latest", https = false }
```

## Version Strategies

### Latest Tag

Always deploy the most recent push:

```toml
[routes]
"app.mydomain.com" = { image = "myapp:latest" }
```

### Pinned Version

Deploy a specific version:

```toml
[routes]
"app.mydomain.com" = { image = "myapp:v2.1.0" }
```

Update the config to deploy a new version:

```toml
[routes]
"app.mydomain.com" = { image = "myapp:v2.2.0" }  # Changed
```

### Semantic Versioning

Use different routes for different versions:

```toml
[routes]
"app.mydomain.com" = { image = "myapp:v2.1.0" }        # Production
"staging.mydomain.com" = { image = "myapp:staging" }    # Staging
"canary.mydomain.com" = { image = "myapp:canary" }      # Canary
```

## Multiple Routes

### Same Image, Different Domains

```toml
[routes]
"app.mydomain.com" = { image = "myapp:latest" }
"www.mydomain.com" = { image = "myapp:latest" }
"mydomain.com" = { image = "myapp:latest" }
```

### Multiple Services

```toml
[routes]
"app.mydomain.com" = { image = "frontend:latest" }
"api.mydomain.com" = { image = "backend:v2.1.0" }
"docs.mydomain.com" = { image = "documentation:latest" }
"status.mydomain.com" = { image = "status-page:v1.0.0" }
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

## Route Changes

Route changes in the config file are hot-reloaded automatically:

1. Edit `[routes]` in `gordon.toml`
2. Save the file
3. Gordon reloads the updated routes and proxy config

Legacy `http://...` route keys are still read here for backward compatibility and rewritten the next time Gordon saves the config.

You can still manage routes with the API or CLI if you prefer live mutations:

```bash
gordon routes add newapp.mydomain.com newapp:latest
gordon routes add app.mydomain.com myapp:v2.2.0
gordon routes remove oldapp.mydomain.com
```

## Examples

### Development Setup

```toml
[routes]
"dev-app.example.com" = { image = "myapp:latest" }
"dev-api.example.com" = { image = "myapi:latest" }
```

Add to `/etc/hosts`:

```text
127.0.0.1  dev-app.example.com dev-api.example.com registry.example.com
```

### Production Setup

```toml
[routes]
"app.company.com" = { image = "company-app:v2.1.0" }
"api.company.com" = { image = "company-api:v1.5.2" }
"admin.company.com" = { image = "admin-panel:v1.0.1" }
"docs.company.com" = { image = "company-docs:latest" }
```

### Multi-Tenant SaaS

```toml
[routes]
# Platform services
"app.saas-platform.com" = { image = "saas-frontend:v2.1.0" }
"api.saas-platform.com" = { image = "saas-api:v3.2.1" }

# Customer subdomains
"acme.saas-platform.com" = { image = "saas-app:v2.1.0" }
"beta.saas-platform.com" = { image = "saas-app:v2.1.0" }

# Customer custom domains
"portal.acme-corp.com" = { image = "saas-app:v2.1.0" }
```

## External Services

For non-containerized services (databases, legacy apps, etc.), see [External Routes](./external-routes.md).

## Related

- [Configuration Overview](./index.md)
- [Auto Route](./auto-route.md)
- [Attachments](./attachments.md)
- [External Routes](./external-routes.md)
