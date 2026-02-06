# Attachments Configuration

Attach service dependencies (databases, caches, queues) to your applications.

## Requirements

**Network isolation must be enabled** for attachments to work properly:

```toml
[network_isolation]
enabled = true
```

Without network isolation, containers run on Docker's default bridge network which **does not provide DNS resolution**. Your application won't be able to reach attachments by hostname (e.g., `postgres:5432`).

## Configuration

```toml
[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
"api.mydomain.com" = ["postgres:latest"]
```

## Syntax

```toml
[attachments]
"<domain-or-group>" = ["<image>:<tag>", "<image>:<tag>"]
```

| Component | Description |
|-----------|-------------|
| `domain-or-group` | Route domain or network group name |
| `image:tag` | Service images to deploy alongside the app |

## How Attachments Work

When you define attachments:

1. Gordon deploys attachment containers to the same network as your app
2. Services are accessible by their image name (before the colon)
3. Attachments start before your main application
4. Attachments persist across app updates

```
┌───────────────────────────────────────────────────┐
│ Network: gordon-app-mydomain-com                  │
│                                                   │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐     │
│  │   App    │───>│ postgres │    │  redis   │     │
│  │  :3000   │    │  :5432   │    │  :6379   │     │
│  └──────────┘    └──────────┘    └──────────┘     │
│                                                   │
└───────────────────────────────────────────────────┘
```

## Service Discovery

Attachments are accessible by their image name within the network:

```toml
[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
```

In your application:
```javascript
// Connect using simple hostnames
const db = await pg.connect("postgresql://postgres:5432/mydb");
const cache = await redis.connect("redis://redis:6379");
```

## Persistent Storage

Use Dockerfile VOLUME directives for persistent data:

```dockerfile
# postgres.Dockerfile
FROM postgres:18
VOLUME ["/var/lib/postgresql/data"]
ENV POSTGRES_DB=myapp
ENV POSTGRES_USER=app
# Password is injected via attachment secrets — do NOT hardcode here
```

Configure credentials via attachment secrets instead of hardcoding them:

```bash
# Set database password securely
gordon secrets set app.mydomain.com --attachment postgres POSTGRES_PASSWORD=secret

# Verify
gordon secrets list app.mydomain.com
```

This works with all secrets backends (pass, sops, unsafe). See [Secrets Configuration](./secrets.md) for backend details.

Build and push to Gordon:
```bash
docker build -f postgres.Dockerfile -t my-postgres:latest .
docker push registry.mydomain.com/my-postgres:latest
```

Use in attachments:
```toml
[attachments]
"app.mydomain.com" = ["my-postgres:latest"]
```

## Attachment Secrets

Inject environment variables into attachment containers using the `--attachment` flag on secrets commands:

```bash
# Set secrets for the postgres attachment
gordon secrets set app.mydomain.com --attachment postgres POSTGRES_USER=admin POSTGRES_PASSWORD=secret

# Set secrets for the redis attachment
gordon secrets set app.mydomain.com --attachment redis REDIS_PASSWORD=cache-secret

# View all secrets (domain + attachments) in a tree view
gordon secrets list app.mydomain.com

# Remove an attachment secret
gordon secrets remove app.mydomain.com --attachment postgres POSTGRES_PASSWORD
```

The `--attachment` flag takes the service name (the image name before the colon in your attachments config). For example, if your config has `"app.mydomain.com" = ["postgres:18", "redis:7-alpine"]`, the service names are `postgres` and `redis`.

Storage depends on your secrets backend:
- **pass**: `gordon/env/attachments/<container-name>/<KEY>`
- **sops/unsafe**: `gordon-<container-name>.env` files

See [Secrets Configuration](./secrets.md) and [Secrets Commands](../cli/secrets.md) for details.

## Shared Attachments with Network Groups

Share services between multiple apps using network groups:

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
"backend" = ["shared-postgres:latest", "shared-redis:latest"]
```

Both `app.mydomain.com` and `api.mydomain.com` can access the shared services.

## Per-App vs Shared Attachments

### Per-App Attachments

Each app gets its own isolated service instances:

```toml
[attachments]
"app.mydomain.com" = ["postgres:latest"]   # App's own postgres
"api.mydomain.com" = ["postgres:latest"]   # API's own postgres
```

### Shared Attachments

Multiple apps share the same service instances:

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
"backend" = ["postgres:latest", "redis:latest"]  # Shared by both
```

### Mixed Approach

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
"backend" = ["redis:latest"]                   # Shared cache
"app.mydomain.com" = ["postgres:latest"]       # App's own database
"api.mydomain.com" = ["postgres:latest"]       # API's own database
```

## Examples

### Web App with Database

```toml
[routes]
"app.mydomain.com" = "myapp:latest"

[attachments]
"app.mydomain.com" = ["postgres:18", "redis:7-alpine"]
```

### Microservices with Shared Queue

```toml
[routes]
"orders.mydomain.com" = "orders-service:latest"
"inventory.mydomain.com" = "inventory-service:latest"
"notifications.mydomain.com" = "notifications-service:latest"

[network_groups]
"services" = ["orders.mydomain.com", "inventory.mydomain.com", "notifications.mydomain.com"]

[attachments]
"services" = ["rabbitmq:3-management"]
"orders.mydomain.com" = ["orders-db:latest"]
"inventory.mydomain.com" = ["inventory-db:latest"]
```

### Custom Service Images

Build custom service images with your configuration:

```dockerfile
# my-postgres.Dockerfile
FROM postgres:18
VOLUME ["/var/lib/postgresql/data"]
ENV POSTGRES_DB=production
ENV POSTGRES_USER=appuser
# Password should come from env file
```

```dockerfile
# my-redis.Dockerfile
FROM redis:7-alpine
VOLUME ["/data"]
CMD ["redis-server", "--appendonly", "yes"]
```

```toml
[attachments]
"app.mydomain.com" = ["my-postgres:latest", "my-redis:latest"]
```

## Container Naming

Attachment containers are named:
- `gordon-<domain>-<image-name>`
- Example: `gordon-app-mydomain-com-postgres`

## Labels

Gordon adds labels to attachment containers:

| Label | Value |
|-------|-------|
| `gordon.managed` | `true` |
| `gordon.attachment` | `true` |
| `gordon.attached-to` | Domain or group name |

## CLI Management

Manage attachments via the CLI without editing configuration files.

### List Attachments

```bash
# List all attachments
gordon attachments list

# List attachments for a specific domain or network group
gordon attachments list app.mydomain.com
gordon attachments list backend

# Remote mode
gordon attachments list --remote https://gordon.mydomain.com --token $TOKEN
```

### Add Attachments

```bash
# Add attachment to a domain
gordon attachments add app.mydomain.com postgres:18

# Add attachment to a network group
gordon attachments add backend redis:7-alpine

# Remote mode
gordon attachments add app.mydomain.com postgres:18 --remote https://gordon.mydomain.com --token $TOKEN
```

### Remove Attachments

```bash
# Remove attachment from a domain
gordon attachments remove app.mydomain.com postgres:18

# Remove from network group
gordon attachments remove backend redis:7-alpine

# Remote mode
gordon attachments remove app.mydomain.com postgres:18 --remote https://gordon.mydomain.com --token $TOKEN
```

### Alias

The `gordon attach` command is an alias for `gordon attachments`:

```bash
gordon attach list
gordon attach add app.mydomain.com postgres:18
gordon attach remove app.mydomain.com postgres:18
```

## Related

- [Network Isolation](./network-isolation.md)
- [Network Groups](./network-groups.md)
- [Routes](./routes.md)
- [CLI Commands](/docs/cli/index.md)
