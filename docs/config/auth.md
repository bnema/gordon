# Authentication

Configure authentication for the Gordon registry and Admin API.

Authentication is enabled by default. Gordon will not start without a valid `token_secret` (or `GORDON_AUTH_TOKEN_SECRET`).

## Pick a Mode

### Token-only (recommended for CI/CD)

```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
token_expiry = "30d"
```

### Password + Token (interactive + CI/CD)

```toml
[auth]
enabled = true
secrets_backend = "pass"
username = "deploy"
password_hash = "gordon/auth/password_hash"
token_secret = "gordon/auth/token_secret"
```

Token auth always works. Adding `username` + `password_hash` enables interactive login.

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Enable authentication |
| `secrets_backend` | string | `"unsafe"` | Secrets backend: `"pass"`, `"sops"`, or `"unsafe"` |
| `token_secret` | string | - | **Required.** Path to JWT signing secret in secrets backend |
| `token_expiry` | string | `"30d"` | Token validity duration (0 = never expires) |
| `username` | string | - | Username for password auth (optional) |
| `password_hash` | string | - | Path to bcrypt hash in secrets backend (optional) |

## Secrets Backends

`token_secret` and `password_hash` are read through the configured backend:

| Backend | Description |
| --------- | ------------- |
| `pass` | Unix password manager (GPG-encrypted) |
| `sops` | Mozilla SOPS encrypted files |
| `unsafe` | Plain text files (development only) |

See [Secret Providers](./secrets.md) for setup details.

## Environment Variable

`GORDON_AUTH_TOKEN_SECRET` overrides `token_secret` in the config. If you don’t set the env var, `token_secret` is loaded from the configured secrets backend (including `pass`).

```bash
export GORDON_AUTH_TOKEN_SECRET="your-32-character-secret-here"
gordon serve
```

## Setup

### Token-only setup

```bash
# Create token secret (random 32+ characters)
pass insert gordon/auth/token_secret

# Generate a token
gordon auth token generate --subject ci-bot --scopes push,pull --expiry 0
```

```bash
# Docker login with token
docker login -u ci-bot -p <token> registry.mydomain.com
```

### Password + token setup

```bash
# Generate bcrypt hash
gordon auth password hash

# Store hash and token secret
pass insert gordon/auth/password_hash
pass insert gordon/auth/token_secret
```

```bash
# Docker registry access
docker login -u deploy -p <password> registry.mydomain.com

# Remote CLI login (stores a token for you)
gordon auth login --remote prod
```

## Remote CLI Tokens

If you already have a token (token-only servers), store it directly:

```bash
gordon auth login --token <token>
# or
gordon remotes set-token prod <token>
```

## Token Scopes

Registry scopes:

| Scope | Permission |
| ------- | ------------ |
| `push` | Push images to registry |
| `pull` | Pull images from registry |
| `push,pull` | Both push and pull (default) |

Admin scopes (for remote CLI):

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

Examples:

```bash
gordon auth token generate --subject reader --scopes pull --expiry 30d
gordon auth token generate --subject builder --scopes push --expiry 0
gordon auth token generate --subject admin --scopes "push,pull,admin:*:*" --expiry 0
```

## Token Expiry

Durations support `d`, `w`, `M`, `y` and combinations like `1y6M` or `2w3d`.

```bash
gordon auth token generate --subject ci --expiry 0
gordon auth token generate --subject deploy --expiry 1y
gordon auth token generate --subject temp --expiry 30d
```

## Instance-specific Tokens

- Tokens are signed with `token_secret`
- Tokens from one Gordon instance won't work on another
- Changing `token_secret` invalidates all existing tokens

## Disable Auth (development only)

```toml
[auth]
enabled = false
```

> **Security Warning:** When authentication is disabled, the admin API is fully accessible without credentials. This includes endpoints to deploy containers, manage secrets, modify routes, and reload configuration. Only disable auth for local development where Gordon is not exposed to the internet.

## Internal Registry Auth

Gordon generates internal credentials when auth is enabled. These are used for loopback pulls during deploys and are regenerated on each restart.

Gordon also generates a separate service token for its own registry-domain pulls. This token is managed internally and is not exposed via configuration or CLI.

To view internal credentials (for troubleshooting):

```bash
gordon auth internal
```

> **Note:** Internal credentials are stored with restrictive permissions (0600) in secure runtime directories (XDG_RUNTIME_DIR or ~/.gordon/run) and cleaned up on shutdown.

## Related

- [Secret Providers](./secrets.md)
- [CLI Commands](../cli/index.md)
- [GitHub Actions Deployment](../deployment/github-actions.md)
