# Logging Configuration

Configure Gordon process and HTTP access logging. Workload logs are read directly from the container runtime.

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

[logging.access_log]
enabled = false
format = "json"
output = "stdout"
exclude_health_checks = true
syslog_identifier = "gordon-access"
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
| `file.enabled` | bool | `false` | Enable process file logging |
| `file.path` | string | `{data_dir}/logs/gordon.log` | Process log path |
| `file.max_size` | int | `100` | Max file size in MB before rotation |
| `file.max_backups` | int | `3` | Number of old files to keep |
| `file.max_age` | int | `28` | Days to keep old files |

The Admin API and `gordon logs` read from the process log file. Keep `logging.file.enabled` set to `true` if you need process log streaming.

### Workload Logs

`gordon apps logs APP --service SERVICE` reads stdout and stderr directly from the container runtime. Gordon does not persist workload logs to files; configure retention in the container runtime's logging driver.

The accepted `logging.container_logs` configuration fields are currently not connected to a production log sink and do not create files.

### Access Log

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `access_log.enabled` | bool | `false` | Enable a dedicated HTTP access log |
| `access_log.format` | string | `"json"` | Access log format: `json`, `clf`, `combined` |
| `access_log.output` | string | `"stdout"` | Output sink: `stdout`, `file`, `journald` |
| `access_log.file_path` | string | - | File path when `output = "file"` |
| `access_log.max_size` | int | `100` | Max file size in MB for file output |
| `access_log.max_backups` | int | `3` | Old files to keep for file output |
| `access_log.max_age` | int | `28` | Days to keep file output |
| `access_log.exclude_health_checks` | bool | `true` | Skip health and readiness checks |
| `access_log.syslog_identifier` | string | `"gordon-access"` | Journald identifier |

Use the access log for reverse-proxy traffic analysis, CrowdSec/fail2ban ingestion, or request auditing without mixing entries into the main process log.

## Log Levels

| Level | Description |
|-------|-------------|
| `trace` | Very detailed debugging |
| `debug` | Debug information |
| `info` | General information (default) |
| `warn` | Warnings |
| `error` | Errors only |

## Log Rotation

Process file logs and file-based access logs rotate by size and retain files by count and age. Old files are compressed.

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

[logging.access_log]
enabled = true
format = "json"
output = "file"
file_path = "./logs/access.log"
exclude_health_checks = true
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

[logging.access_log]
enabled = true
format = "json"
output = "journald"
syslog_identifier = "gordon-access"
```

### Minimal (Console Only)

```toml
[logging]
level = "info"
format = "console"

[logging.file]
enabled = false
```

## Viewing Logs

### Gordon Process Logs

```bash
gordon logs -f
gordon logs -n 100
tail -f ~/.gordon/logs/gordon.log
journalctl --user -u gordon -f
```

### Workload Logs

```bash
gordon apps logs blog --service web
gordon apps logs blog --service web --follow
```

These commands stream from the container runtime; they do not read Gordon-managed workload log files.

## Security

Process and access log files are created with owner-only permissions. Gordon redacts common credential patterns when serving logs, but application output may still contain sensitive data. Restrict process-log, journal, runtime, and `admin:logs:read` access.

## Related

- [Configuration Overview](./index.md)
- [CLI Commands](../cli/index.md)
- [Troubleshooting](../reference/troubleshooting.md)
