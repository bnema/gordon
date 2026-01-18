# Authentication

Configure authentication for the Gordon registry and Admin API.

## Quick Start

Authentication is **enabled by default** for security. Gordon will not start without authentication configured.

**Option 1: Password Authentication (simplest)**
```bash
# 1. Generate password hash
gordon auth password hash
# Enter your password, copy the bcrypt hash

# 2. Save hash to secrets file
mkdir -p ~/.gordon/secrets
echo '$2a$10$...' > ~/.gordon/secrets/password_hash

# 3. Add to config.toml
cat >> ~/.gordon/config.toml << 'EOF'
[auth]
password_hash = "password_hash"
EOF
```

**Option 2: Disable Authentication (development only)**
```toml
[auth]
enabled = false
```

## Configuration

```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
token_expiry = "30d"

# Optional: enable password authentication
username = "deploy"
password_hash = "gordon/auth/password_hash"
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Enable authentication |
| `secrets_backend` | string | `"unsafe"` | Secrets backend: `"pass"`, `"sops"`, or `"unsafe"` |
| `token_secret` | string | - | **Required.** Path to JWT signing secret in secrets backend |
| `token_expiry` | string | `"30d"` | Token validity duration (0 = never expires) |
| `username` | string | - | Username for password auth (optional) |
| `password_hash` | string | - | Path to bcrypt hash in secrets backend (optional) |

## Authentication Modes

Gordon infers the authentication mode from your configuration:

| Config | Mode | Description |
|--------|------|-------------|
| Only `token_secret` | Token-only | JWT tokens for all access |
| `token_secret` + `username` + `password_hash` | Password + Token | Password for interactive login, tokens for CI/CD |

Tokens always work regardless of mode. The difference is whether password authentication is available.

## Environment Variables

Gordon supports environment variables for sensitive configuration:

| Variable | Description |
|----------|-------------|
| `GORDON_AUTH_TOKEN_SECRET` | JWT signing secret (takes priority over config file) |

Using environment variables is recommended for production as it avoids storing secrets on disk:

```bash
export GORDON_AUTH_TOKEN_SECRET="your-32-character-secret-here"
gordon serve
```

## Secrets Backend

The `secrets_backend` option determines how Gordon retrieves sensitive values like `token_secret` and `password_hash`:

| Backend | Description |
|---------|-------------|
| `pass` | Unix password manager (GPG-encrypted) |
| `sops` | Mozilla SOPS encrypted files |
| `unsafe` | Plain text files (development only) |

See [Secret Providers](./secrets.md) for detailed configuration of each backend.

## Authentication Types

### Token-Only Mode

JWT-based authentication, ideal for CI/CD pipelines when you don't need interactive login:

```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
token_expiry = "30d"
```

**Setup:**
```bash
# Create token secret (random 32+ characters)
pass insert gordon/auth/token_secret

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

# Revoke a specific token
gordon auth token revoke <token-id>

# Revoke all tokens
gordon auth token revoke --all
```

### Password + Token Mode

Password authentication for interactive login, plus tokens for CI/CD:

```toml
[auth]
enabled = true
secrets_backend = "pass"
username = "deploy"
password_hash = "gordon/auth/password_hash"
token_secret = "gordon/auth/token_secret"
```

When `username` and `password_hash` are configured, both password and token authentication work.

**Setup:**
```bash
# Generate bcrypt hash
gordon auth password hash
# Enter password when prompted

# Store hash in secrets backend
pass insert gordon/auth/password_hash
# Paste the bcrypt hash

# Create token secret (for CI/CD token generation)
pass insert gordon/auth/token_secret
# Enter a random 32+ character string
```

**Usage:**
```bash
# Docker registry access
docker login -u deploy -p <password> registry.mydomain.com

# Remote CLI authentication
gordon auth login --remote prod
# Enter username and password when prompted
```

**Remote CLI Authentication:**

With password authentication enabled, you can use `gordon auth login` to obtain a token for remote CLI operations:

```bash
gordon auth login --remote prod
# Username: deploy
# Password: ********
# ✓ Authentication successful!
# Token stored for remote 'prod'
```

The returned token is stored automatically and used for subsequent CLI commands.

## Token Scopes

Tokens can have different permission levels:

### Registry Scopes

| Scope | Permission |
|-------|------------|
| `push` | Push images to registry |
| `pull` | Pull images from registry |
| `push,pull` | Both push and pull (default) |

### Admin Scopes

For remote CLI access via the Admin API:

| Scope | Permission |
|-------|------------|
| `admin:*:*` | Full admin access |
| `admin:routes:read` | Read-only routes access |
| `admin:routes:write` | Routes write access |
| `admin:config:read` | Read-only config access |
| `admin:config:write` | Config write access |
| `admin:status:read` | Read-only status/health |
| `admin:secrets:read` | List secret keys |
| `admin:secrets:write` | Set/delete secrets |

**Examples:**
```bash
# Pull-only token (for read-only access)
gordon auth token generate --subject reader --scopes pull --expiry 30d

# Push-only token (for CI builds)
gordon auth token generate --subject builder --scopes push --expiry 0

# Full admin access (for remote CLI)
gordon auth token generate --subject admin --scopes "push,pull,admin:*:*" --expiry 0
```

## Token Expiry

Control how long tokens remain valid. Supports human-friendly units:

| Unit | Description |
|------|-------------|
| `d` | days (24 hours) |
| `w` | weeks (7 days) |
| `M` | months (30 days) |
| `y` | years (365 days) |

Compound durations work too: `1y6M`, `2w3d`

```bash
# Never expires (for CI/CD)
gordon auth token generate --subject ci --expiry 0

# Expires in 1 year
gordon auth token generate --subject deploy --expiry 1y

# Expires in 30 days
gordon auth token generate --subject temp --expiry 30d
```

## Instance-Specific Tokens

Tokens are signed with the `token_secret`, which is unique to each Gordon instance:

- Tokens from one Gordon instance won't work on another
- Changing the `token_secret` invalidates all existing tokens
- Each environment (dev, staging, prod) should have different secrets

## Development Setup

For local development without authentication:

```toml
[auth]
enabled = false
```

## Examples

### CI/CD Setup (Token-Only)

```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
```

```bash
# Generate CI token
gordon auth token generate --subject github-actions --scopes push,pull --expiry 0
```

In GitHub Actions:
```yaml
- name: Login to Gordon Registry
  run: docker login -u github-actions -p ${{ secrets.GORDON_TOKEN }} ${{ secrets.GORDON_REGISTRY }}
```

### Production with Password + Token

```toml
[auth]
enabled = true
secrets_backend = "pass"
username = "deploy"
password_hash = "gordon/auth/password_hash"
token_secret = "gordon/auth/token_secret"
```

### Enterprise with SOPS

```toml
[auth]
enabled = true
secrets_backend = "sops"
token_secret = "gordon/auth/token_secret"
```

## Internal Registry Auth

Gordon generates internal credentials automatically when auth is enabled. These are used for loopback pulls when deploying containers and are regenerated on each restart.

To view internal credentials (for troubleshooting):
```bash
gordon auth internal
```

## Related

- [Secret Providers](./secrets.md)
- [CLI Commands](../cli/commands.md)
- [GitHub Actions Deployment](../deployment/github-actions.md)
