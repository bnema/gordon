# gordon start

Start the Gordon server with registry and proxy components.

## Synopsis

```bash
gordon start [options]
```

## Options

| Option | Short | Default | Description |
|--------|-------|---------|-------------|
| `--config` | `-c` | Auto-detected | Path to configuration file |

## Description

Starts the Gordon server, which includes:

- **Container Registry** - Receives image pushes on the registry port
- **HTTP Proxy** - Routes traffic to containers on the proxy port
- **Event Bus** - Coordinates deployments and updates
- **Config Watcher** - Monitors configuration file for changes

## Configuration File Detection

Gordon looks for configuration in this order:

1. Path specified with `--config`
2. `/etc/gordon/gordon.toml`
3. `~/.config/gordon/gordon.toml`
4. `./gordon.toml`

## First Run

On first run without a config file, Gordon creates a default configuration at `~/.config/gordon/gordon.toml`.

```bash
# First run - creates default config
gordon start
# Edit the config, then restart
```

## Examples

### Basic Start

```bash
gordon start
```

### With Custom Config

```bash
gordon start --config /path/to/gordon.toml
gordon start -c ./my-config.toml
```

### With Environment Override

```bash
GORDON_SERVER_PORT=8080 gordon start
GORDON_LOGGING_LEVEL=debug gordon start
```

## Signals

Gordon responds to these signals:

| Signal | Action |
|--------|--------|
| `SIGTERM` | Graceful shutdown |
| `SIGINT` | Graceful shutdown (Ctrl+C) |
| `SIGUSR1` | Reload configuration |

## Process Management

### Running in Foreground

```bash
gordon start
# Press Ctrl+C to stop
```

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
ExecStart=/usr/local/bin/gordon start

[Install]
WantedBy=default.target
EOF

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now gordon
sudo loginctl enable-linger $USER
```

### Check Status

```bash
# systemd
systemctl --user status gordon

# Process
pgrep -f "gordon start"
```

## Startup Sequence

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

## Shutdown Sequence

1. Receive shutdown signal
2. Stop accepting new requests
3. Complete in-flight requests
4. Stop managed containers (if configured)
5. Remove PID file
6. Exit

## Logs

View startup and runtime logs:

```bash
# Using gordon logs
gordon logs -f

# Direct file access
tail -f ~/.gordon/logs/gordon.log

# With systemd
journalctl --user -u gordon -f
```

## Related

- [CLI Overview](./index.md)
- [Configuration](../config/index.md)
- [Installation](../installation.md)
