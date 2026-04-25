# Authentication Commands

Manage Gordon server authentication tokens.

## gordon auth

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `login` | Store a token for a remote Gordon server |
| `show-token` | Print the stored token for a remote |
| `logout` | Remove stored token for a remote |
| `status` | Check authentication session status |
| `token generate` | Generate a new JWT token |
| `token list` | List all stored tokens |
| `token revoke` | Revoke a token by ID (use `--all` to revoke all tokens) |
| `internal` | Show internal registry credentials |

---

## gordon auth login

Store a pre-generated token for a remote Gordon server.

```bash
gordon auth login --token <token> [options]
```

### Options

| Option | Description |
|--------|-------------|
| `-t, --token` | **Required.** Authentication token to store |

Use the global `--remote, -r` flag to target a specific remote. See [CLI Overview](./index.md).

### Description

Stores a token for use with a remote Gordon server. The `--token` flag is required — generate a token on the server with `gordon auth token generate`.

When [pass](https://www.passwordstore.org/) is available, the token is stored encrypted at `gordon/remotes/<name>/token` and plaintext fields are cleared from `remotes.toml`. When `pass` is unavailable, the token is stored in plaintext with a warning.

The token is verified against `/admin/status` on a best-effort basis.

### Examples

```bash
gordon auth login --token <token>
gordon auth login --remote prod --token <token>
```

### Output

When `pass` is available:

```text
Token stored in pass for remote 'prod'
```

When `pass` is not available:

```text
Warning: 'pass' not available. Storing token in plaintext config. Consider installing pass (https://www.passwordstore.org/) for secure token storage.
Token stored for remote 'prod'
```

---

## gordon auth show-token

Print the stored token for a remote.

```bash
gordon auth show-token [--remote <name>]
```

Output is the raw token string, suitable for piping. Returns an error if no token is stored.

```bash
gordon auth show-token
gordon auth show-token --remote prod
gordon auth show-token | pbcopy  # copy to clipboard
```

---

## gordon auth logout

Remove the stored token for a remote.

```bash
gordon auth logout [--remote <name>] [--revoke]
```

| Option | Description |
|--------|-------------|
| `--revoke` | Also revoke the token server-side (not yet implemented) |

Use the global `--remote, -r` flag to target a specific remote. See [CLI Overview](./index.md).

Deletes the token from pass and clears token fields in `remotes.toml`.

---

## gordon auth token generate

Generate a new JWT authentication token for registry access.

```bash
gordon auth token generate --subject <name> [options]
```

### Options

| Option | Default | Description |
|--------|---------|-------------|
| `--subject` | (required) | Username/subject for the token |
| `--scopes` | `push,pull` | Comma-separated scopes |
| `--expiry` | `30d` | Duration until expiry (0 = never) |
| `-c, --config` | Auto | Path to config file |

### Duration Format

Supports human-friendly duration units:

| Unit | Description | Example |
|------|-------------|---------|
| `d` | Days (24 hours) | `30d` |
| `w` | Weeks (7 days) | `2w` |
| `M` | Months (30 days) | `6M` |
| `y` | Years (365 days) | `1y` |

Compound durations are also supported: `1y6M`, `2w3d`, `1d12h`

Standard Go durations also work: `24h`, `30m`, `1h30m`

### Scopes

Registry scopes: `push`, `pull`, `push,pull`

Admin scopes: `admin:*:*`, `admin:routes:read`, `admin:routes:write`, `admin:config:read`, `admin:config:write`, `admin:status:read`, `admin:logs:read`, `admin:secrets:read`, `admin:secrets:write`

Combine scopes with commas:

```bash
gordon auth token generate --subject admin --scopes "push,pull,admin:*:*" --expiry 0
```

### Examples

```bash
# CI token that never expires
gordon auth token generate --subject github-actions --expiry 0

# Read-only token for 30 days
gordon auth token generate --subject reader --scopes pull --expiry 30d

# Push-only token for 1 year
gordon auth token generate --subject builder --scopes push --expiry 1y

# Temporary token (24 hours)
gordon auth token generate --subject temp --expiry 24h

# Full admin access token
gordon auth token generate --subject admin --scopes "push,pull,admin:*:*" --expiry 6M

# Read-only admin token
gordon auth token generate --subject monitor --scopes "admin:status:read" --expiry 30d
```

### Output

```
Token generated successfully!
Subject: github-actions
Scopes: push, pull
Expiry: never

Token (use as password with docker login):
eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...

Usage: docker login -u github-actions -p <token> <registry>
```

---

## gordon auth token list

List all stored authentication tokens.

```bash
gordon auth token list [options]
```

### Output

```
ID                                    Subject               Expires               Revoked
------------------------------------------------------------------------------------------
a1b2c3d4-e5f6-7890-abcd-ef1234567890  github-actions        never                 no
b2c3d4e5-f6a7-8901-bcde-f12345678901  deploy-bot            2024-02-15 10:30      no
c3d4e5f6-a7b8-9012-cdef-123456789012  old-token             2024-01-01 00:00      yes
```

---

## gordon auth token revoke

Revoke a token by its ID.

```bash
gordon auth token revoke <token-id> [options]
gordon auth token revoke --all  # Revoke all tokens
```

### Example

```bash
gordon auth token revoke a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

### Output

```
Token a1b2c3d4-e5f6-7890-abcd-ef1234567890 has been revoked.
```

---

## gordon auth internal

Display the auto-generated internal registry credentials.

```bash
gordon auth internal
```

Gordon generates temporary credentials for loopback-only communication with its local registry. These credentials are useful for manual recovery when debugging deployment issues.

When registry auth is enabled, Gordon also generates a separate service token for its own registry-domain pulls. That token is not exposed via the CLI.

### Output

```
Internal Registry Credentials
==============================
Username: gordon-internal
Password: 8f3a2b1c4d5e6f7a8b9c0d1e2f3a4b5c...

Usage:
  docker login localhost:5000 -u gordon-internal -p 8f3a2b1c4d5e6f7a8b9c0d1e2f3a4b5c...
```

### Notes

- Credentials are regenerated each time Gordon starts
- Only available while Gordon is running
- Used for manual `docker pull` from the local registry during debugging
- Separate from the service token used by Gordon core for registry-domain pulls

### Use Cases

- Debugging image pull failures
- Manual recovery after deployment issues
- Inspecting images in the local registry

---

## Token Scopes

| Scope | Permission |
|-------|------------|
| `push` | Push images to registry |
| `pull` | Pull images from registry |
| `push,pull` | Both push and pull (default) |
| `admin:*:*` | Full admin access |
| `admin:routes:read` | Read-only routes access |
| `admin:routes:write` | Routes write access |
| `admin:config:read` | Read-only config access |
| `admin:config:write` | Config write access |
| `admin:status:read` | Read-only status/health |
| `admin:logs:read` | Read-only logs access |
| `admin:secrets:read` | List secret keys |
| `admin:secrets:write` | Set/delete secrets |

## Token Expiry Formats

| Format | Duration |
|--------|----------|
| `0` | Never expires |
| `24h` | 24 hours |
| `720h` | 30 days |
| `8760h` | 1 year |

---

## Workflow Examples

### CI/CD Setup

```bash
# Generate token for CI
gordon auth token generate --subject ci-bot --scopes push,pull --expiry 0

# Save token as CI secret
# GitHub: Settings > Secrets > New repository secret
# GitLab: Settings > CI/CD > Variables
```

### Docker Login

```bash
# Using generated token
docker login -u ci-bot -p <token> registry.mydomain.com

# In CI/CD
echo "$GORDON_TOKEN" | docker login -u ci-bot --password-stdin registry.mydomain.com
```

### Token Rotation

```bash
# Generate new token
gordon auth token generate --subject ci-bot --expiry 0

# Update CI secrets with new token

# Revoke old token
gordon auth token revoke <old-token-id>
```

### Token-only Setup

```bash
# Create token secret
pass insert gordon/auth/token_secret
# Enter a random 32+ character string

# Configure gordon.toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"

# Generate a deploy token
gordon auth token generate --subject deploy --scopes "push,pull" --expiry 0
```

## Related

- [CLI Overview](./index.md)
- [Authentication Configuration](../config/auth.md)
