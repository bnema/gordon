# Server Commands

Commands for running and managing the Gordon server process.

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
gordon deploy <domain> [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name of the route to deploy (required) |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

### Description

**Local mode:** Sends `SIGUSR2` to the Gordon process with the specified domain.

**Remote mode:** Calls the remote Gordon Admin API to trigger deployment.

Both modes trigger:

- Fresh image pull (always pulls latest, ignoring cache)
- Container redeployment for the specified route

### Examples

```bash
# Local deployment
gordon deploy myapp.example.com
gordon deploy api.example.com

# Remote deployment (override)
gordon deploy myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### Use Cases

- Recover from a failed deployment
- Force redeploy without pushing a new image
- Manual deployment when automatic deploy didn't trigger
- Trigger deployments on remote Gordon instances from CI/CD

---

## gordon logs

Display Gordon process logs or container logs.

### Synopsis

```bash
gordon logs [domain] [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `[domain]` | Optional. Container domain to view logs for. Without this, shows Gordon process logs. |

### Options

| Option | Short | Default | Description |
|--------|-------|---------|-------------|
| `--config` | `-c` | Auto | Path to config file |
| `--follow` | `-f` | false | Follow log output (like `tail -f`) |
| `--lines` | `-n` | 50 | Number of lines to show |
| `--remote` | | | Remote Gordon URL |
| `--token` | | | Authentication token for remote |

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

### Examples

```bash
# Gordon process logs
gordon logs              # Last 50 lines
gordon logs -f           # Follow logs
gordon logs -n 100       # Last 100 lines
gordon logs -f -n 200    # Follow, starting from last 200 lines

# Container logs
gordon logs myapp.local           # Last 50 lines from container
gordon logs myapp.local -f        # Follow container logs
gordon logs myapp.local -n 100    # Last 100 lines from container

# Remote mode (override)
gordon logs --remote https://gordon.mydomain.com --token $TOKEN
gordon logs myapp.local --remote https://gordon.mydomain.com --token $TOKEN
```

### Log Locations

```bash
# Using gordon logs
gordon logs -f

# Direct file access
tail -f ~/.gordon/logs/gordon.log

# With systemd
journalctl --user -u gordon -f

# Container logs via docker (local alternative)
docker logs --tail 50 myapp.local
docker logs -f myapp.local
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

## Related

- [CLI Overview](./index.md)
- [Configuration Reference](../config/index.md)
