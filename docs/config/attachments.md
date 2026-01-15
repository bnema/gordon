# Attachments Configuration

Attach service dependencies (databases, caches, queues) to your applications.

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
┌─────────────────────────────────────────────────────┐
│ Network: gordon-app-mydomain-com                    │
│                                                     │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐     │
│  │   App    │───>│ postgres │    │  redis   │     │
│  │  :3000   │    │  :5432   │    │  :6379   │     │
│  └──────────┘    └──────────┘    └──────────┘     │
│                                                     │
└─────────────────────────────────────────────────────┘
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
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]
ENV POSTGRES_DB=myapp
ENV POSTGRES_USER=app
ENV POSTGRES_PASSWORD=secret
```

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
"app.mydomain.com" = ["postgres:15", "redis:7-alpine"]
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
FROM postgres:15
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

## Related

- [Network Isolation](./network-isolation.md)
- [Network Groups](./network-groups.md)
- [Routes](./routes.md)
