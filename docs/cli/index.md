# CLI Commands

Gordon provides a command-line interface for server management, app deployment, and authentication.

Most `list`/`show` commands also support `--json` for machine-readable output.

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

Management commands use the authenticated daemon API. By default they connect through the owner-only local Unix socket; add `--remote` to use authenticated HTTP/TLS against another Gordon instance.

| Command | Description | Documentation |
|---------|-------------|---------------|
| `gordon apps` | Manage applications, operations, and app secrets | [apps](./apps.md) |
| `gordon backups` | Manage declared app database and volume backups | [backup](./backup.md) |
| `gordon daemon` | Daemon status, logs, reload, config, TLS, traffic, and networks | [daemon](./daemon.md) |
| `gordon images` | Push, list, and prune images | [images](./images.md) |
| `gordon volumes` | Manage volumes | [volumes](./volumes.md) |

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

# Reload installation configuration (never activates app state)
gordon daemon reload

# Applications (daemon-owned; fail without a reachable daemon)
gordon apps apply --file blog.toml
gordon apps apply --file blog.toml --deploy
gordon apps list
gordon apps show blog
gordon apps diff blog
gordon apps deploy blog
gordon apps restart blog
gordon apps stop blog
gordon apps start blog
gordon apps remove blog
gordon apps operations show blog --key <operation-key>
gordon apps operations watch blog --key <operation-key>
gordon apps secrets list blog

# Daemon
gordon daemon status
gordon daemon config show
gordon daemon config validate --file /path/to/gordon.toml
gordon daemon tls
gordon daemon networks

# Push an image (OCI transfer only; deploy separately)
gordon images push myapp --build --remote prod

# View logs
gordon daemon logs                                      # Gordon process logs
gordon daemon logs -f                                   # Follow process logs
gordon daemon logs -n 100                               # Last 100 process-log lines
gordon apps logs blog --service web              # App service logs
gordon apps logs blog --service web --follow     # Follow app service logs

# Check version
gordon version

# Traffic plane
gordon daemon traffic --remote prod
gordon daemon traffic --remote prod --json

# Backups
gordon backups list
gordon backups run shop --service api --database orders
gordon backups volume run shop --service api --volume data
gordon backups status

# Images
gordon images push myapp --build --remote prod
gordon images list
gordon images prune --dry-run
gordon images prune --keep-releases 3

# Authentication
gordon auth login --remote https://gordon.example.com --token $TOKEN
gordon auth status
gordon auth show-token
gordon auth logout
gordon auth token generate --subject ci-bot --expiry 0
gordon auth token list
gordon auth token revoke <token-id>
gordon auth internal

# Remotes
gordon remotes add prod https://gordon.mydomain.com --token $TOKEN
gordon remotes list
gordon remotes use prod

# Volumes
gordon volumes list
gordon volumes prune
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

When no explicit remote is selected and no active remote is configured, Gordon can
**auto-infer a saved remote** for `gordon images push`. It probes your saved remotes and uses the
remote automatically when exactly one matches. If multiple remotes match, Gordon stops with
an ambiguity error and asks you to use `--remote`. If any remote probe fails, Gordon also
stops rather than guessing.

```bash
# Auto-inferred single match from saved remotes
gordon images push myapp --build
```

**Important:** The remote URL must be the `gordon_domain` configured on the remote Gordon instance. This is the domain that serves both the container registry and the Admin API.

Use `--insecure` when the remote endpoint uses a self-signed or otherwise untrusted TLS certificate.
You can make this persistent with `insecure_tls = true` in `[client]` of `~/.config/gordon/gordon.toml`
or in a specific entry in `~/.config/gordon/remotes.toml`.

```bash
# Using flags (use the gordon_domain from remote Gordon config)
gordon daemon status --remote https://gordon.example.com --token $TOKEN

# Against self-signed/private CA endpoint
gordon --remote https://gordon.example.com --token $TOKEN --insecure daemon status

# Using environment variables
export GORDON_REMOTE=https://gordon.example.com
export GORDON_TOKEN=$TOKEN
export GORDON_INSECURE=true
gordon daemon status
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
GORDON_LOGGING_LEVEL=debug gordon serve
```

Pattern: `GORDON_SECTION_KEY` (uppercase, underscores)

## Related

- [Core Concepts](../concepts.md)
- [Configuration Reference](../config/index.md)
