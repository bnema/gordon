# Upgrading Gordon

This guide covers breaking changes and migration steps between major versions.

## v2.30.0 to v2.31.0

### Breaking: Default Port Changes

Gordon now defaults to unprivileged ports that work without root or firewall rules:

| Setting | Old default | New default |
|---------|-------------|-------------|
| `server.port` | `80` | `8088` |
| `server.registry_port` | `5000` | `5000` (unchanged) |
| `server.tls_port` | *(n/a)* | `8443` |

**If you relied on the old defaults** (no explicit `port` in your config), add the port explicitly:

```toml
[server]
port = 80
```

Or set up firewall port forwarding to the new defaults:

```bash
sudo firewall-cmd --permanent --add-forward-port=port=80:proto=tcp:toport=8088
sudo firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=8443
sudo firewall-cmd --reload
```

### New: Internal Certificate Authority

Gordon now includes an internal CA for automatic on-demand TLS. Set `tls_port = 0` to disable. See [Server Configuration](./config/server.md#internal-ca-and-tls) for details.

### Required for Cloudflare/Proxy Setups: `proxy_allowed_ips`

The internal CA's HTTP onboarding gate rejects non-localhost HTTP requests by default. If Gordon sits behind Cloudflare or another reverse proxy, add the proxy's edge IPs to `proxy_allowed_ips`:

```toml
[server]
proxy_allowed_ips = [
  "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
  "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
  "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
  "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
  "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
]
```

Without this, all proxied HTTP traffic returns `403 Forbidden`. Set `tls_port = 0` to disable the internal CA and skip this requirement.

### Breaking: `server.gordon_domain` Replaces `server.registry_domain`

Gordon now uses `server.gordon_domain` as the public registry and admin host. Migrate older configs that still set only `server.registry_domain` before restarting:

**Before:**

```toml
[server]
registry_domain = "gordon.example.com"
```

**After:**

```toml
[server]
gordon_domain = "gordon.example.com"
```

If you do not migrate, `gordon status --remote ...` and `gordon routes list --remote ...` can fail with `/auth/token` `404`, and `reg-domain/v2/` or `/admin/status` can return `404`.

## v2.16.0 to v2.30.0

### Breaking: Password Authentication Removed

Gordon v2.30.0 removes password-based authentication entirely. Only token-based authentication is supported.

**What changed:**

- `auth.type = "password"` is no longer accepted
- `auth.password` and `auth.password_hash` config fields are removed
- The `gordon auth password hash` CLI command is removed
- The `gordon auth login` command now requires `--token` (no more interactive password prompt)
- The `/auth/password` endpoint now returns `410 Gone`
- Long-lived tokens are no longer accepted on admin/registry endpoints — they must be exchanged for ephemeral tokens via `/auth/token`

**New features:**

- `auth.access_token_ttl` configures the lifetime of ephemeral access tokens issued by `/auth/token` (default: `"15m"`)
- `gordon auth show-token` prints the stored token for a remote
- `gordon auth logout` removes the stored token (with optional `--revoke`)
- Automatic token exchange: the CLI transparently exchanges long-lived tokens for ephemeral ones before API calls
- Admin scopes (`admin:*:*`, `admin:routes:read`, etc.) allow fine-grained access control for remote CLI operations

**Migration steps:**

1. **Before upgrading**, generate a token on your current Gordon instance:

   ```bash
   gordon auth token generate --subject deploy --scopes "push,pull" --expiry 0
   ```

   Save this token securely. You will need it after upgrading. Admin scopes (`admin:*:*`) are only available after upgrading to v2.30.0 — regenerate your token with admin scopes after the upgrade if needed.

2. **Update remotes** to use the generated token:

   ```bash
   gordon auth login --token <token>
   # or
   gordon remotes set-token prod <token>
   ```

3. **Update your config** to remove password fields:

   **Before (v2.16.0):**

   ```toml
   [auth]
   enabled = true
   secrets_backend = "pass"
   username = "deploy"
   password_hash = "gordon/auth/password_hash"
   token_secret = "gordon/auth/token_secret"
   ```

   **After (v2.30.0):**

   ```toml
   [auth]
   enabled = true
   secrets_backend = "pass"
   token_secret = "gordon/auth/token_secret"
   access_token_ttl = "15m"
   ```

4. **Update CI/CD pipelines** to use `GORDON_TOKEN` environment variable for authentication. See the [deployment guides](./deployment/index.md).

5. **Upgrade the binary** and restart:

   ```bash
   curl -fsSL https://gordon.bnema.dev/install | bash
   systemctl --user restart gordon
   ```

6. **Verify** the server starts without errors:

   ```bash
   journalctl --user -u gordon -f
   ```

### New: Configurable Access Token TTL

Ephemeral access tokens issued by `/auth/token` now have a configurable lifetime via `auth.access_token_ttl` (default `"15m"`, maximum `"1h"`).

Tokens with a lifetime at or below `MaxAccessTokenLifetime` (1 hour) are treated as ephemeral: they skip token store validation for performance, which means they **cannot be individually revoked**. They become invalid only when they naturally expire. Shortening `auth.access_token_ttl` only reduces the exposure window; it does not enable per-token revocation. If you need explicit revocation, use a stored long-lived token instead of `/auth/token`.

```toml
[auth]
access_token_ttl = "30m"
```

### New: Admin Scopes

Tokens can now include fine-grained admin scopes for remote CLI operations:

```bash
# Full admin access
gordon auth token generate --subject admin --scopes "push,pull,admin:*:*" --expiry 0

# Read-only monitoring
gordon auth token generate --subject monitor --scopes "admin:status:read" --expiry 30d

# CI deploy with route read + config write
gordon auth token generate --subject ci --scopes "push,pull,admin:routes:read,admin:config:write" --expiry 0
```

See [Token Scopes](./config/auth.md#token-scopes) for the full list.

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
- **Auth login command**: `gordon auth login --remote <name>` for token authentication
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
