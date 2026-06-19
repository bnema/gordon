# Backup Command

Manage database backups and volume backups.

## gordon backups databases

Database backups are logical PostgreSQL backups made with `pg_dump`.

```bash
gordon backups databases <subcommand>
```

Subcommands:

- `list [domain]` - List database backups
- `run <domain> [--db <name>]` - Trigger an immediate database backup
- `detect <domain>` - Detect supported databases for a domain
- `status` - Show database backup status

Compatibility aliases remain available: `gordon backups list`, `run`, `detect`, and `status` map to database backups.

## gordon backups volumes

Volume backups are best-effort filesystem archives of Gordon-managed named volumes, uploaded to S3.

```bash
gordon backups volumes <subcommand>
```

Subcommands:

- `list [domain]` - List completed volume backup archives
- `run [domain] [--volume <name>]` - Trigger volume backups now
- `status` - Show completed archives plus current/recent in-memory job state

Volume backups exclude bind mounts, tmpfs mounts, anonymous volumes, and non-Gordon volumes. Live archives are not application-consistent unless the application is quiesced or stopped.

## Examples

```bash
# Database backups
gordon backups databases list
gordon backups databases run app.example.com --db postgres
gordon backups databases detect app.example.com
gordon backups databases status

# Volume backups
gordon backups volumes list
gordon backups volumes list app.example.com
gordon backups volumes run
gordon backups volumes run app.example.com --volume gordon-app-example-com-data
gordon backups volumes status
```

## JSON Output

Database and volume list commands support `--json`.

```bash
gordon backups volumes list --json
```

## Required Permissions

- Read operations (`list`, `status`, `detect`) require `admin:status:read`.
- `run` requires `admin:config:write`.

## Related

- [CLI Commands](./index.md)
- [Backups Configuration](../config/backups.md)
