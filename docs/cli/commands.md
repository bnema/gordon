# CLI Commands

Gordon provides a command-line interface for server management, deployment, and authentication.

## Commands Overview

| Command | Description |
|---------|-------------|
| `gordon serve` | Start the Gordon server |
| `gordon reload` | Reload configuration and sync containers |
| `gordon deploy` | Manually deploy or redeploy a specific route |
| `gordon logs` | Display Gordon process logs |
| `gordon version` | Print version information |
| `gordon auth` | Manage registry authentication |
| `gordon routes` | Manage routes (local or remote) |
| `gordon secrets` | Manage secrets (remote only) |
| `gordon remotes` | Manage saved remote Gordon instances |

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
gordon logs
gordon logs -f
gordon logs -n 100

# Check version
gordon version

# Authentication
gordon auth token generate --subject ci-bot --expiry 0
gordon auth token list
gordon auth token revoke <token-id>
gordon auth password hash
gordon auth internal
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

---

## gordon serve

Start the Gordon server with registry and proxy components.

### Synopsis

```bash
gordon serve [options]
```

### Options

| Option | Short | Default | Description |
|--------|-------|---------|-------------|
| `--config` | `-c` | Auto-detected | Path to configuration file |

### Description

Starts the Gordon server, which includes:

- **Container Registry** - Receives image pushes on the registry port
- **HTTP Proxy** - Routes traffic to containers on the proxy port
- **Event Bus** - Coordinates deployments and updates
- **Config Watcher** - Monitors configuration file for changes

### Configuration File Detection

Gordon looks for configuration in this order:

1. Path specified with `--config`
2. `/etc/gordon/gordon.toml`
3. `~/.config/gordon/gordon.toml`
4. `./gordon.toml`

### First Run

On first run without a config file, Gordon creates a default configuration at `~/.config/gordon/gordon.toml`.

```bash
# First run - creates default config
gordon serve
# Edit the config, then restart
```

### Examples

```bash
# Basic start
gordon serve

# With custom config
gordon serve --config /path/to/gordon.toml
gordon serve -c ./my-config.toml

# With environment override
GORDON_SERVER_PORT=8080 gordon serve
GORDON_LOGGING_LEVEL=debug gordon serve
```

### Signals

Gordon responds to these signals:

| Signal | Action |
|--------|--------|
| `SIGTERM` | Graceful shutdown |
| `SIGINT` | Graceful shutdown (Ctrl+C) |
| `SIGUSR1` | Reload configuration |
| `SIGUSR2` | Manual deploy (used by `gordon deploy`) |

### Running with systemd

```bash
# Create user service
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/gordon.service <<EOF
[Unit]
Description=Gordon Container Platform

[Service]
Type=simple
Restart=always
ExecStart=/usr/local/bin/gordon serve

[Install]
WantedBy=default.target
EOF

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now gordon
sudo loginctl enable-linger $USER
```

### Startup Sequence

1. Load configuration
2. Initialize logger
3. Create PID file
4. Connect to Docker/Podman runtime
5. Create storage directories
6. Initialize services (registry, proxy, auth)
7. Register event handlers
8. Start config file watcher
9. Sync existing containers
10. Start HTTP servers

### Shutdown Sequence

1. Receive shutdown signal
2. Stop accepting new requests
3. Complete in-flight requests
4. Stop managed containers (if configured)
5. Remove PID file
6. Exit

---

## gordon reload

Reload configuration and sync containers to match.

### Synopsis

```bash
gordon reload
```

### Description

Sends `SIGUSR1` to the running Gordon process, triggering:

- Configuration file reload
- Route synchronization
- Deployment of containers for routes missing containers
- Attachment deployment

### Example

```bash
# After editing gordon.toml, apply changes without restart
vim ~/.config/gordon/gordon.toml
gordon reload
```

---

## gordon deploy

Manually deploy or redeploy a specific route.

### Synopsis

