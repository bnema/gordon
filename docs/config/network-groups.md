# Network Groups

Group multiple applications into shared networks for inter-service communication.

## Configuration

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]
"monitoring" = ["grafana.mydomain.com", "prometheus.mydomain.com"]
```

## Syntax

```toml
[network_groups]
"<group-name>" = ["<domain>", "<domain>", ...]
```

| Component | Description |
|-----------|-------------|
| `group-name` | Name for the shared network |
| `domains` | List of routes that share this network |

## How It Works

Network groups create shared Docker networks for multiple applications:

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]
```

Creates a shared network: `gordon-backend`

```
┌────────────────────────────────────────────────┐
│ gordon-backend                                 │
│                                                │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐  │
│  │   App    │←──→│   API    │←──→│ Postgres │  │
│  │  :3000   │    │  :8080   │    │  :5432   │  │
│  └──────────┘    └──────────┘    └──────────┘  │
│                                                │
└────────────────────────────────────────────────┘
```

## Service Discovery

Applications in the same group can communicate using container names:

```toml
[routes]
"app.mydomain.com" = "frontend:latest"
"api.mydomain.com" = "backend:latest"

[network_groups]
"services" = ["app.mydomain.com", "api.mydomain.com"]
```

Server-side code (SSR, API routes) can reach other containers by name:
```javascript
// In server-side code (SSR / API route) - NOT browser JavaScript
const response = await fetch("http://backend:8080/api/data");
```

Browser clients should use the public route instead:
```javascript
// In browser JavaScript
const response = await fetch("https://api.mydomain.com/api/data");
```

## Shared Attachments

Combine network groups with attachments for shared services:

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
"backend" = ["postgres:latest", "redis:latest"]
```

Both `app.mydomain.com` and `api.mydomain.com` can access:
- `postgres:5432`
- `redis:6379`

## Multiple Groups

An application can only be in one network group:

```toml
# Each app belongs to one group
[network_groups]
"customer-facing" = ["app.mydomain.com", "api.mydomain.com"]
"internal" = ["admin.mydomain.com", "metrics.mydomain.com"]
```

## Mixed Isolation

Combine network groups with per-app attachments:

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
# Shared by both apps
"backend" = ["redis:latest", "rabbitmq:latest"]

# App-specific databases
"app.mydomain.com" = ["app-postgres:latest"]
"api.mydomain.com" = ["api-postgres:latest"]
```

Result:
- Shared: Redis and RabbitMQ
- Isolated: Each app has its own Postgres

## Examples

### Microservices Architecture

```toml
[routes]
"orders.mydomain.com" = "orders-service:latest"
"inventory.mydomain.com" = "inventory-service:latest"
"shipping.mydomain.com" = "shipping-service:latest"
"payments.mydomain.com" = "payments-service:latest"

[network_groups]
"order-processing" = ["orders.mydomain.com", "inventory.mydomain.com", "shipping.mydomain.com"]
"payment-processing" = ["payments.mydomain.com"]

[attachments]
"order-processing" = ["rabbitmq:latest"]
"payments.mydomain.com" = ["payments-db:latest"]
```

### Monitoring Stack

```toml
[routes]
"grafana.mydomain.com" = "grafana/grafana:latest"
"prometheus.mydomain.com" = "prom/prometheus:latest"
"alertmanager.mydomain.com" = "prom/alertmanager:latest"

[network_groups]
"monitoring" = ["grafana.mydomain.com", "prometheus.mydomain.com", "alertmanager.mydomain.com"]

[attachments]
"monitoring" = ["prometheus-data:latest"]
```

### Multi-Tenant SaaS

```toml
[routes]
"app.saas.com" = "saas-app:latest"
"api.saas.com" = "saas-api:latest"
"admin.saas.com" = "saas-admin:latest"

# API and App share backend services
[network_groups]
"platform" = ["app.saas.com", "api.saas.com"]

# Admin is isolated
[attachments]
"platform" = ["postgres:latest", "redis:latest", "elasticsearch:latest"]
"admin.saas.com" = ["admin-db:latest"]
```

## Network Naming

Group networks are named: `{network_prefix}-{group-name}`

| Group | Network Name |
|-------|--------------|
| `backend` | `gordon-backend` |
| `monitoring` | `gordon-monitoring` |
| `order-processing` | `gordon-order-processing` |

## Related

- [Network Isolation](./network-isolation.md)
- [Attachments](./attachments.md)
- [Routes](./routes.md)
