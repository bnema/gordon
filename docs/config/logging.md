# Logging Configuration

Configure application and container log collection.

## Configuration

```toml
[logging]
level = "info"
format = "console"

[logging.file]
enabled = true
path = "~/.gordon/logs/gordon.log"
max_size = 100
max_backups = 3
max_age = 28

[logging.container_logs]
enabled = true
dir = "~/.gordon/logs/containers"
max_size = 100
max_backups = 3
max_age = 28
```

## Options

### General Logging

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `level` | string | `"info"` | Log level: trace, debug, info, warn, error |
| `format` | string | `"console"` | Output format: console or json |

### File Logging

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `file.enabled` | bool | `false` | Enable file-based logging |
| `file.path` | string | - | Path to main log file |
| `file.max_size` | int | `100` | Max file size in MB before rotation |
| `file.max_backups` | int | `3` | Number of old files to keep |
| `file.max_age` | int | `28` | Days to keep old files |

### Container Logs

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `container_logs.enabled` | bool | `true` | Collect container stdout/stderr |
| `container_logs.dir` | string | `{data_dir}/logs/containers` | Directory for container logs |
| `container_logs.max_size` | int | `100` | Max file size in MB |
| `container_logs.max_backups` | int | `3` | Old files to keep |
| `container_logs.max_age` | int | `28` | Days to keep files |

## Log Levels

| Level | Description |
|-------|-------------|
| `trace` | Very detailed debugging |
| `debug` | Debug information |
| `info` | General information (default) |
| `warn` | Warnings |
| `error` | Errors only |

## Log Directory Structure

```
~/.gordon/logs/
├── gordon.log              # Main application logs
├── gordon.log.1            # Rotated log
├── gordon.log.2.gz         # Compressed old log
├── proxy.log               # HTTP proxy traffic
└── containers/
    ├── abc123def.log       # Container by ID
    ├── app_mydomain_com.log  # Symlink by domain
    └── api_mydomain_com.log
```

## Log Rotation

Gordon uses automatic log rotation:

1. **Size-based**: Rotates when file exceeds `max_size` MB
2. **Age-based**: Removes files older than `max_age` days
3. **Count-based**: Keeps only `max_backups` old files
4. **Compression**: Old files are gzip compressed

Example with defaults:
- Log grows to 100MB → rotated to `gordon.log.1`
- After 3 rotations → oldest is compressed
- Files older than 28 days → deleted

## Examples

### Development

```toml
[logging]
level = "debug"
format = "console"

[logging.file]
enabled = true
path = "./logs/gordon.log"
max_size = 10
max_backups = 2
max_age = 7

[logging.container_logs]
enabled = true
dir = "./logs/containers"
max_size = 10
max_backups = 2
max_age = 7
```

### Production

```toml
[logging]
level = "info"
format = "json"

[logging.file]
enabled = true
path = "~/.gordon/logs/gordon.log"
max_size = 100
max_backups = 10
max_age = 90

[logging.container_logs]
enabled = true
dir = "~/.gordon/logs/containers"
max_size = 100
max_backups = 10
max_age = 90
```

### Minimal (Console Only)

```toml
[logging]
level = "info"
format = "console"

[logging.file]
enabled = false

[logging.container_logs]
enabled = true
```

## Viewing Logs

### Gordon Logs

```bash
# Using gordon logs command
gordon logs -f          # Follow logs
gordon logs -n 100      # Last 100 lines

# Direct file access
tail -f ~/.gordon/logs/gordon.log

# With journalctl (if using systemd)
journalctl --user -u gordon -f
```

### Container Logs

```bash
# View container logs
tail -f ~/.gordon/logs/containers/app_mydomain_com.log

# All container logs
ls ~/.gordon/logs/containers/
```

## Log Format

### Console Format

```
2024-01-15T10:30:00Z INF container deployed domain=app.mydomain.com image=myapp:latest
2024-01-15T10:30:01Z INF proxy routing updated route=app.mydomain.com
```

### JSON Format

```json
{"level":"info","time":"2024-01-15T10:30:00Z","message":"container deployed","domain":"app.mydomain.com","image":"myapp:latest"}
{"level":"info","time":"2024-01-15T10:30:01Z","message":"proxy routing updated","route":"app.mydomain.com"}
```

## Security

Log files are created with secure permissions:
- Directories: `0700` (owner only)
- Files: `0600` (owner only)

Sensitive values (passwords, tokens) are automatically redacted.

## Related

- [Configuration Overview](./index.md)
- [CLI Commands](../cli/index.md)
- [Troubleshooting](../reference/troubleshooting.md)
