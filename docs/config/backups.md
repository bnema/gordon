# Backups Configuration

Gordon has two backup flows:

- **Database backups**: PostgreSQL logical dumps via `pg_dump`, stored on the local filesystem.
- **Volume backups**: best-effort filesystem archives of Gordon-managed named volumes, uploaded to S3. Administrative bind mounts are operator-owned host paths and are never archived.

Backups are identified by app, service, and the declared database or volume
(see [Apps](../config/apps.md)). Domains are routing addresses and are never a
backup identity.

## Schedule

Each backup flow runs on one installation-wide schedule. Apps only declare
*what* to back up; the installation decides *when*.

| `schedule` | Runs | Default `time` |
|---|---|---|
| `hourly` | every hour at the `time` minutes | `00:00` (on the hour) |
| `daily` | every day | `02:00` |
| `weekly` | every Sunday | `03:00` |
| `monthly` | the first day of each month | `04:00` |

- `time` is `HH:MM` (24-hour) in the **server's local timezone** (`TZ`, or `/etc/localtime`). The run keeps the same wall-clock time across daylight-saving changes.
- The next run is computed from the current time when Gordon starts. A run that falls while Gordon is down is skipped, not caught up: no workload runs then, so there is nothing new to back up.
- Gordon logs the schedule and `next_run` at startup (`backup scheduler enabled`, `volume backup scheduler enabled`).

## Database backups

```toml
[backups.databases]
enabled = true
schedule = "daily"
time = "03:00"
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
| `backups.databases.schedule` | `"daily"` | Run frequency: `hourly`, `daily`, `weekly`, `monthly`; databases run when their declared `schedule` matches |
| `backups.databases.time` | `""` | `HH:MM` server local time; empty uses the preset default |
| `backups.databases.storage_dir` | `""` | Root backup directory; defaults to `{server.data_dir}/backups` |
| `backups.databases.retention.*` | `0` | Number of backups to keep per DB and schedule tier |

## Volume backups to S3

```toml
[backups.volumes]
enabled = true
schedule = "daily"
time = "03:30"
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
# storage_class = "ONEZONE_IA"

[backups.volumes.retention]
keep = 14
```

| Key | Default | Description |
|---|---:|---|
| `backups.volumes.enabled` | `false` | Enables automatic volume archives |
| `backups.volumes.schedule` | `"daily"` | Run frequency: `hourly`, `daily`, `weekly`, `monthly` |
| `backups.volumes.time` | `""` | `HH:MM` server local time; empty uses the preset default |
| `backups.volumes.compression` | `"gzip"` | `gzip` or `zstd`; helper image must provide the tool |
| `backups.volumes.timeout` | `"2h"` | Per-volume archive/upload timeout |
| `backups.volumes.max_concurrency` | `2` | Volumes archived in parallel during a scheduled run |
| `backups.volumes.helper_image` | `"alpine:3.20"` | Helper container image used for tar/compression |
| `backups.volumes.s3.bucket` | `""` | Required when volume backups are enabled |
| `backups.volumes.s3.region` | `""` | Required when volume backups are enabled |
| `backups.volumes.s3.prefix` | `""` | Object key prefix |
| `backups.volumes.s3.endpoint` | `""` | Optional S3-compatible endpoint |
| `backups.volumes.s3.path_style` | `false` | Use path-style addressing for S3-compatible storage |
| `backups.volumes.s3.sse_algorithm` | `""` | Server-side encryption sent with each upload, such as `AES256` |
| `backups.volumes.s3.sse_kms_key_id` | `""` | KMS key ID for `aws:kms` encryption |
| `backups.volumes.s3.storage_class` | `""` | Storage class sent with each upload, such as `STANDARD_IA` (AWS) or `ONEZONE_IA` (Scaleway One Zone); empty uses the bucket default |
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
- Only volumes listed in a service's `backup.volume` are archived. Leave caches and rebuildable data (CI runner state, build caches) out of that list.
- Stored size is roughly `retention.keep` × the compressed size of each backed-up volume.
- Live volume archives are best-effort; consistency requires application quiesce, pause, or stop.
- Automated volume restore is not part of the MVP.
- Snapshots are not generic across Docker/Podman volumes and require backend-specific support such as ZFS, Btrfs, LVM, EBS, or a snapshot-capable volume driver.
- S3 credentials should come from the environment, shared config, instance role, or Gordon secret conventions. Use least-privilege IAM for the configured prefix.

## Related

- [App Manifest](./apps.md)
- [CLI Backup Command](../cli/backup.md)
- [Configuration Reference](./reference.md)
