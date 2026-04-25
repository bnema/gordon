# Network Isolation

Isolate applications in separate Docker networks for enhanced security.

## Configuration

```toml
[network_isolation]
enabled = true
network_prefix = "gordon"
internal = false
```

## Migration Note

As of this release, `network_isolation.enabled` defaults to `true` (previously `false`).
Existing installs that rely on a shared network must explicitly opt out:

```toml
[network_isolation]
enabled = false
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Enable per-app network isolation (changed from `false`) |
| `network_prefix` | string | `"gordon"` | Prefix for created networks |
| `internal` | bool | `false` | Create isolated Docker networks with Docker's `Internal` flag, blocking direct external egress from containers on those networks. Default remains `false` for compatibility. |

## How It Works

When network isolation is enabled, each application gets its own Docker network:

```
[network_isolation]
enabled = true
network_prefix = "gordon"

[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "myapi:latest"
```

Creates two isolated networks:
- `gordon-app-mydomain-com`
- `gordon-api-mydomain-com`

## Network Naming

Networks are named: `{prefix}-{domain-with-dashes}`

| Domain | Network Name |
|--------|--------------|
| `app.mydomain.com` | `gordon-app-mydomain-com` |
| `api.company.io` | `gordon-api-company-io` |
| `staging.app.dev` | `gordon-staging-app-dev` |

## Security Benefits

### Without Network Isolation

All containers can potentially communicate:

```
┌───────────────────────────────────────┐
│ Default Bridge Network                │
│                                       │
│  App A ←──────→ App B ←──────→ App C  │
│    ↕              ↕              ↕    │
│  DB A ←──────→ DB B  ←──────→  DB C   │
│                                       │
└───────────────────────────────────────┘
```

### With Network Isolation

Each app is isolated with its dependencies:

```
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│ gordon-app-a    │  │ gordon-app-b    │  │ gordon-app-c    │
│                 │  │                 │  │                 │
│  App A ←→ DB A  │  │  App B ←→ DB B  │  │  App C ←→ DB C  │
│                 │  │                 │  │                 │
└─────────────────┘  └─────────────────┘  └─────────────────┘
        ↑                   ↑                   ↑
        └───────── No direct communication ─────┘
```

## Service Discovery

Within an isolated network, services are discoverable by name:

```toml
[network_isolation]
enabled = true

[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
```

Your application connects using simple hostnames:

```python
# These work within the isolated network
db = connect("postgresql://postgres:5432/mydb")
cache = connect("redis://redis:6379")
```

## Examples

### Basic Isolation

```toml
[network_isolation]
enabled = true

[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "myapi:latest"

[attachments]
"app.mydomain.com" = ["app-postgres:latest"]
"api.mydomain.com" = ["api-postgres:latest"]
```

Each app gets its own network with its own database.

### Production Configuration

```toml
[network_isolation]
enabled = true
network_prefix = "prod"

[routes]
"app.company.com" = "company-app:v2.1.0"
"api.company.com" = "company-api:v1.5.0"
"admin.company.com" = "admin-panel:v1.0.0"
```

Creates networks:
- `prod-app-company-com`
- `prod-api-company-com`
- `prod-admin-company-com`

### Shared Services with Network Groups

When apps need to communicate, use network groups:

```toml
[network_isolation]
enabled = true

[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
"backend" = ["shared-postgres:latest", "shared-redis:latest"]
```

Both apps share the `gordon-backend` network.

## Inspecting Networks

View created networks:

```bash
docker network ls | grep gordon
# gordon-app-mydomain-com
# gordon-api-mydomain-com
# gordon-backend
```

Inspect a network:

```bash
docker network inspect gordon-app-mydomain-com
```

## Related

- [Network Groups](./network-groups.md)
- [Attachments](./attachments.md)
- [Configuration Overview](./index.md)
