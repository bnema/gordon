# Environment Variables

Configure per-application environment variables using domain-based files.

## Configuration

```toml
[env]
dir = "~/.gordon/env"  # Default location
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `dir` | string | `~/.gordon/env` | Directory containing .env files |

## How It Works

Gordon loads environment variables from files named after the domain:

```
~/.gordon/env/
├── app_mydomain_com.env      # For app.mydomain.com
├── api_mydomain_com.env      # For api.mydomain.com
└── admin_mydomain_com.env    # For admin.mydomain.com
```

**Naming convention:** Replace dots with underscores, add `.env` extension.

| Domain | File Name |
|--------|-----------|
| `app.mydomain.com` | `app_mydomain_com.env` |
| `api.company.io` | `api_company_io.env` |
| `staging.app.dev` | `staging_app_dev.env` |

## Backend Behavior

The `auth.secrets_backend` setting changes where route secrets live:

### Pass Backend

- Env files are not used for route secrets.
- Use `gordon secrets set` to store per-domain secrets in pass.
- Existing `.env` files are migrated on startup and renamed to `.env.migrated`.

### SOPS or Unsafe Backend

- Env files remain the source of truth.
- Use `${sops:...}` syntax for encrypted values when `secrets_backend = "sops"`.

## File Format

Standard `.env` file format:

```bash
# app_mydomain_com.env
NODE_ENV=production
PORT=3000
DATABASE_URL=postgresql://db:5432/myapp
API_KEY=sk-1234567890
DEBUG=false
```

## Variable Merging

Variables are merged from multiple sources (lowest to highest priority):

1. **Dockerfile ENV** - Base defaults from image
2. **.env file** - Overrides Dockerfile values

```dockerfile
# Dockerfile
ENV NODE_ENV=development
ENV PORT=3000
```

```bash
# app_mydomain_com.env
NODE_ENV=production  # Overrides Dockerfile
# PORT not set, uses 3000 from Dockerfile
```

Result: `NODE_ENV=production`, `PORT=3000`

## Secret Provider Syntax

Reference secrets from configured backends in env files:

### Pass (Unix Password Manager)

```bash
DATABASE_PASSWORD=${pass:myapp/database/password}
API_SECRET=${pass:company/api-secret}
JWT_KEY=${pass:production/jwt-signing-key}
```

### SOPS (Encrypted Files)

```bash
DATABASE_PASSWORD=${sops:secrets.yaml:database.password}
API_SECRET=${sops:production.yaml:api.secret}
STRIPE_KEY=${sops:secrets.yaml:stripe.api_key}
```

### Syntax Reference

| Provider | Syntax | Example |
|----------|--------|---------|
| pass | `${pass:<path>}` | `${pass:myapp/db-password}` |
| sops | `${sops:<file>:<key.path>}` | `${sops:secrets.yaml:db.password}` |

## Examples

### Basic Application

```bash
# app_mydomain_com.env
NODE_ENV=production
PORT=3000
LOG_LEVEL=info
```

### Database Connection

```bash
# ~/.gordon/env/api_mydomain_com.env
DATABASE_HOST=postgres
DATABASE_PORT=5432
DATABASE_NAME=myapi
DATABASE_USER=apiuser
DATABASE_PASSWORD=${pass:myapi/db-password}
DATABASE_URL=postgresql://${DATABASE_USER}:${DATABASE_PASSWORD}@${DATABASE_HOST}:${DATABASE_PORT}/${DATABASE_NAME}
```

### Full Production Setup

```bash
# ~/.gordon/env/app_company_com.env
# Application settings
NODE_ENV=production
PORT=3000
LOG_LEVEL=warn

# Database
DATABASE_URL=postgresql://postgres:5432/production
DATABASE_PASSWORD=${pass:company/db-password}

# Redis
REDIS_URL=redis://redis:6379

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

### Development Environment

```bash
# ~/.gordon/env/app_local.env
NODE_ENV=development
PORT=3000
LOG_LEVEL=debug
DATABASE_URL=postgresql://postgres:5432/dev
DEBUG=*
```

## File Permissions

Gordon creates env files with secure permissions:
- Directory: `0700` (owner only)
- Files: `0600` (owner only)

Create files with proper permissions:

```bash
mkdir -p ~/.gordon/env
chmod 700 ~/.gordon/env
touch ~/.gordon/env/app_mydomain_com.env
chmod 600 ~/.gordon/env/app_mydomain_com.env
```

## Auto-Creation

Gordon creates empty env files automatically when deploying a new route. You can then edit the file to add your variables.

## Viewing Effective Environment

To see what environment a container receives:

```bash
docker inspect gordon-app-mydomain-com | jq '.[0].Config.Env'
```

## Related

- [Secrets Configuration](./secrets.md)
- [Routes](./routes.md)
- [Attachments](./attachments.md)
