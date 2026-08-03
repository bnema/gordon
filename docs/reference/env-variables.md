# Environment Variables Reference

How Gordon handles workload and component environment variables.

## Process and component environment

`GORDON_ROLE` selects `monolith`, `control`, `runtime`, `edge`, or `registry`; `gordon serve --role` is the equivalent CLI flag. Source config overrides with the `GORDON_` prefix are control-owned. During migration Gordon writes private `0600` role files and minimizes values by role:

- runtime only: `DOCKER_HOST`, `PODMAN_HOST`, `CONTAINER_HOST`, S3 backup credentials;
- control only: pass/SOPS provider settings and general config overrides;
- edge only: Cloudflare DNS credential when configured;
- all roles: optional telemetry headers when telemetry is enabled.

Generated scoped names include `GORDON_COMPONENT_RUNTIME_TOKEN` (control/runtime), `GORDON_COMPONENT_EDGE_TOKEN` (edge), `GORDON_COMPONENT_REGISTRY_TOKEN` (registry), `GORDON_REGISTRY_FORWARD_TOKEN` (edge/registry), and a prepare-only migration probe token. Operators should configure `[runtime] token_env = "GORDON_RUNTIME_HANDOFF_TOKEN"`; generated names are internal contracts, not values to hand-author or share.

`GORDON_MIGRATION_IMAGE` selects the candidate image for migration and is not propagated to roles. Missing required names are reported without values.

## Workload environment

## Variable Sources

Environment variables are loaded from multiple sources and merged:

| Source | Priority | Description |
|--------|----------|-------------|
| Dockerfile `ENV` | Lowest | Default values from image |
| `.env` file | Highest | Per-route overrides |

Higher priority sources override lower priority values.

## Environment File Location

Files are stored in the env directory (default: `~/.gordon/env/`):

```
~/.gordon/env/
├── app_mydomain_com.env
├── api_mydomain_com.env
└── admin_mydomain_com.env
```

**Naming:** Replace dots with underscores, add `.env` suffix.

| Domain | File Name |
|--------|-----------|
| `app.mydomain.com` | `app_mydomain_com.env` |
| `api.company.io` | `api_company_io.env` |
| `staging.app.dev` | `staging_app_dev.env` |

## File Format

Standard `.env` format:

```bash
# Comments start with #
KEY=value
ANOTHER_KEY=another value

# Quotes are optional but recommended for values with spaces
MESSAGE="Hello World"

# No spaces around =
DATABASE_URL=postgresql://localhost:5432/mydb
```

## Secret Provider Syntax

Reference secrets from configured backends:

### Pass Provider

```bash
# Syntax: ${pass:<path>}
DATABASE_PASSWORD=${pass:myapp/database/password}
API_KEY=${pass:company/api-key}
JWT_SECRET=${pass:production/jwt-secret}
```

### SOPS Provider

```bash
# Syntax: ${sops:<file>:<key.path>}
DATABASE_PASSWORD=${sops:secrets.yaml:database.password}
API_SECRET=${sops:production.yaml:api.secret.key}
STRIPE_KEY=${sops:secrets.yaml:stripe.api_key}
```

## Variable Expansion

### From Secrets

```bash
# Password from pass
DB_PASSWORD=${pass:myapp/db-password}

# Then use in connection string
DATABASE_URL=postgresql://user:${DB_PASSWORD}@db:5432/mydb
```

### Static Values

```bash
# Simple values
NODE_ENV=production
PORT=3000

# Values with special characters (use quotes)
MESSAGE="Hello, World!"
JSON_CONFIG='{"key": "value"}'
```

## Dockerfile ENV

Variables from Dockerfile serve as defaults:

```dockerfile
FROM node:18
ENV NODE_ENV=development
ENV PORT=3000
ENV LOG_LEVEL=info
```

Override in `.env` file:

```bash
# Override NODE_ENV, keep PORT and LOG_LEVEL defaults
NODE_ENV=production
```

Result:
- `NODE_ENV=production` (from .env)
- `PORT=3000` (from Dockerfile)
- `LOG_LEVEL=info` (from Dockerfile)

## Common Variables

### Node.js

```bash
NODE_ENV=production
PORT=3000
LOG_LEVEL=warn
```

### Python

```bash
FLASK_ENV=production
DJANGO_SETTINGS_MODULE=myapp.settings.production
PYTHONUNBUFFERED=1
```

### Database Connections

```bash
# PostgreSQL
DATABASE_URL=postgresql://user:pass@postgres:5432/mydb

# MySQL
DATABASE_URL=mysql://user:pass@mysql:3306/mydb

# Redis
REDIS_URL=redis://redis:6379/0
```

### External Services

```bash
# AWS
AWS_ACCESS_KEY_ID=${pass:aws/access-key}
AWS_SECRET_ACCESS_KEY=${pass:aws/secret-key}
AWS_REGION=us-east-1

# Stripe
STRIPE_SECRET_KEY=${pass:stripe/secret-key}
STRIPE_PUBLISHABLE_KEY=pk_live_...

# SendGrid
SENDGRID_API_KEY=${pass:sendgrid/api-key}
```

## Examples

### Minimal

```bash
NODE_ENV=production
PORT=3000
```

### With Database

```bash
NODE_ENV=production
PORT=3000
DATABASE_URL=postgresql://postgres:5432/myapp
DATABASE_PASSWORD=${pass:myapp/db-password}
```

### Full Production

```bash
# Application
NODE_ENV=production
PORT=3000
LOG_LEVEL=warn

# Database
DATABASE_URL=postgresql://postgres:5432/production
DATABASE_PASSWORD=${pass:company/db-password}

# Cache
REDIS_URL=redis://redis:6379/0

# External APIs
STRIPE_SECRET_KEY=${pass:company/stripe-secret}
SENDGRID_API_KEY=${pass:company/sendgrid-key}

# Auth
JWT_SECRET=${pass:company/jwt-secret}
SESSION_SECRET=${pass:company/session-secret}

# Feature flags
ENABLE_ANALYTICS=true
MAINTENANCE_MODE=false
```

## Viewing Container Environment

```bash
docker inspect gordon-app-mydomain-com --format '{{json .Config.Env}}' | jq
```

## Related

- [Environment Configuration](../config/env.md)
- [Secrets Configuration](../config/secrets.md)
