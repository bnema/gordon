# Remote CLI Management

Manage Gordon instances remotely using the CLI with the `--remote` flag or saved remotes.

## What You'll Learn

- Using global `--remote` and `--token` flags
- Saving and managing remote connections
- Environment variable configuration
- Token security best practices
- Remote command workflows

## Prerequisites

- Gordon CLI installed
- A remote Gordon instance running with admin API enabled
- Authentication token for the remote instance

## Understanding the Remote URL

Default: use the Gordon registry domain configured on the remote instance.

```toml
# On the remote Gordon server
[server]
registry_domain = "reg.example.com"  # Use this as --remote URL
```

The CLI connects to `https://reg.example.com/admin/*` endpoints.

Private tailnet setup (recommended for hardened VPS): keep using the HTTPS domain, point that DNS record to the VPS tailnet IP, and set insecure TLS for self-signed certs.

```bash
# Save remote once
gordon remotes add tailnet-reg https://gordon.example.com --token-env GORDON_TOKEN --insecure
gordon remotes use tailnet-reg

# Use it for auth/admin commands
gordon auth login
gordon routes list
```

Equivalent `remotes.toml` entry:

```toml
active = "tailnet-reg"

[remotes.tailnet-reg]
url = "https://gordon.example.com"
token_env = "GORDON_TOKEN"
insecure_tls = true
```

Use this mode when registry/auth/admin access is intentionally restricted to tailnet CIDRs.

## Quick Start

### One-off Remote Command

```bash
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN
```

### Using Environment Variables

```bash
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=$TOKEN
gordon routes list
```

### Using Saved Remotes

```bash
# Add a remote
gordon remotes add prod https://gordon.mydomain.com --token-env PROD_TOKEN

# Set as active
gordon remotes use prod

# Now use without flags
gordon routes list
```

## Global Flags

These flags are available on all commands:

| Flag | Description |
|------|-------------|
| `--remote <URL>` | Remote Gordon URL |
| `--token <TOKEN>` | Authentication token |
| `--insecure` | Skip TLS verification (self-signed/private certs) |

```bash
# List routes on remote
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN

# Manage secrets on remote
gordon secrets list myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GORDON_REMOTE` | Remote Gordon URL |
| `GORDON_TOKEN` | Authentication token |

Environment variables are useful for CI/CD pipelines and shell sessions:

```bash
# Set for current session
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...

# Commands automatically use these
gordon routes list
gordon secrets list myapp.example.com
```

## Saved Remotes

Save frequently used remotes to avoid repeating URLs and tokens.

### Configuration File

Remotes are stored in `~/.config/gordon/remotes.toml`:

```toml
active = "prod"

[remotes.prod]
url = "https://gordon.mydomain.com"
token_env = "PROD_TOKEN"

[remotes.staging]
url = "https://staging.mydomain.com"
token = "eyJ..."
```

### Managing Remotes

#### List Remotes

```bash
gordon remotes list
```

Output:

```
Saved Remotes

Name            URL                                    Token           Status
──────────────────────────────────────────────────────────────────────────────
prod            https://gordon.mydomain.com            $PROD_TOKEN     active
staging         https://staging.mydomain.com           set
dev             https://dev.mydomain.com               none
```

#### Add a Remote

```bash
# Basic (no token)
gordon remotes add prod https://gordon.mydomain.com

# With direct token
gordon remotes add prod https://gordon.mydomain.com --token eyJ...

# With environment variable reference (recommended)
gordon remotes add prod https://gordon.mydomain.com --token-env PROD_TOKEN
```

| Flag | Description |
|------|-------------|
| `--token <TOKEN>` | Store token directly in config |
| `--token-env <VAR>` | Store env variable name (token resolved at runtime) |

#### Authenticate with Password Auth

For servers using password authentication, you can log in interactively:

```bash
# Login to active remote
gordon auth login

# Login to specific remote
gordon auth login --remote prod

# Pre-fill username
gordon auth login --username admin
```

This prompts for your username and password, authenticates with the remote server, and stores the returned token automatically.

> **Note:** Only works with servers that have password authentication enabled (`username` + `password_hash` configured). For token-only servers, use `gordon remotes set-token` instead.

#### Set Token Manually

For servers using token-based auth, or to update a token manually:

```bash
# Set token directly
gordon remotes set-token prod eyJhbGciOiJIUzI1NiIs...

# Set token from file
gordon remotes set-token staging $(cat token.txt)
```

#### Remove a Remote

```bash
# With confirmation prompt
gordon remotes remove staging

# Skip confirmation
gordon remotes remove staging --force
```

#### Set Active Remote

```bash
gordon remotes use prod
```

When a remote is active, it's used automatically for all remote-capable commands:

```bash
gordon remotes use prod
gordon routes list              # Uses prod remote
gordon secrets list app.com     # Uses prod remote
```

## Resolution Precedence

When multiple sources specify remote or token, the CLI uses this priority order:

**Remote URL:**
1. `--remote` flag
2. `GORDON_REMOTE` environment variable
3. Active remote from `remotes.toml`

**Token:**
1. `--token` flag
2. `GORDON_TOKEN` environment variable
3. Token from active remote in `remotes.toml`

This allows overriding specific values while keeping defaults:

```bash
# Active remote is prod, but use different token
gordon routes list --token $TEMPORARY_TOKEN
```

## Commands Supporting Remote

These commands work with remote targeting:

