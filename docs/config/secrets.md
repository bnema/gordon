# Secrets Configuration

Configure how Gordon stores and retrieves sensitive data.

## Configuration

The secrets backend is configured within the `[auth]` section:

```toml
[auth]
secrets_backend = "pass"  # "pass", "sops", or "unsafe"
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `secrets_backend` | string | `"unsafe"` | Secrets storage backend |

## Backends

### Pass (Recommended for Production)

Uses the Unix password manager (`pass`) for secure secret storage:

```toml
[auth]
secrets_backend = "pass"
```

**Setup:**
```bash
# Install pass
sudo apt install pass

# Initialize with GPG key
pass init your-gpg-key-id

# Store secrets
pass insert gordon/auth/password_hash
pass insert gordon/auth/token_secret
```

**Usage in config:**
```toml
[auth]
password_hash = "gordon/auth/password_hash"  # Path in pass store
token_secret = "gordon/auth/token_secret"
```

**Benefits:**
- GPG-encrypted storage
- Version control friendly (encrypted files)
- Standard Unix tooling
- Works with team GPG keys

**Route secrets storage:**
- `gordon secrets set` stores per-domain secrets in pass under `gordon/env/<sanitized-domain>/<KEY>` (dots/colons/slashes → underscores)
- Existing `.env` files are auto-migrated on startup and renamed to `.env.migrated`
- Attachment secrets are stored under `gordon/env/attachments/<container-name>/<KEY>` with a `.keys` manifest
- Use `gordon secrets set <domain> --attachment <service> KEY=value` to manage them

### SOPS

Uses Mozilla SOPS for encrypted file-based secrets:

```toml
[auth]
secrets_backend = "sops"
```

**Setup:**
```bash
# Install sops
brew install sops  # macOS
# or download from https://github.com/getsops/sops/releases

# Create encrypted secrets file
sops secrets.yaml
```

**Usage in env files:**
```bash
# ~/.gordon/env/app_mydomain_com.env
API_SECRET=${sops:secrets.yaml:api.secret}
DB_PASSWORD=${sops:secrets.yaml:database.password}
```

**Benefits:**
- Multiple encryption backends (AWS KMS, GCP KMS, Azure Key Vault, PGP)
- YAML/JSON file encryption
- Git-friendly (encrypted files in repo)

**Security:**
- Absolute paths are rejected to prevent arbitrary file access
- Path traversal (`..`) is blocked
- Only relative paths from your config directory are allowed

**Route secrets storage:**
- Domain secrets stay in `.env` files
- Use `${sops:...}` syntax to resolve encrypted values

**Attachment secrets storage:**
- Attachment secrets are stored in `gordon-<container-name>.env` files alongside domain env files
- Use `${sops:...}` syntax inside attachment env files to resolve encrypted values
- Use `gordon secrets set <domain> --attachment <service> KEY=value` to manage them

### Unsafe (Development Only)

Stores secrets as plain text files:

```toml
[auth]
secrets_backend = "unsafe"
```

**Storage location:**
```
{data_dir}/secrets/
├── gordon/
│   └── auth/
│       ├── password_hash
│       └── token_secret
```

**Attachment secrets:**
- Stored as `gordon-<container-name>.env` files in the env directory
- Example: `gordon-app__mydomain__com-postgres.env`
- Use `gordon secrets set <domain> --attachment <service> KEY=value` to manage them

**Usage:**
```bash
# Create secret
mkdir -p ~/.gordon/secrets/gordon/auth
echo "your-bcrypt-hash" > ~/.gordon/secrets/gordon/auth/password_hash
```

> **Warning:** Only use for local development. Secrets are stored in plain text.

## Secret Provider Syntax

In environment files, reference secrets using provider syntax:

### Pass Provider

```bash
# ${pass:<path>}
DATABASE_PASSWORD=${pass:myapp/database/password}
API_KEY=${pass:myapp/api-key}
```

### SOPS Provider

```bash
# ${sops:<file>:<key.path>}
DATABASE_PASSWORD=${sops:secrets.yaml:database.password}
API_SECRET=${sops:production.yaml:api.secret.key}
```

## Examples

### Production with Pass

```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
```

```bash
# Setup
pass insert gordon/auth/token_secret
# Enter a random 32+ character string

# Generate tokens
gordon auth token generate --subject deploy --expiry 0
```

### Development with Unsafe

```toml
[auth]
enabled = false
secrets_backend = "unsafe"
```

### Enterprise with SOPS

```toml
[auth]
enabled = true
secrets_backend = "sops"
token_secret = "gordon/auth/token_secret"
```

Environment file:
```bash
# ~/.gordon/env/app_company_com.env
NODE_ENV=production
DATABASE_URL=postgresql://db:5432/app
DATABASE_PASSWORD=${sops:secrets.yaml:database.password}
API_KEY=${sops:secrets.yaml:api.key}
JWT_SECRET=${sops:secrets.yaml:jwt.secret}
```

## Security Recommendations

1. **Production**: Always use `pass` or `sops` backend
2. **Never commit**: Don't commit unencrypted secrets to git
3. **Rotate regularly**: Regenerate tokens and passwords periodically
4. **Least privilege**: Use separate secrets per environment
5. **Path validation**: SOPS provider rejects absolute paths and path traversal attempts for security

## Related

- [Authentication](./auth.md)
- [Environment Variables](./env.md)
- [Configuration Overview](./index.md)
