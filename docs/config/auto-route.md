# Auto Route Configuration

Automatically create routes from image names that contain domains.

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
registry_domain = "registry.local"

[auto_route]
enabled = true

# No routes defined - all come from image names
```

Usage:
```bash
docker push registry.local/myapp.local:latest
docker push registry.local/api.local:latest
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

## Limitations

- Auto-routes always use the pushed image tag
- No support for HTTP-only routes (all HTTPS)
- No automatic attachment configuration
- Cannot pin versions (always uses pushed tag)

For more control, define routes manually in `[routes]`.

## Related

- [Routes](./routes.md)
- [Configuration Overview](./index.md)
