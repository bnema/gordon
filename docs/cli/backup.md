# Backup Command

Manage backups for database attachments.

## gordon backups

```bash
gordon backups <subcommand>
```

Subcommands:

- `list [domain]` - List backups globally or for one domain
- `run <domain> [--db <name>]` - Trigger an immediate backup
- `detect <domain>` - Detect supported databases for a domain
- `status` - Show backup status across domains

> Backup commands work both locally and remotely.
>
> - Local mode (no `--remote`) executes backups through in-process services.
> - Remote mode uses the admin API on the target instance.
>
> `gordon backups run <domain>` auto-selects the database only when exactly one supported DB attachment is detected. If multiple DB attachments are present, pass `--db <name>`.

In normal usage, configure remotes once (`gordon remotes ...`) and run backup commands without per-command remote flags.

## Examples

```bash
# List all backups
gordon backups list

# List backups for one domain
gordon backups list app.example.com

# Trigger backup (auto-detect when exactly one DB attachment exists)
gordon backups run app.example.com

# Trigger backup for specific attachment name
gordon backups run app.example.com --db postgres

# Detect DB attachments
gordon backups detect app.example.com

# Status summary
gordon backups status
```

## Output Columns

### list

```text
<domain>\t<db>\t<status>\t<started_at>\t<backup_id>
```

### detect

```text
<name>\t<type>\t<host>\t<port>\t<image>
```

### status

```text
<domain>\t<db>\t<status>\t<started_at>
```

### run

```text
<domain>\t<db>\t<status>\t<started_at>\t<backup_id>\t<size_bytes>
```

## Required Permissions

- Read operations (`list`, `status`, `detect`) require `admin:status:read`.
- `run` requires `admin:config:write`.

## Related

- [CLI Commands](./index.md)
- [Backups Configuration](../config/backups.md)
