# gordon auth

Manage registry authentication tokens and passwords.

## Synopsis

```bash
gordon auth token generate [options]
gordon auth token list [options]
gordon auth token revoke <token-id> [options]
gordon auth password hash
```

## Commands

### gordon auth token generate

Generate a new JWT authentication token for registry access.

```bash
gordon auth token generate --subject <name> [options]
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `--subject` | (required) | Username/subject for the token |
| `--scopes` | `push,pull` | Comma-separated scopes |
| `--expiry` | `720h` | Duration until expiry (0 = never) |
| `-c, --config` | Auto | Path to config file |

**Examples:**

```bash
# CI token that never expires
gordon auth token generate --subject github-actions --expiry 0

# Read-only token
gordon auth token generate --subject reader --scopes pull --expiry 720h

# Push-only token for build system
gordon auth token generate --subject builder --scopes push --expiry 0

# Temporary token (24 hours)
gordon auth token generate --subject temp --expiry 24h
```

**Output:**

```
Token generated successfully!
Subject: github-actions
Scopes: push, pull
Expiry: never

Token (use as password with docker login):
eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...

Usage: docker login -u github-actions -p <token> <registry>
```

### gordon auth token list

List all stored authentication tokens.

```bash
gordon auth token list [options]
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `-c, --config` | Auto | Path to config file |

**Example:**

```bash
gordon auth token list
```

**Output:**

```
ID                                    Subject               Expires               Revoked
------------------------------------------------------------------------------------------
a1b2c3d4-e5f6-7890-abcd-ef1234567890  github-actions        never                 no
b2c3d4e5-f6a7-8901-bcde-f12345678901  deploy-bot            2024-02-15 10:30      no
c3d4e5f6-a7b8-9012-cdef-123456789012  old-token             2024-01-01 00:00      yes
```

### gordon auth token revoke

Revoke a token by its ID.

```bash
gordon auth token revoke <token-id> [options]
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `-c, --config` | Auto | Path to config file |

**Example:**

```bash
gordon auth token revoke a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

**Output:**

```
Token a1b2c3d4-e5f6-7890-abcd-ef1234567890 has been revoked.
```

### gordon auth password hash

Generate a bcrypt hash for password authentication.

```bash
gordon auth password hash
```

Interactively prompts for a password and outputs the bcrypt hash.

**Example:**

```bash
gordon auth password hash
```

**Output:**

```
Enter password: ********

Bcrypt hash (store in your secrets backend):
$2a$10$N9qo8uLOickgx2ZMRZoMye...

Then reference the path in your config:
  [registry_auth]
  type = "password"
  password_hash = "gordon/registry/password_hash"
```

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
pass insert gordon/registry/password_hash
# Paste: $2a$10$...

# Configure gordon.toml
[registry_auth]
enabled = true
type = "password"
username = "deploy"
password_hash = "gordon/registry/password_hash"
```

## Related

- [Registry Authentication](../config/registry-auth.md)
- [Secrets Configuration](../config/secrets.md)
- [CLI Overview](./index.md)
