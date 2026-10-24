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
| `admin:apps:read` | Read-only apps access (list, show, diff, status) |
| `admin:apps:write` | App mutations (apply, deploy, lifecycle, secrets) |
| `admin:config:read` | Read-only config access |
| `admin:config:write` | Config write access |
| `admin:status:read` | Read-only status/health |
| `admin:logs:read` | Read-only logs access |
| `admin:volumes:read` | List Docker volumes |
| `admin:volumes:write` | Prune eligible Gordon-managed volumes |

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

- `/admin/*` endpoints are not registered on the TCP listener.
- `/v2/*` registry endpoints are restricted to loopback (`127.0.0.1` / `::1`).
- Remote registry and remote admin access are disabled.

Local `gordon apps` commands keep working through an owner-only administration socket:

- When `XDG_RUNTIME_DIR` is set, the daemon uses `$XDG_RUNTIME_DIR/gordon/admin.sock`; an invalid or unsafe preferred path fails closed and never falls back. When `XDG_RUNTIME_DIR` is unset, the daemon uses `~/.gordon/run/admin.sock`. The CLI probes `$XDG_RUNTIME_DIR/gordon/admin.sock` when set, then `/run/user/<uid>/gordon/admin.sock`, then `~/.gordon/run/admin.sock`, accepting only a safe owner-owned socket.
- The runtime directory is `0700` and the socket `0600`, both owned by the effective user. A foreign owner, group/other permission bits, or a symlink makes the daemon refuse to start and the CLI refuse to connect; an existing live listener is never replaced.
- The socket exposes only app administration and app log reads. The implicit `local-owner` principal receives exactly `admin:apps:read`, `admin:apps:write`, and `admin:logs:read`; configuration, auth, prune, route, backup, and volume routes are denied.
- Unix platforms only. Never share, mount, or forward the socket: filesystem access to it is the credential.

With neither a running daemon nor an explicit remote, app commands fail with `daemon-unavailable`. An explicit `--remote`/`GORDON_REMOTE` is authoritative and never falls back to the local socket.

Use this only when Gordon is intended for local machine usage.

## Internal Registry Auth

Gordon generates internal credentials when auth is enabled. These are used for loopback pulls during deploys and are regenerated on each restart.

Gordon also generates a separate service token for its own registry-domain pulls. This token is managed internally and is not exposed via configuration or CLI.

To view internal credentials (for troubleshooting):

```bash
gordon auth internal
```

> **Note:** Internal credentials are stored with restrictive permissions (0600) in secure runtime directories (XDG_RUNTIME_DIR or ~/.gordon/run) and cleaned up on shutdown.

## OIDC Trusted Issuers

Declare each workload identity provider at the top level of `gordon.toml`:

```toml
[[trusted_issuers]]
issuer = "https://issuer.example.com"
audience = "https://ci.example.com"
# jwks_uri = "https://issuer.example.com/keys"
max_token_lifetime = "1h"
clock_skew = "60s"
allow_private_network = false
```

| Key | Default | Description |
|-----|---------|-------------|
| `issuer` | Required | Byte-exact JWT `iss`; absolute HTTPS URL without userinfo, query, or fragment |
| `audience` | Required | Non-empty exact value that must occur in JWT `aud` |
| `jwks_uri` | `""` | HTTPS JWKS override; otherwise discovered from the issuer's `/.well-known/openid-configuration` |
| `max_token_lifetime` | `"1h"` | Positive maximum `exp - iat` |
| `clock_skew` | `"60s"` | Non-negative allowance for time claims; `"0s"` disables it |
| `allow_private_network` | `false` | Permit private, loopback, link-local and other non-public destination IPs for this issuer's discovery and JWKS requests |

Issuer URLs and subjects are not normalized; trailing slashes matter.
`jwks_uri` follows the same URL rules as `issuer`. Wildcards and unknown keys
are rejected. Discovery must report the exact configured issuer. Redirects
are refused, HTTPS certificates are verified, and destination IPs are checked
on connection. Enable private network access only for a provider you control.

Tokens must use RS256 or ES256 and contain `sub`, `iat`, and `exp`;
`nbf`, when present, is checked. A `kid` is required with multiple keys.
Keys are cached for 1 hour. Before an issuer's first successful fetch, failed
attempts back off for 2 seconds, doubling up to 60 seconds; requests trigger
retries and concurrent requests share one fetch. A changed issuer policy resets
this state on reload. After success, at most one fetch per issuer per minute
is allowed, including failures and unknown-`kid` probes, so rotated keys can
be refused for up to 60 seconds. On fetch failure, cached keys remain usable
for up to 24 hours from their last successful fetch; after that Gordon fails
closed. No usable keys yields HTTP 503 with `Retry-After: 30`;
an invalid identity token yields 401.

## CI Access Tokens

On the [CI host](./server.md#ci-host), `GET /auth/token` exchanges Basic
credentials (username `oidc`, password the OIDC JWT) for a short-lived
`ci-access` bearer token. The username is ignored. A matching
[set-image policy](./set-image.md) is required. Requested repository scopes
are intersected with policy grants: only `push` and `pull` are issued,
never admin scopes. `pull` permits the HEAD checks needed for pushing;
the CI host does not serve image pulls.

The response contains `token`, `expires_in` (seconds), and `issued_at`
(RFC 3339). A login without a scope gets an unscoped token. Set-image and
lookup use the bearer token, not the raw OIDC JWT.
TTL is `auth.access_token_ttl` (default `15m`), capped at 1 hour.
CI access tokens cannot authenticate to the main registry or admin API.
They are not stored and are revoked only by rotating the token secret;
removing an issuer does not revoke already issued tokens. Current policies
are still checked on requests: removing a policy blocks its repository
access and set-image requests, and hides operation lookups without an app policy.

## Related

- [Secret Providers](./secrets.md)
- [CLI Commands](../cli/index.md)
- [GitHub Actions Deployment](../deployment/github-actions.md)
