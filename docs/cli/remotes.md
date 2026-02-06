# Remotes Commands

Manage saved remote Gordon instances for easier CLI usage.

## gordon remotes

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List saved remotes |
| `add` | Add a new remote |
| `remove` | Remove a saved remote |
| `use` | Set active remote |
| `set-token` | Set or update token for a remote |

---

## gordon remotes list

List all saved remotes.

```bash
gordon remotes list
```

### Output

```
Saved Remotes

Name            URL                                    Token           Status
-------------------------------------------------------------------------------
prod            https://gordon.mydomain.com            $PROD_TOKEN     active
staging         https://staging.mydomain.com           set
dev             https://dev.mydomain.com               none
```

---

## gordon remotes add

Add a new remote.

```bash
gordon remotes add <name> <url> [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<name>` | Name for the remote (e.g., `prod`, `staging`) |
| `<url>` | Gordon URL (the `gordon_domain` from remote config) |

### Options

| Option | Description |
|--------|-------------|
| `--token` | Store token directly in config |
| `--token-env` | Store environment variable name (token resolved at runtime) |

### Examples

```bash
# Basic (no token)
gordon remotes add prod https://gordon.mydomain.com

# With direct token
gordon remotes add prod https://gordon.mydomain.com --token eyJ...

# With environment variable reference (recommended)
gordon remotes add prod https://gordon.mydomain.com --token-env PROD_TOKEN
```

### Token Security

**Option 1: Environment Variable Reference (Recommended)**

```bash
gordon remotes add prod https://gordon.mydomain.com --token-env PROD_TOKEN
```

The `remotes.toml` stores only the variable name:

```toml
[remotes.prod]
url = "https://gordon.mydomain.com"
token_env = "PROD_TOKEN"
```

At runtime, Gordon reads `$PROD_TOKEN` from the environment.

**Option 2: Direct Token Storage**

```bash
gordon remotes add prod https://gordon.mydomain.com --token eyJ...
```

The token is stored directly in `~/.config/gordon/remotes.toml`. Ensure restricted permissions:

```bash
chmod 600 ~/.config/gordon/remotes.toml
```

---

## gordon remotes remove

Remove a saved remote.

```bash
gordon remotes remove <name>
gordon remotes remove <name> --force  # Skip confirmation
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<name>` | Name of the remote to remove |

### Options

| Option | Description |
|--------|-------------|
| `--force` | Skip confirmation prompt |

### Example

```bash
gordon remotes remove staging
```

---

## gordon remotes use

Set the active remote.

```bash
gordon remotes use <name>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<name>` | Name of the remote to set as active |

### Description

When a remote is active, it's used automatically for all remote-capable commands without needing to specify `--remote` and `--token`:

```bash
gordon remotes use prod
gordon routes list              # Uses prod remote automatically
gordon secrets list app.com     # Uses prod remote automatically
```

### Example

```bash
gordon remotes use prod
```

---

## gordon remotes set-token

Set or update the authentication token for a saved remote.

```bash
gordon remotes set-token <name> <token>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<name>` | Name of the remote |
| `<token>` | The JWT token to set |

### Description

This command is useful when:

- The server uses token-based authentication (not password auth)
- You have a pre-generated token from `gordon auth token generate`
- You want to update an expired token

For servers with password authentication, use `gordon auth login` instead.

### Examples

```bash
# Set token for prod remote
gordon remotes set-token prod eyJhbGciOiJIUzI1NiIs...

# Set token from file
gordon remotes set-token staging $(cat token.txt)

# Set token from environment variable
gordon remotes set-token prod "$GORDON_TOKEN"
```

### Output

```
✓ Token updated for remote 'prod'
```

---

## Configuration File

Remotes are stored in `~/.config/gordon/remotes.toml`:

```toml
active = "prod"

[remotes.prod]
url = "https://gordon.mydomain.com"
token_env = "PROD_TOKEN"
insecure_tls = true

[remotes.staging]
url = "https://staging.mydomain.com"
token = "eyJ..."
```

## Resolution Precedence

When multiple sources specify remote or token, the CLI uses this priority:

**Remote URL:**
1. `--remote` flag
2. `GORDON_REMOTE` environment variable
3. Active remote from `remotes.toml`

**Token:**
1. `--token` flag
2. `GORDON_TOKEN` environment variable
3. Token from active remote in `remotes.toml`

**Insecure TLS:**
1. `--insecure` flag
2. `GORDON_INSECURE` environment variable (`true`/`false`)
3. `[client] insecure_tls` in `gordon.toml`
4. `insecure_tls` from the selected remote in `remotes.toml`

This allows overriding specific values while keeping defaults:

```bash
# Active remote is prod, but use different token
gordon routes list --token $TEMPORARY_TOKEN
```

---

## Workflow Examples

### Multi-Environment Setup

```bash
# Add all environments
gordon remotes add prod https://gordon.example.com --token-env PROD_TOKEN
gordon remotes add staging https://gordon.staging.example.com --token-env STAGING_TOKEN
gordon remotes add dev https://gordon.dev.example.com --token-env DEV_TOKEN

# Work with prod
gordon remotes use prod
gordon routes list
gordon secrets list myapp.example.com

# Switch to staging
gordon remotes use staging
gordon routes list
```

### CI/CD Pipeline

```yaml
# GitHub Actions example
env:
  GORDON_REMOTE: ${{ secrets.GORDON_URL }}
  GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}

steps:
  - name: Deploy to Gordon
    run: |
      gordon routes deploy myapp.example.com
```

### Compare Environments

```bash
# Compare routes across environments
gordon routes list --remote https://gordon.example.com --token $PROD_TOKEN
gordon routes list --remote https://gordon.staging.example.com --token $STAGING_TOKEN

# Or switch between active remotes
gordon remotes use prod && gordon routes list
gordon remotes use staging && gordon routes list
```

### Tailscale Admin + Wildcard App Domains

Use this pattern when Gordon admin/API is reachable only on a Tailscale path (self-signed or private cert),
while app traffic still uses your public wildcard DNS and reverse-proxy routing.

```toml
# ~/.config/gordon/remotes.toml
active = "prod-ts"

[remotes.prod-ts]
url = "https://gordon-backend.tailnet.ts.net"
token_env = "GORDON_TOKEN"
insecure_tls = true
```

```bash
# CLI calls Gordon admin API over Tailscale with insecure TLS allowed
gordon status
gordon routes list
gordon deploy app.bnema.dev
```

`insecure_tls` only affects CLI -> Gordon admin HTTPS verification. It does not change
your runtime routing: the Gordon reverse proxy and container routes can still serve
`*.bnema.dev` behind your normal wildcard DNS setup.

## Related

- [CLI Overview](./index.md)
- [Remote CLI Guide](/wiki/guides/remote-cli.md)
