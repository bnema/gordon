# CLI Commands

Gordon provides a command-line interface for server management, deployment, and authentication.

Most `list` commands also support `--json` for machine-readable output.

Commands are organized by where they run:

- **Server Commands** - Must run on the machine hosting Gordon
- **Management Commands** - Work locally or remotely
- **Client Commands** - CLI utilities that don't require a running Gordon server

## Server Commands (local only)

| Command | Description | Documentation |
|---------|-------------|---------------|
| `gordon serve` | Start the Gordon server | [serve](./serve.md) |
| `gordon auth` | Manage Gordon server authentication | [auth](./auth.md) |
| `gordon ca` | Manage the internal Certificate Authority | [ca](./ca.md) |

## Management Commands (local or remote)

Management commands run locally through in-process services by default. Add `--remote` to target another Gordon instance.

| Command | Description | Documentation |
|---------|-------------|---------------|
| `gordon attachments` | Manage container attachments | [attachments](./attachments.md) |
| `gordon autoroute` | Manage auto-route domain allowlist | [autoroute](./autoroute.md) |
| `gordon backups` | Manage database backups | [backup](./backup.md) |
| `gordon bootstrap` | Configure a route, attachments, and secrets for an app | [bootstrap](./bootstrap.md) |
| `gordon config show` | Show server configuration | [config](./config.md) |
| `gordon deploy` | Manually deploy or redeploy a route | [serve](./serve.md#gordon-deploy) |
| `gordon images` | List and prune images | [images](./images.md) |
| `gordon logs` | Display Gordon process or container logs | [serve](./serve.md#gordon-logs) |
| `gordon networks list` | List Gordon-managed Docker networks | [networks](./networks.md) |
| `gordon preview` | Create or manage preview environments | [preview](../config/preview.md) |
| `gordon push` | Tag, push, and optionally deploy an image | [push](./push.md) |
| `gordon attachments push` | Build/push attachment images to registry | [attachments](./attachments.md) |
| `gordon reload` | Reload configuration and sync containers | [serve](./serve.md#gordon-reload) |
| `gordon restart` | Restart a running container | [restart](./restart.md) |
| `gordon rollback` | Roll back to a previous image version | [rollback](./rollback.md) |
| `gordon routes` | Manage routes | [routes](./routes.md) |
| `gordon secrets` | Manage secrets | [secrets](./secrets.md) |
| `gordon status` | Show Gordon server status | [status](./status.md) |
| `gordon volumes` | Manage volumes | - |

## Client Commands

| Command | Description | Documentation |
|---------|-------------|---------------|
| `gordon remotes` | Manage saved remote Gordon instances | [remotes](./remotes.md) |
| `gordon version` | Print version information | [serve](./serve.md#gordon-version) |
| `gordon completion` | Generate shell autocompletion scripts | - |

## Quick Reference

```bash
# Start Gordon
gordon serve
gordon serve --config /path/to/config.toml

# Reload configuration
gordon reload

# Deploy a specific route
gordon deploy myapp.example.com

# Restart a running container
gordon restart myapp.example.com

# First-time route setup
gordon bootstrap app.example.com myapp:latest --attachment postgres:18 --env APP_ENV=production

# Then push and deploy
gordon push app.example.com --build --no-confirm

# Push an image and deploy
gordon push myapp.example.com --build

# Push and deploy without confirmation
gordon push myapp.example.com --no-confirm

# Roll back to a previous tag
gordon rollback myapp.example.com

# View logs
gordon logs                          # Gordon process logs
gordon logs -f                       # Follow process logs
gordon logs -n 100                   # Last 100 lines
gordon logs myapp.example.com       # Container logs for myapp.example.com
gordon logs myapp.example.com -f    # Follow container logs

# Check version
gordon version

# Backups
gordon backups list
gordon backups run app.example.com
gordon backups detect app.example.com
gordon backups status

# Images
gordon images list
gordon images prune --runtime-only
gordon images prune --keep 3

# Authentication
gordon auth login --remote https://gordon.example.com --token $TOKEN
gordon auth status
gordon auth show-token
gordon auth logout
gordon auth token generate --subject ci-bot --expiry 0
gordon auth token list
gordon auth token revoke <token-id>
gordon auth internal

# Routes
gordon routes list
gordon routes add myapp.example.com myapp:latest
gordon routes remove myapp.example.com
gordon routes deploy myapp.example.com

# Attachments
gordon attachments list
gordon attachments add app.example.com postgres:18
gordon attachments remove app.example.com postgres:18

# Secrets
gordon secrets list myapp.example.com
gordon secrets set myapp.example.com DATABASE_URL "postgres://..."
gordon secrets remove myapp.example.com DATABASE_URL

# Remotes
gordon remotes add prod https://gordon.mydomain.com --token $TOKEN
gordon remotes list
gordon remotes use prod

# Preview environments
gordon preview create app.example.com --branch feature-x
gordon preview list
gordon preview extend app.example.com --branch feature-x
gordon preview delete app.example.com --branch feature-x

# Volumes
gordon volumes list
gordon volumes prune

# Auto-route allowlist
gordon autoroute allow list
gordon autoroute allow add example.com
gordon autoroute allow add "*.staging.example.com"
gordon autoroute allow remove example.com
```

## Global Options

| Option | Description |
|--------|-------------|
| `-c, --config` | Path to configuration file |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |
| `--insecure` | Skip TLS certificate verification for remote HTTPS endpoints |

### Remote Targeting

The CLI can target remote Gordon instances using client config, an active remote, `--remote`,
or `GORDON_REMOTE` environment variable. Use `--remote` and `--token` as global overrides
when you want to bypass your saved configuration.

**Important:** The remote URL must be the `gordon_domain` configured on the remote Gordon instance. This is the domain that serves both the container registry and the Admin API.
If remote CLI gets a `404` during `/auth/token` exchange, the server likely still sets only `server.registry_domain` and needs `server.gordon_domain` configured.

Use `--insecure` when the remote endpoint uses a self-signed or otherwise untrusted TLS certificate.
You can make this persistent with `insecure_tls = true` in `[client]` of `~/.config/gordon/gordon.toml`
or in a specific entry in `~/.config/gordon/remotes.toml`.
For Tailscale setups, you can also avoid `--insecure` by using the machine `*.ts.net` name with Tailscale-issued TLS certs in Gordon server config.
Your public app domains can still use wildcard DNS and normal reverse-proxy routing.

```bash
# Using flags (use the gordon_domain from remote Gordon config)
gordon routes list --remote https://gordon.example.com --token $TOKEN

# Against self-signed/private CA endpoint
gordon --remote https://gordon.example.com --token $TOKEN --insecure status

# Using environment variables
export GORDON_REMOTE=https://gordon.example.com
export GORDON_TOKEN=$TOKEN
export GORDON_INSECURE=true
gordon routes list
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Configuration error |

## Environment Variables

Gordon reads configuration from environment variables:

```bash
GORDON_SERVER_PORT=8080 gordon serve
GORDON_LOGGING_LEVEL=debug gordon serve
```

Pattern: `GORDON_SECTION_KEY` (uppercase, underscores)

## Related

- [Core Concepts](../concepts.md)
- [Configuration Reference](../config/index.md)