```bash
gordon deploy <domain>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name of the route to deploy (required) |

### Description

Sends `SIGUSR2` to the Gordon process with the specified domain, triggering:

- Fresh image pull (always pulls latest, ignoring cache)
- Container redeployment for the specified route

### Examples

```bash
gordon deploy myapp.example.com
gordon deploy api.example.com
```

### Use Cases

- Recover from a failed deployment
- Force redeploy without pushing a new image
- Manual deployment when automatic deploy didn't trigger

---

## gordon logs

Display Gordon process logs.

### Synopsis

```bash
gordon logs [options]
```

### Options

| Option | Short | Default | Description |
|--------|-------|---------|-------------|
| `--config` | `-c` | Auto | Path to config file |
| `--follow` | `-f` | false | Follow log output (like `tail -f`) |
| `--lines` | `-n` | 50 | Number of lines to show |

### Examples

```bash
gordon logs              # Last 50 lines
gordon logs -f           # Follow logs
gordon logs -n 100       # Last 100 lines
gordon logs -f -n 200    # Follow, starting from last 200 lines
```

### Log Locations

```bash
# Using gordon logs
gordon logs -f

# Direct file access
tail -f ~/.gordon/logs/gordon.log

# With systemd
journalctl --user -u gordon -f
```

---

## gordon version

Print version information.

### Synopsis

```bash
gordon version
```

### Output

```
Gordon v2.0.0
Commit: abc1234
Build Date: 2024-01-15
```

---

## gordon auth

Manage registry authentication tokens and passwords.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `token generate` | Generate a new JWT token |
| `token list` | List all stored tokens |
| `token revoke` | Revoke a token by ID |
| `password hash` | Generate bcrypt password hash |
| `internal` | Show internal registry credentials |

### gordon auth token generate

Generate a new JWT authentication token for registry access.

```bash
gordon auth token generate --subject <name> [options]
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `--subject` | (required) | Username/subject for the token |
| `--scopes` | `push,pull` | Comma-separated scopes |
| `--expiry` | `720h` | Duration until expiry (0 = never) |
| `-c, --config` | Auto | Path to config file |

**Examples:**

```bash
# CI token that never expires
gordon auth token generate --subject github-actions --expiry 0

# Read-only token
gordon auth token generate --subject reader --scopes pull --expiry 720h

# Push-only token for build system
gordon auth token generate --subject builder --scopes push --expiry 0

# Temporary token (24 hours)
gordon auth token generate --subject temp --expiry 24h
```

**Output:**

```
Token generated successfully!
Subject: github-actions
Scopes: push, pull
Expiry: never

Token (use as password with docker login):
eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...

Usage: docker login -u github-actions -p <token> <registry>
```

### gordon auth token list

List all stored authentication tokens.

```bash
gordon auth token list [options]
```

**Output:**

```
ID                                    Subject               Expires               Revoked
------------------------------------------------------------------------------------------
a1b2c3d4-e5f6-7890-abcd-ef1234567890  github-actions        never                 no
b2c3d4e5-f6a7-8901-bcde-f12345678901  deploy-bot            2024-02-15 10:30      no
c3d4e5f6-a7b8-9012-cdef-123456789012  old-token             2024-01-01 00:00      yes
```

### gordon auth token revoke

Revoke a token by its ID.

```bash
gordon auth token revoke <token-id> [options]
```

**Example:**

```bash
gordon auth token revoke a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

**Output:**

```
Token a1b2c3d4-e5f6-7890-abcd-ef1234567890 has been revoked.
```

### gordon auth password hash

Generate a bcrypt hash for password authentication.

```bash
gordon auth password hash
```

Interactively prompts for a password and outputs the bcrypt hash.

**Output:**

```
Enter password: ********

Bcrypt hash (store in your secrets backend):
$2a$10$N9qo8uLOickgx2ZMRZoMye...

Then reference the path in your config:
  [registry_auth]
  type = "password"
  password_hash = "gordon/registry/password_hash"
