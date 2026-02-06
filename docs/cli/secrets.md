# Secrets Commands

Manage secrets on local or remote Gordon instances.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

Storage depends on the secrets backend:
- `pass`: secrets are stored in pass under `gordon/env/<domain>/<KEY>`
- `sops` or `unsafe`: secrets are stored in domain `.env` files

## gordon secrets

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all secrets for a domain |
| `set` | Set a secret value |
| `remove` | Remove a secret |

---

## gordon secrets list

List all secrets for a specific domain. When attachment secrets are present, they are displayed in a tree view below the domain secrets.

```bash
gordon secrets list <domain>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name to list secrets for |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon secrets list myapp.local

# Remote (override)
gordon secrets list myapp.local --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```
Secrets for app.mydomain.com

Key                       Value
DATABASE_URL              ****
API_KEY                   ****
├─ [postgres]
│  ├─ POSTGRES_USER       ****
│  └─ POSTGRES_PASSWORD   ****
└─ [redis]
   └─ REDIS_PASSWORD      ****
```

---

## gordon secrets set

Set a secret value for a domain.

```bash
gordon secrets set <domain> <KEY=value>...
gordon secrets set myapp.local DATABASE_URL "postgres://..."
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name to set the secret for |
| `<key>` | The secret key (environment variable name) |
| `<value>` | The secret value |

### Options

| Option | Description |
|--------|-------------|
| `--attachment` / `-a` | Target an attachment service (e.g., postgres, redis) |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon secrets set myapp.local DATABASE_URL "postgres://user:pass@postgres:5432/db"
gordon secrets set myapp.local API_KEY "your-api-key"

# Remote (override)
gordon secrets set myapp.local DATABASE_URL "postgres://..." --remote https://gordon.mydomain.com --token $TOKEN

# Set attachment secrets
gordon secrets set app.mydomain.com --attachment postgres POSTGRES_PASSWORD=secret
gordon secrets set app.mydomain.com -a redis REDIS_PASSWORD=mysecret

# Multiple secrets at once
gordon secrets set app.mydomain.com -a postgres POSTGRES_USER=admin POSTGRES_PASSWORD=secret
```

---

## gordon secrets remove

Remove a secret from a domain.

```bash
gordon secrets remove <domain> <key>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name |
| `<key>` | The secret key to remove |

### Options

| Option | Description |
|--------|-------------|
| `--attachment` / `-a` | Target an attachment service (e.g., postgres, redis) |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon secrets remove myapp.local DATABASE_URL

# Remote (override)
gordon secrets remove myapp.local DATABASE_URL --remote https://gordon.mydomain.com --token $TOKEN

# Remove attachment secret
gordon secrets remove app.mydomain.com --attachment postgres POSTGRES_PASSWORD
```

---

## Workflow Examples

### Setting Up Application Secrets

```bash
# Database connection
gordon secrets set myapp.local DATABASE_URL "postgres://user:pass@postgres:5432/mydb"

# API keys
gordon secrets set myapp.local STRIPE_KEY "sk_live_..."
gordon secrets set myapp.local SENDGRID_KEY "SG..."

# JWT secret
gordon secrets set myapp.local JWT_SECRET "your-jwt-secret-here"

# Verify
gordon secrets list myapp.local
```

### CI/CD Secret Management

```bash
# In your CI/CD pipeline
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=$GORDON_TOKEN

# Update secrets before deploy
gordon secrets set myapp.example.com DATABASE_URL "$DATABASE_URL"
gordon secrets set myapp.example.com API_KEY "$API_KEY"

# Deploy
gordon deploy myapp.example.com
```

### Rotating Secrets

```bash
# Generate new secret
NEW_JWT_SECRET=$(openssl rand -base64 32)

# Update the secret
gordon secrets set myapp.local JWT_SECRET "$NEW_JWT_SECRET"

# Redeploy to pick up new secret
gordon deploy myapp.local
```

### Attachment Secrets

```bash
# Configure database credentials
gordon secrets set app.mydomain.com -a postgres POSTGRES_USER=admin POSTGRES_PASSWORD=secret

# Configure cache credentials
gordon secrets set app.mydomain.com -a redis REDIS_PASSWORD=cache-secret

# Verify
gordon secrets list app.mydomain.com

# Redeploy to pick up new secrets
gordon deploy app.mydomain.com
```

## Related

- [CLI Overview](./index.md)
- [Secrets Configuration](../config/secrets.md)
- [Environment Variables](../config/env.md)
