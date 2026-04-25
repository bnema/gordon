# Authentication

Configure authentication for the Gordon registry and Admin API.

Authentication is enabled by default. Gordon will not start without a valid `token_secret` (or `GORDON_AUTH_TOKEN_SECRET`).

## Configuration

Gordon uses token-only authentication. All registry and Admin API access is controlled through JWT tokens.

```toml
[auth]
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
token_expiry = "30d"
access_token_ttl = "15m"
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Authentication toggle. `false` enables local-only mode (see below) |
| `secrets_backend` | string | `"unsafe"` | Secrets backend: `"pass"`, `"sops"`, or `"unsafe"` |
| `token_secret` | string | - | **Required.** Path to JWT signing secret in secrets backend |
| `token_expiry` | string | `"30d"` | Token validity duration (0 = never expires) |
| `access_token_ttl` | string | `"15m"` | Lifetime of ephemeral access tokens issued by `/auth/token` |

## Secrets Backends

`token_secret` is read through the configured backend:

| Backend | Description |
| --------- | ------------- |
| `pass` | Unix password manager (GPG-encrypted) |
| `sops` | Mozilla SOPS encrypted files |
| `unsafe` | Plain text files (development only) |

See [Secret Providers](./secrets.md) for setup details.

## Environment Variable

`GORDON_AUTH_TOKEN_SECRET` overrides `token_secret` in the config. If you don't set the env var, `token_secret` is loaded from the configured secrets backend (including `pass`).

```bash
export GORDON_AUTH_TOKEN_SECRET="your-32-character-secret-here"
gordon serve
```

## Setup

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

## Remote CLI Tokens

Store a token for remote CLI access:

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
| `admin:logs:read` | Read-only logs access |
| `admin:volumes:read` | List Docker volumes |
| `admin:volumes:write` | Prune eligible Gordon-managed volumes |
| `admin:secrets:read` | List secret keys |
| `admin:secrets:write` | Set/delete secrets |

Examples:

```bash
gordon auth token generate --subject reader --scopes pull --expiry 30d
gordon auth token generate --subject builder --scopes push --expiry 0
gordon auth token generate --subject admin --scopes "push,pull,admin:*:*" --expiry 0
```

## Access Token TTL

The `access_token_ttl` setting controls the lifetime of ephemeral access tokens issued by the `/auth/token` endpoint. These short-lived tokens are used internally for registry operations and Admin API sessions.

| Value | Meaning |
|-------|---------|
| `"15m"` | 15 minutes (default) |
| `"30m"` | 30 minutes |
| `"1h"` | 1 hour (maximum before store validation applies) |

Tokens with a lifetime of 1 hour or less skip store validation for performance. Longer-lived tokens (generated via `gordon auth token generate`) are always validated against the token store.

```toml
[auth]
access_token_ttl = "30m"
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

## Local-only Mode

Authentication is enabled by default. If you set `auth.enabled=false`, Gordon switches to local-only mode:

- `/admin/*` endpoints are not registered.
- `/v2/*` registry endpoints are restricted to loopback (`127.0.0.1` / `::1`).
- Remote registry and remote admin access are disabled.

Use this only when Gordon is intended for local machine usage.

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
