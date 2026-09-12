# Backup Command

Manage app database backups and app volume backups.

Backups are identified by app, service, and the declared resource. Domains are
routing addresses and are never a backup identity.

## Declaring backup targets

A service declares its databases and volumes in the app manifest, and lists
which of them are backed up:

```toml
[[service]]
name = "api"
image = "registry.example.com/shop/api:1.4.2"

[[service.database]]
name = "orders"
type = "postgres"
schedule = "daily"

[[service.backup]]
postgres = ["orders"]

[[service.volume]]
name = "data"
path = "/var/lib/data"

[service.backup]
volume = ["data"]
```

A declared database or volume that the service's backup declaration does not
reference is not a backup target.

## gordon backups

Database backups are logical PostgreSQL backups made with `pg_dump`.

```bash
gordon backups <subcommand>
```

Subcommands:

- `list [app]` - List stored backups, for every app or one app
- `run <app> --service <service> --database <database>` - Run one declared
  database backup now
- `status` - Show stored backups plus declared targets that have no completed
  backup yet

## gordon backups volume

Volume backups are best-effort filesystem archives of the app's declared
volumes, uploaded to S3.

```bash
gordon backups volume <subcommand>
```

Subcommands:

- `list [app]` - List completed volume backup archives
- `run <app> --service <service> --volume <volume>` - Run one declared volume
  backup now
- `status` - Show completed archives plus current/recent in-memory job state

Volume archives are not application-consistent unless the application is
quiesced or stopped.

## Selectors

`--service` and the resource flag (`--database`, `--volume`) select the target:

- both given: the target must exist, otherwise the command reports not found;
- one omitted: it is only allowed when exactly one compatible target remains;
- several candidates: the command reports the ambiguity and lists the safe
  `service/resource` names.

Nothing is ever chosen by guessing.

## Examples

```bash
# Database backups
gordon backups list
gordon backups list shop
gordon backups run shop --service api --database orders
gordon backups status

# Volume backups
gordon backups volume list
gordon backups volume list shop
gordon backups volume run shop --service api --volume data
gordon backups volume status
```

## Scheduling

The installation backup schedule runs every declared database whose own
`schedule` matches the firing tier, and applies retention under the app name.
Volume declarations carry no schedule of their own: the installation volume
backup interval runs the declared volume targets.

## JSON Output

Every list, run, and status command supports `--json`.

```bash
gordon backups volume list --json
```

## Required Permissions

- Read operations (`list`, `status`) require `admin:status:read`.
- `run` requires `admin:config:write`.

## Related

- [CLI Commands](./index.md)
- [Backups Configuration](../config/backups.md)
- [Apps Configuration](../config/apps.md)
