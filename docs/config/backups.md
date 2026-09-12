# Backups Configuration

Gordon has two backup flows:

- **Database backups**: PostgreSQL logical dumps via `pg_dump`, stored on the local filesystem.
- **Volume backups**: best-effort filesystem archives of Gordon-managed named volumes, uploaded to S3.

Backups are identified by app, service, and the declared database or volume
(see [Apps](../config/apps.md)). Domains are routing addresses and are never a
backup identity.

## Database backups

```toml
[backups.databases]
enabled = true
schedule = "daily"
storage_dir = "~/.gordon/backups"

[backups.databases.retention]
hourly = 24
daily = 7
weekly = 4
monthly = 12
```

| Key | Default | Description |
|---|---:|---|
| `backups.databases.enabled` | `false` | Enables PostgreSQL backup service wiring |
| `backups.databases.schedule` | `"daily"` | Scheduler preset: `hourly`, `daily`, `weekly`, `monthly` |
| `backups.databases.storage_dir` | `""` | Root backup directory; defaults to `{server.data_dir}/backups` |
| `backups.databases.retention.*` | `0` | Number of backups to keep per DB and schedule tier |

## Volume backups to S3

```toml
[backups.volumes]
enabled = true
interval = "24h"
compression = "gzip"
timeout = "2h"
max_concurrency = 2
helper_image = "alpine:3.20"

[backups.volumes.s3]
bucket = "gordon-backups"
region = "eu-west-3"
prefix = "prod/gordon"
# endpoint = "https://s3.example.com"
# path_style = false
# sse_algorithm = "AES256"
# sse_kms_key_id = ""

[backups.volumes.retention]
keep = 14
```

| Key | Default | Description |
|---|---:|---|
| `backups.volumes.enabled` | `false` | Enables automatic volume archives |
| `backups.volumes.interval` | `"24h"` | Interval between scheduled runs |
| `backups.volumes.compression` | `"gzip"` | `gzip` or `zstd`; helper image must provide the tool |
| `backups.volumes.timeout` | `"2h"` | Per-volume archive/upload timeout |
| `backups.volumes.max_concurrency` | `2` | Maximum concurrent volume backups |
| `backups.volumes.helper_image` | `"alpine:3.20"` | Helper container image used for tar/compression |
| `backups.volumes.s3.bucket` | `""` | Required when volume backups are enabled |
| `backups.volumes.s3.region` | `""` | Required when volume backups are enabled |
| `backups.volumes.s3.prefix` | `""` | Object key prefix |
| `backups.volumes.s3.endpoint` | `""` | Optional S3-compatible endpoint |
| `backups.volumes.s3.path_style` | `false` | Use path-style addressing for S3-compatible storage |
| `backups.volumes.retention.keep` | `14` | Completed archives to keep per app + volume |

Volume backup objects are stored under the canonical app name:

```text
<prefix>/apps/<app>/volumes/<volume>/<timestamp>-<id>.tar.gz   # gzip
<prefix>/apps/<app>/volumes/<volume>/<timestamp>-<id>.tar.zst  # zstd
```

Objects written by earlier versions under a domain prefix are not read or
migrated: back them up and re-run the app's volume backups if you need them in
the current layout.

## Notes

- Volume backups archive the app's declared volumes that its backup declaration references.
- Bind mounts, tmpfs mounts, anonymous volumes, and undeclared volumes are never backed up.
- Database backups archive the app's declared PostgreSQL databases that its backup declaration references.
- Live volume archives are best-effort; consistency requires application quiesce, pause, or stop.
- Automated volume restore is not part of the MVP.
- Snapshots are not generic across Docker/Podman volumes and require backend-specific support such as ZFS, Btrfs, LVM, EBS, or a snapshot-capable volume driver.
- S3 credentials should come from the environment, shared config, instance role, or Gordon secret conventions. Use least-privilege IAM for the configured prefix.

## Related

- [App Manifest](./apps.md)
- [CLI Backup Command](../cli/backup.md)
- [Configuration Reference](./reference.md)
