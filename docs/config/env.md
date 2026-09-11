# Environment Variables

App environment comes from two places: public `[env]` in the app file (injected into all services at deploy) and per-service `[service.secrets]` (values in pass, resolved at deploy). This page covers the installation `[env]` store location and the provider syntax used in env files.

## Configuration

```toml
[env]
dir = "~/.gordon/env"  # Default location
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `dir` | string | `~/.gordon/env` | Directory for the installation secret store |

## How It Works

The `[env]` directory backs the installation secret store:

- With the `pass` backend, `gordon secrets set <domain> --from-file` stores per-domain secrets in pass under `gordon/env/<sanitized-domain>/<KEY>` (dots/colons/slashes → underscores). Env files are not used for secrets.
- Existing `.env` files are migrated on startup and renamed to `.env.migrated`.
- With the `sops` or `unsafe` backend, env files remain the source of truth; use `${sops:...}` syntax for encrypted values when `secrets_backend = "sops"`.

App containers do NOT read these files: services receive the app file's `[env]` plus their resolved `[service.secrets]` at deploy time (see [App Manifest](./apps.md)).

## Secret Provider Syntax

Reference secrets from configured backends in env files:

### Pass (Unix Password Manager)

```bash
DATABASE_PASSWORD=${pass:myapp/database/password}
API_SECRET=${pass:company/api-secret}
JWT_KEY=${pass:production/jwt-signing-key}
```

### SOPS (Encrypted Files)

```bash
DATABASE_PASSWORD=${sops:secrets.yaml:database.password}
API_SECRET=${sops:production.yaml:api.secret}
STRIPE_KEY=${sops:secrets.yaml:stripe.api_key}
```

### Syntax Reference

| Provider | Syntax | Example |
|----------|--------|---------|
| pass | `${pass:<path>}` | `${pass:myapp/db-password}` |
| sops | `${sops:<file>:<key.path>}` | `${sops:secrets.yaml:db.password}` |

## File Permissions

The env directory uses secure permissions:
- Directory: `0700` (owner only)
- Files: `0600` (owner only)

## Related

- [Secrets Configuration](./secrets.md)
- [App Manifest](./apps.md)
