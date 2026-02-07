# Backups Configuration

Configure automatic PostgreSQL logical backups for attachment containers.

## Overview

When backups are enabled, Gordon detects PostgreSQL attachments for each route and can:

- Run on-demand backups via `gordon backup run`
- Store backup artifacts on the Gordon host filesystem
- Apply retention policies per schedule tier

Current scope:

- PostgreSQL 17 and 18
- Logical dumps (`pg_dump -Fc`)
- Local filesystem storage

## Configuration

```toml
[backups]
enabled = true
storage_dir = "~/.gordon/backups"

[backups.retention]
hourly = 24
daily = 7
weekly = 4
monthly = 12
```

## Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `backups.enabled` | bool | `false` | Enables backup service wiring |
| `backups.storage_dir` | string | `""` | Root backup directory. If empty, defaults to `{server.data_dir}/backups` |
| `backups.retention.hourly` | int | `0` | Number of hourly backups to keep per DB |
| `backups.retention.daily` | int | `0` | Number of daily backups to keep per DB |
| `backups.retention.weekly` | int | `0` | Number of weekly backups to keep per DB |
| `backups.retention.monthly` | int | `0` | Number of monthly backups to keep per DB |

## Storage Layout

Backups are stored under:

```text
<storage_dir>/backups/<domain>/<db>/<schedule>/<timestamp>.bak
```

Example:

```text
~/.gordon/backups/backups/app.example.com/postgres/daily/20260207T110000Z.bak
```

## Notes

- Backups require PostgreSQL attachment containers to be running.
- The backup command executes inside the database container using Gordon runtime APIs.
- Retention applies per database and per schedule.

## Related

- [Attachments Configuration](./attachments.md)
- [CLI Backup Command](../cli/backup.md)
- [Configuration Reference](./reference.md)
