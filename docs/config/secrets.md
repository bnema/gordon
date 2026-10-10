# Secrets Configuration

Configure how Gordon stores and retrieves sensitive data: auth secrets such as `token_secret` (this page) and app secret values (managed with `gordon apps secrets`, values in pass under `gordon/apps/<uuid>/<service>/<name>`).

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
pass insert gordon/auth/token_secret
```

**Usage in config:**
```toml
[auth]
token_secret = "gordon/auth/token_secret"  # Path in pass store
```

**Benefits:**
- GPG-encrypted storage
- Version control friendly (encrypted files)
- Standard Unix tooling
- Works with team GPG keys

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

**Benefits:**
- Multiple encryption backends (AWS KMS, GCP KMS, Azure Key Vault, PGP)
- YAML/JSON file encryption
- Git-friendly (encrypted files in repo)

**Security:**
- Absolute paths are rejected to prevent arbitrary file access
- Path traversal (`..`) is blocked
- Only relative paths from your config directory are allowed

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
│       └── token_secret
```

**Usage:**
```bash
# Create secret
mkdir -p ~/.gordon/secrets/gordon/auth
echo "your-token-secret" > ~/.gordon/secrets/gordon/auth/token_secret
```

> **Warning:** Only use for local development. Secrets are stored in plain text.

## App Secret Values

App secret names are registered in the app manifest (`[services.<name>.secrets]` maps ENV name to secret name); values are written separately and stay in pass:

```bash
gordon apps secrets set blog --service web DATABASE_URL=...
```

Values affect the next deploy/restart, never running containers. Only key names are ever echoed back, never values. See [App Manifest](./apps.md) and [Apps CLI](../cli/apps.md).

### Secret files

A secret declared with `as = "file"` is delivered as a file instead of an env value:

```toml
[services.web.secrets]
DATABASE_URL = { name = "database-url", as = "file" }
```

- The container sees a read-only file at `/run/secrets/DATABASE_URL` and the env var `DATABASE_URL_FILE=/run/secrets/DATABASE_URL`.
- On the host, Gordon writes one file per secret under `<data_dir>/secrets-run/<app>/<container>/`, with directories `0700` and files `0444`. The files persist so containers restarted by the runtime or after a reboot still find them.
- Gordon removes a container's files once the container is confirmed gone, and sweeps files of unknown containers at startup.
- Exclude `<data_dir>/secrets-run` from host backups: it holds plain secret values.
- Env-delivered secrets stay visible through `docker inspect` and `/proc/1/environ` inside the container. Prefer file delivery for apps that read `*_FILE` variables.
- An older Gordon v3 binary ignores file delivery and injects those secrets as env vars again. Its leftover `secrets-run` directories are harmless and are swept by the next start of a version that supports file delivery.

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

## Security Recommendations

1. **Production**: Always use `pass` or `sops` backend
2. **Never commit**: Don't commit unencrypted secrets to git (app files must never contain secret values)
3. **Rotate regularly**: Regenerate tokens and passwords periodically
4. **Least privilege**: Use separate secrets per environment
5. **Path validation**: SOPS provider rejects absolute paths and path traversal attempts for security
6. **File delivery**: Use `as = "file"` for app secrets when the image supports `*_FILE` variables (see [Secret files](#secret-files))
7. **Hardening**: See [Security Hardening](./security-hardening.md) for the runtime guard, process protection, and credential scoping

## Related

- [Authentication](./auth.md)
- [App Manifest](./apps.md)
- [Security Hardening](./security-hardening.md)
- [Configuration Overview](./index.md)
