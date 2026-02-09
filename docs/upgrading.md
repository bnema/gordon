# Upgrading Gordon

This guide covers breaking changes and migration steps between major versions.

## v2.6.0 to v2.7.0

### Breaking: Token Secret Required

Gordon v2.7.0 requires a `token_secret` for JWT authentication. The server will not start without it configured.

**Error you'll see:**
```
Error: token_secret is required for JWT token generation; set GORDON_AUTH_TOKEN_SECRET environment variable or configure auth.token_secret
```

**Choose one of these migration options:**

#### Option A: Environment Variable (simplest)

```bash
# Generate a random secret
export GORDON_AUTH_TOKEN_SECRET="$(openssl rand -base64 32)"

# Add to your shell profile or systemd service
echo 'export GORDON_AUTH_TOKEN_SECRET="your-secret"' >> ~/.bashrc
```

For systemd services:
```bash
mkdir -p ~/.config/systemd/user/gordon.service.d
cat > ~/.config/systemd/user/gordon.service.d/token.conf << EOF
[Service]
Environment="GORDON_AUTH_TOKEN_SECRET=$(openssl rand -base64 32)"
EOF
systemctl --user daemon-reload
systemctl --user restart gordon
```

#### Option B: Config File with Pass (recommended for production)

```bash
# Generate and store secret in pass
openssl rand -base64 32 | pass insert -e gordon/auth/token_secret

# Update your gordon.toml
```

```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
```

#### Option C: Config File with Unsafe Backend (development only)

```bash
# Generate secret file
mkdir -p ~/.gordon/secrets
openssl rand -base64 32 > ~/.gordon/secrets/token_secret
chmod 600 ~/.gordon/secrets/token_secret
```

```toml
[auth]
enabled = true
secrets_backend = "unsafe"
token_secret = "token_secret"
```

### Breaking: Unsafe Token Store Warning

Using `secrets_backend = "unsafe"` now logs a warning on startup:
```
WRN using unsafe secrets backend - secrets are stored in plain text
```

This is intentional to encourage secure secret storage in production. The warning can be ignored for development.

### New Features

- **Attachment secrets discovery**: `gordon secrets list <domain>` now shows secrets for attachment containers
- **Auth login command**: `gordon auth login --remote <name>` for password authentication
- **Rate limiting**: Configurable rate limits under `[api.rate_limit]`

### Security Improvements

v2.7.0 includes a comprehensive security audit with:

- JWT tokens now include "not before" (nbf) claim
- SSRF protection for external routes
- Security headers middleware
- Rate limiting on registry and token endpoints
- Command injection prevention in pass provider
- Path traversal prevention in secrets store

## v2.5.0 to v2.6.0

### Breaking: Config Restructure

The `[secrets]` and `[registry_auth]` sections were merged into `[auth]`.

**Before (v2.5.0):**
```toml
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "password"
password_hash = "gordon/registry/password_hash"
```

**After (v2.6.0+):**
```toml
[auth]
enabled = true
secrets_backend = "pass"
password_hash = "gordon/auth/password_hash"
```

### Breaking: Auth Enabled by Default

Gordon keeps auth enabled by default. If you set `auth.enabled = false`, Gordon runs in local-only mode (`/admin/*` disabled, `/v2/*` loopback-only).

### Breaking: Secret Paths Changed

If using pass or sops, update your secret paths:
- `gordon/registry/*` → `gordon/auth/*`

## General Upgrade Process

1. **Read the changelog** for your target version
2. **Backup your config** before upgrading
3. **Test in staging** if possible
4. **Upgrade the binary**:
   ```bash
   curl -fsSL https://gordon.bnema.dev/install | bash
   ```
5. **Restart Gordon**:
   ```bash
   systemctl --user restart gordon
   ```
6. **Check logs** for any errors:
   ```bash
   gordon logs
   ```

## Getting Help

- [GitHub Issues](https://github.com/bnema/gordon/issues)
- [Documentation](https://gordon.bnema.dev/docs)
