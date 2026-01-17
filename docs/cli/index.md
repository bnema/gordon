# CLI Commands

Gordon provides a command-line interface for server management, deployment, and authentication.

Commands are organized by where they run:

- **Server Commands** - Must run on the machine hosting Gordon
- **Management Commands** - Work locally or remotely via `--remote` flag
- **Client Commands** - CLI utilities that don't require a running Gordon server

## Server Commands (local only)

| Command | Description | Documentation |
|---------|-------------|---------------|
| `gordon serve` | Start the Gordon server | [serve](./serve.md) |
| `gordon auth` | Manage Gordon server authentication | [auth](./auth.md) |

## Management Commands (local or --remote)

| Command | Description | Documentation |
|---------|-------------|---------------|
| `gordon routes` | Manage routes | [routes](./routes.md) |
| `gordon attachments` | Manage container attachments | [attachments](./attachments.md) |
| `gordon secrets` | Manage secrets | [secrets](./secrets.md) |
| `gordon deploy` | Manually deploy or redeploy a route | [serve](./serve.md#gordon-deploy) |
| `gordon reload` | Reload configuration and sync containers | [serve](./serve.md#gordon-reload) |
| `gordon logs` | Display Gordon process or container logs | [serve](./serve.md#gordon-logs) |
| `gordon status` | Show Gordon server status | - |

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

# View logs
gordon logs                          # Gordon process logs
gordon logs -f                       # Follow process logs
gordon logs -n 100                   # Last 100 lines
gordon logs myapp.local              # Container logs for myapp.local
gordon logs myapp.local -f           # Follow container logs

# Check version
gordon version

# Authentication
gordon auth token generate --subject ci-bot --expiry 0
gordon auth token list
gordon auth token revoke <token-id>
gordon auth password hash
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
gordon secrets list myapp.local
gordon secrets set myapp.local DATABASE_URL "postgres://..."
gordon secrets remove myapp.local DATABASE_URL

# Remotes
gordon remotes add prod https://gordon.mydomain.com --token $TOKEN
gordon remotes list
gordon remotes use prod
```

## Global Options

| Option | Description |
|--------|-------------|
| `-c, --config` | Path to configuration file |
| `--remote` | Remote Gordon URL (e.g., `https://gordon.mydomain.com`) |
| `--token` | Authentication token for remote |

### Remote Targeting

The CLI can target remote Gordon instances using the `--remote` flag or `GORDON_REMOTE` environment variable.

**Important:** The remote URL must be the `gordon_domain` configured on the remote Gordon instance. This is the domain that serves both the container registry and the Admin API.

```bash
# Using flags (use the gordon_domain from remote Gordon config)
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN

# Using environment variables
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=$TOKEN
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
