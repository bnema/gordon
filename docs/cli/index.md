# CLI Reference

Gordon provides a command-line interface for server management and authentication.

## Commands

| Command | Description |
|---------|-------------|
| `gordon start` | Start the Gordon server |
| `gordon reload` | Reload configuration and sync containers |
| `gordon logs` | Display Gordon process logs |
| `gordon version` | Print version information |
| `gordon auth` | Manage registry authentication |

## Quick Reference

```bash
# Start Gordon
gordon start
gordon start --config /path/to/config.toml

# Reload configuration
gordon reload

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
```

## Global Options

| Option | Description |
|--------|-------------|
| `-c, --config` | Path to configuration file |

## Command Details

### gordon start

Starts the Gordon server with registry and proxy components.

```bash
gordon start [options]
```

**Options:**
- `-c, --config string` - Path to config file

**Example:**
```bash
gordon start
gordon start --config ~/.config/gordon/gordon.toml
gordon start -c /etc/gordon/gordon.toml
```

See [gordon start](./start.md) for details.

### gordon reload

Sends a reload signal to the running Gordon process. Reloads configuration and syncs containers to match.

```bash
gordon reload
```

This sends `SIGUSR1` to the Gordon process, triggering:
- Configuration file reload
- Route synchronization
- Attachment deployment

### gordon logs

Displays logs from the Gordon process.

```bash
gordon logs [options]
```

**Options:**
- `-c, --config string` - Path to config file
- `-f, --follow` - Follow log output (like `tail -f`)
- `-n, --lines int` - Number of lines to show (default: 50)

**Examples:**
```bash
gordon logs              # Last 50 lines
gordon logs -f           # Follow logs
gordon logs -n 100       # Last 100 lines
gordon logs -f -n 200    # Follow, starting from last 200 lines
```

### gordon version

Prints version information.

```bash
gordon version
```

**Output:**
```
Gordon v2.0.0
Commit: abc1234
Build Date: 2024-01-15
```

### gordon auth

Manage registry authentication. See [gordon auth](./auth.md) for details.

```bash
gordon auth token generate   # Generate JWT token
gordon auth token list       # List all tokens
gordon auth token revoke     # Revoke a token
gordon auth password hash    # Generate bcrypt hash
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
GORDON_SERVER_PORT=8080 gordon start
GORDON_LOGGING_LEVEL=debug gordon start
```

Pattern: `GORDON_SECTION_KEY` (uppercase, underscores)

## Related

- [gordon start](./start.md)
- [gordon auth](./auth.md)
- [Configuration Reference](../config/index.md)
