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
- **Reverse Proxy** - Routes app hosts to recorded loopback backends
- **Event Bus** - Coordinates runtime notifications
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
GORDON_LOGGING_LEVEL=debug gordon serve
```

### Signals

Gordon responds to these signals:

| Signal | Action |
|--------|--------|
| `SIGTERM` | Graceful shutdown |
| `SIGINT` | Graceful shutdown (Ctrl+C) |
| `SIGUSR1` | Reload installation configuration |
| `SIGUSR2` | Reserved; no app deployment action |

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
9. Start HTTP servers
10. Run best-effort startup recovery (reconcile apps intended to run from active state)

### Startup Recovery

After Gordon starts, including after a host reboot, it runs a best-effort recovery pass once the listeners are bound. Errors are logged, but Gordon keeps starting.

1. **Reconcile app boot state** - Gordon ensures apps intended to run are running from active state (stopped-intent apps stay stopped, pending desired revisions are never activated).
4. **Start the background monitor** - Ongoing crash recovery resumes after the startup pass.

Reload and startup recovery never activate pending desired app state, re-resolve images, or flip intent. They touch installation settings and runtime reconciliation only.

### Shutdown Sequence

1. Receive shutdown signal
2. Stop accepting new requests
3. Complete in-flight requests
4. Stop managed containers (if configured)
5. Remove PID file
6. Exit

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
