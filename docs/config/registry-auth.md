# Registry Authentication

Secure your container registry with password or token-based authentication.

## Configuration

```toml
[registry_auth]
enabled = true
type = "token"  # "password" or "token"

# For token auth:
token_secret = "gordon/registry/token_secret"  # Path in secrets backend
token_expiry = "720h"                          # Duration or 0 for never

# For password auth:
# username = "deploy"
# password_hash = "gordon/registry/password_hash"  # Path in secrets backend
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `false` | Enable registry authentication |
| `type` | string | `"password"` | Auth type: `"password"` or `"token"` |
| `username` | string | - | Username for password auth |
| `password_hash` | string | - | Path to bcrypt hash in secrets backend |
| `token_secret` | string | - | Path to JWT signing secret in secrets backend |
| `token_expiry` | string | `"30d"` | Token validity duration (0 = never expires). Supports: d (days), w (weeks), M (months), y (years) |

## Authentication Types

### Token Authentication (Recommended)

JWT-based authentication, ideal for CI/CD pipelines:

```toml
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "token"
token_secret = "gordon/registry/token_secret"
token_expiry = "30d"  # 30 days, or "0" for never. Also: 1y, 2w, 6M
```

**Setup:**
```bash
# Create token secret (random 32+ characters)
pass insert gordon/registry/token_secret

# Generate a token
gordon auth token generate --subject ci-bot --scopes push,pull --expiry 0
```

**Usage:**
```bash
# Docker login with token
docker login -u ci-bot -p <token> registry.mydomain.com
```

**Token Management:**
```bash
# List all tokens
gordon auth token list

# Revoke a token
gordon auth token revoke <token-id>
```

### Password Authentication

Simple username/password authentication:

```toml
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "password"
username = "deploy"
password_hash = "gordon/registry/password_hash"
```

**Setup:**
```bash
# Generate bcrypt hash
gordon auth password hash
# Enter password when prompted

# Store hash in secrets backend
pass insert gordon/registry/password_hash
# Paste the bcrypt hash
```

**Usage:**
```bash
docker login -u deploy -p <password> registry.mydomain.com
```

## Token Scopes

Tokens can have different permission levels:

| Scope | Permission |
|-------|------------|
| `push` | Push images to registry |
| `pull` | Pull images from registry |
| `push,pull` | Both push and pull (default) |

```bash
# Pull-only token (for read-only access)
gordon auth token generate --subject reader --scopes pull --expiry 30d

# Push-only token (for CI builds)
gordon auth token generate --subject builder --scopes push --expiry 0
```

## Token Expiry

Control how long tokens remain valid. Supports human-friendly units:
- `d` - days (24 hours)
- `w` - weeks (7 days)
- `M` - months (30 days)
- `y` - years (365 days)

Compound durations work too: `1y6M`, `2w3d`

```bash
# Never expires (for CI/CD)
gordon auth token generate --subject ci --expiry 0

# Expires in 1 year
gordon auth token generate --subject deploy --expiry 1y

# Expires in 30 days
gordon auth token generate --subject temp --expiry 30d

# Expires in 2 weeks
gordon auth token generate --subject short --expiry 2w
```

## Instance-Specific Tokens

Tokens are signed with the `token_secret`, which is unique to each Gordon instance. This means:

- Tokens from one Gordon instance won't work on another
- Changing the `token_secret` invalidates all existing tokens
- Each environment (dev, staging, prod) should have different secrets

## Examples

### CI/CD Setup

```toml
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "token"
token_secret = "gordon/registry/token_secret"
```

```bash
# Generate CI token
gordon auth token generate --subject github-actions --scopes push,pull --expiry 0

# Token output:
# eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

In GitHub Actions:
```yaml
- name: Login to Gordon Registry
  run: docker login -u github-actions -p ${{ secrets.GORDON_TOKEN }} ${{ secrets.GORDON_REGISTRY }}
```

### Development Setup

```toml
[registry_auth]
enabled = false  # Disable for local development
```

### Production with Password

```toml
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "password"
username = "deploy"
password_hash = "gordon/registry/password_hash"
```

## Internal Registry Auth

Gordon generates internal credentials automatically when auth is enabled. These are used for loopback pulls when deploying containers and are regenerated on each restart.

## Related

- [Secrets Configuration](./secrets.md)
- [CLI Commands](../cli/commands.md)
- [GitHub Actions Deployment](../deployment/github-actions.md)