| Command | Description |
|---------|-------------|
| `gordon routes list` | List all routes |
| `gordon routes add <domain> <image>` | Add a route |
| `gordon routes remove <domain>` | Remove a route |
| `gordon routes deploy <domain>` | Deploy/redeploy a route |
| `gordon attachments list [target]` | List all or targeted attachments |
| `gordon attachments add <target> <image>` | Add an attachment |
| `gordon attachments remove <target> <image>` | Remove an attachment |
| `gordon secrets list <domain>` | List secrets for a domain |
| `gordon secrets set <domain> KEY=value` | Set secrets |
| `gordon secrets remove <domain> <key>` | Remove a secret |
| `gordon status` | Show server status |
| `gordon reload` | Reload config and start new routes |
| `gordon logs` | View Gordon process logs |
| `gordon logs <domain>` | View container logs |

### Remote Logs Requirement

The `gordon logs` command requires **file logging to be enabled** on the remote server. Without it, you'll get a "failed to get logs" error.

On the remote Gordon server, add to `config.toml`:

```toml
[logging.file]
enabled = true
# path is optional - defaults to {data_dir}/logs/gordon.log
# path = "/var/log/gordon/gordon.log"
```

Then restart Gordon. The log directory will be created automatically in your data directory (e.g., `~/.gordon/logs/`).

If you specify a custom path, ensure the directory exists and is writable:

```bash
sudo mkdir -p /var/log/gordon
sudo chown gordon:gordon /var/log/gordon  # adjust user as needed
```

> **Note:** When running as a systemd service, Gordon logs to journalctl by default. However, the remote admin API cannot read from journalctl—it reads from the configured log file. Both can be used simultaneously.

## Token Security

### Option 1: Environment Variable Reference (Recommended)

Store the environment variable name, not the actual token:

```bash
gordon remotes add prod https://gordon.mydomain.com --token-env PROD_TOKEN
```

The `remotes.toml` stores only:

```toml
[remotes.prod]
url = "https://gordon.mydomain.com"
token_env = "PROD_TOKEN"
```

At runtime, Gordon reads `$PROD_TOKEN` from the environment.

**Benefits:**
- Token not stored in plaintext config
- Works with secret managers that inject env vars
- Easy rotation without editing config

### Option 2: Direct Token Storage

```bash
gordon remotes add prod https://gordon.mydomain.com --token eyJ...
```

The token is stored directly in `remotes.toml`:

```toml
[remotes.prod]
url = "https://gordon.mydomain.com"
token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
```

**Security note:** Ensure `remotes.toml` has restricted permissions:

```bash
chmod 600 ~/.config/gordon/remotes.toml
```

### Option 3: Environment Variables Only

For maximum security, don't save tokens at all:

```bash
# Add remote without token
gordon remotes add prod https://gordon.mydomain.com

# Set token via environment
export GORDON_TOKEN=$TOKEN

# Use normally
gordon remotes use prod
gordon routes list
```

## Workflow Examples

### Development Workflow

```bash
# Add dev and staging remotes
gordon remotes add dev https://gordon.dev.local --token-env DEV_TOKEN
gordon remotes add staging https://gordon.staging.example.com --token-env STAGING_TOKEN

# Work with dev
gordon remotes use dev
gordon routes list
gordon routes add myapp.dev.local myapp:latest

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

```bash
# GitLab CI example
deploy:
  script:
    - export GORDON_REMOTE=$GORDON_URL
    - export GORDON_TOKEN=$GORDON_TOKEN
    - gordon routes deploy myapp.example.com
```

### Multi-Environment Management

```bash
# Add all environments
gordon remotes add prod https://gordon.example.com --token-env PROD_TOKEN
gordon remotes add staging https://gordon.staging.example.com --token-env STAGING_TOKEN
gordon remotes add dev https://gordon.dev.example.com --token-env DEV_TOKEN

# Compare routes across environments
gordon routes list --remote https://gordon.example.com --token $PROD_TOKEN
gordon routes list --remote https://gordon.staging.example.com --token $STAGING_TOKEN

# Or switch active
gordon remotes use prod && gordon routes list
gordon remotes use staging && gordon routes list
```

## Troubleshooting

### "unauthorized" Error

Token is missing, expired, or invalid.

```bash
# Check if token is set
echo $GORDON_TOKEN

# Verify token works
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN
```

### "connection refused" Error

Remote Gordon isn't running or URL is wrong.

```bash
# Verify URL is correct
curl https://gordon.mydomain.com/health

# Check if admin API is enabled on remote
# The remote gordon.toml needs:
# [admin]
# enabled = true
```

### Remote Not Found

The specified remote doesn't exist in `remotes.toml`.

```bash
# List available remotes
gordon remotes list

# Add the remote
gordon remotes add myremote https://gordon.mydomain.com
```

### Active Remote Cleared

After removing the active remote, you need to set a new active:

```bash
gordon remotes use prod
```

Or use explicit flags:

```bash
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN
```

### "failed to get logs" Error

The remote Gordon server doesn't have file logging enabled.

```bash
# Error: failed to get process logs: 500 Internal Server Error: failed to get logs
```

On the remote server, enable file logging in `config.toml`:

```toml
[logging.file]
enabled = true
path = "/var/log/gordon/gordon.log"
```

See [Remote Logs Requirement](#remote-logs-requirement) for details.

## Related

- [CLI Commands](/docs/cli/index.md)
- [Authentication](/docs/config/auth.md)
- [Attachments](/docs/config/attachments.md)