```

### gordon auth internal

Display the auto-generated internal registry credentials.

```bash
gordon auth internal
```

Gordon generates temporary credentials for internal communication with its local registry. These credentials are useful for manual recovery when debugging deployment issues.

**Output:**

```
Internal Registry Credentials
==============================
Username: gordon-internal
Password: 8f3a2b1c4d5e6f7a8b9c0d1e2f3a4b5c...

Usage:
  docker login localhost:5000 -u gordon-internal -p 8f3a2b1c4d5e6f7a8b9c0d1e2f3a4b5c...
```

**Notes:**
- Credentials are regenerated each time Gordon starts
- Only available while Gordon is running
- Used for manual `docker pull` from the local registry during debugging

**Use cases:**
- Debugging image pull failures
- Manual recovery after deployment issues
- Inspecting images in the local registry

---

## Token Scopes

| Scope | Permission |
|-------|------------|
| `push` | Push images to registry |
| `pull` | Pull images from registry |
| `push,pull` | Both push and pull (default) |

## Token Expiry Formats

| Format | Duration |
|--------|----------|
| `0` | Never expires |
| `24h` | 24 hours |
| `720h` | 30 days |
| `8760h` | 1 year |

---

## Workflow Examples

### CI/CD Setup

```bash
# Generate token for CI
gordon auth token generate --subject ci-bot --scopes push,pull --expiry 0

# Save token as CI secret
# GitHub: Settings → Secrets → New repository secret
# GitLab: Settings → CI/CD → Variables
```

### Docker Login

```bash
# Using generated token
docker login -u ci-bot -p <token> registry.mydomain.com

# In CI/CD
echo "$GORDON_TOKEN" | docker login -u ci-bot --password-stdin registry.mydomain.com
```

### Token Rotation

```bash
# Generate new token
gordon auth token generate --subject ci-bot --expiry 0

# Update CI secrets with new token

# Revoke old token
gordon auth token revoke <old-token-id>
```

### Password Setup

```bash
# Generate hash
gordon auth password hash
# Enter: mypassword

# Store in pass
pass insert gordon/registry/password_hash
# Paste: $2a$10$...

# Configure gordon.toml
[registry_auth]
enabled = true
type = "password"
username = "deploy"
password_hash = "gordon/registry/password_hash"
```

---

## gordon routes

Manage routes on local or remote Gordon instances.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all routes |
| `add` | Add a new route |
| `remove` | Remove a route |
| `deploy` | Deploy a specific route |

### gordon routes list

List all configured routes.

```bash
gordon routes list
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN
```

### gordon routes add

Add a new route.

```bash
gordon routes add <domain> <image>
gordon routes add myapp.example.com myapp:latest
```

### gordon routes remove

Remove a route.

```bash
gordon routes remove <domain>
gordon routes remove myapp.example.com
```

### gordon routes deploy

Deploy or redeploy a specific route.

```bash
gordon routes deploy <domain>
gordon routes deploy myapp.example.com
```

---

## gordon secrets

Manage secrets on remote Gordon instances. Requires remote targeting.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all secrets |
| `get` | Get a secret value |
| `set` | Set a secret value |
| `delete` | Delete a secret |

### Examples

```bash
# List secrets
gordon secrets list --remote https://gordon.mydomain.com --token $TOKEN

# Set a secret
gordon secrets set DATABASE_URL "postgres://..." --remote ... --token ...

# Get a secret
gordon secrets get DATABASE_URL --remote ... --token ...

# Delete a secret
gordon secrets delete DATABASE_URL --remote ... --token ...
```

---

## gordon remotes

Manage saved remote Gordon instances for easier CLI usage.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List saved remotes |
| `add` | Add a new remote |
| `remove` | Remove a saved remote |
| `use` | Set active remote |

### Examples

```bash
# Add a remote
gordon remotes add prod https://gordon.mydomain.com --token $TOKEN

# List saved remotes
gordon remotes list

# Set active remote
gordon remotes use prod

# Now use without --remote flag
gordon routes list
```

---

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
