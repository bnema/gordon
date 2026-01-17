# Authentication Commands

Manage Gordon server authentication tokens and passwords.

## gordon auth

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `token generate` | Generate a new JWT token |
| `token list` | List all stored tokens |
| `token revoke` | Revoke a token by ID (use `--all` to revoke all tokens) |
| `password hash` | Generate bcrypt password hash |
| `internal` | Show internal registry credentials |

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

# Token for 6 months
gordon auth token generate --subject deploy --expiry 6M
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

## gordon auth password hash

Generate a bcrypt hash for password authentication.

```bash
gordon auth password hash
```

Interactively prompts for a password and outputs the bcrypt hash.

### Output

```
Enter password: ********

Bcrypt hash (store in your secrets backend):
$2a$10$N9qo8uLOickgx2ZMRZoMye...

Then reference the path in your config:
  [auth]
  type = "password"
  password_hash = "gordon/auth/password_hash"
```

---

## gordon auth internal

Display the auto-generated internal registry credentials.

```bash
gordon auth internal
```

Gordon generates temporary credentials for internal communication with its local registry. These credentials are useful for manual recovery when debugging deployment issues.

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
# GitHub: Settings → Secrets → New repository secret
# GitLab: Settings → CI/CD → Variables
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

### Password Setup

```bash
# Generate hash
gordon auth password hash
# Enter: mypassword

# Store in pass
pass insert gordon/auth/password_hash
# Paste: $2a$10$...

# Configure gordon.toml
[auth]
enabled = true
type = "password"
secrets_backend = "pass"
username = "deploy"
password_hash = "gordon/auth/password_hash"
```

## Related

- [CLI Overview](./index.md)
- [Authentication Configuration](../config/auth.md)
