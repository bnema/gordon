# Secrets Configuration

Configure how Gordon stores and retrieves sensitive data.

## Configuration

```toml
[secrets]
backend = "pass"  # "pass", "sops", or "unsafe"
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `backend` | string | `"unsafe"` | Secrets storage backend |

## Backends

### Pass (Recommended for Production)

Uses the Unix password manager (`pass`) for secure secret storage:

```toml
[secrets]
backend = "pass"
```

**Setup:**
```bash
# Install pass
sudo apt install pass

# Initialize with GPG key
pass init your-gpg-key-id

# Store secrets
pass insert gordon/registry/password_hash
pass insert gordon/registry/token_secret
```

**Usage in config:**
```toml
[registry_auth]
password_hash = "gordon/registry/password_hash"  # Path in pass store
token_secret = "gordon/registry/token_secret"
```

**Benefits:**
- GPG-encrypted storage
- Version control friendly (encrypted files)
- Standard Unix tooling
- Works with team GPG keys

### SOPS

Uses Mozilla SOPS for encrypted file-based secrets:

```toml
[secrets]
backend = "sops"
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

### Unsafe (Development Only)

Stores secrets as plain text files:

```toml
[secrets]
backend = "unsafe"
```

**Storage location:**
```
{data_dir}/secrets/
├── gordon/
│   └── registry/
│       ├── password_hash
│       └── token_secret
```

**Usage:**
```bash
# Create secret
mkdir -p ~/.gordon/secrets/gordon/registry
echo "your-bcrypt-hash" > ~/.gordon/secrets/gordon/registry/password_hash
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
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "token"
token_secret = "gordon/registry/token_secret"
```

```bash
# Setup
pass insert gordon/registry/token_secret
# Enter a random 32+ character string

# Generate tokens
gordon auth token generate --subject deploy --expiry 0
```

### Development with Unsafe

```toml
[secrets]
backend = "unsafe"

[registry_auth]
enabled = false
```

### Enterprise with SOPS

```toml
[secrets]
backend = "sops"

[registry_auth]
enabled = true
type = "token"
token_secret = "gordon/registry/token_secret"
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

## Related

- [Registry Authentication](./registry-auth.md)
- [Environment Variables](./env.md)
- [Configuration Overview](./index.md)
