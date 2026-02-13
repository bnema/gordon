# Images Configuration

Configure automatic image cleanup for Docker runtime images and local registry storage.

## Overview

When enabled, Gordon runs a scheduled image prune job that:

- Prunes dangling runtime images.
- Applies tag retention to registry repositories.
- Preserves the `latest` tag.
- Removes unreferenced blobs after tag cleanup.

The CLI `gordon images prune` uses the same defaults as the scheduled job (keep `latest` + 3 previous tags, both scopes enabled: dangling runtime images and registry tag retention).

## Configuration

```toml
[images.prune]
enabled = false
schedule = "daily"
keep_last = 3
```

## Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `images.prune.enabled` | bool | `false` | Enables scheduled image cleanup |
| `images.prune.schedule` | string | `"daily"` | Schedule preset: `hourly`, `daily`, `weekly`, `monthly` |
| `images.prune.keep_last` | int | `3` | Number of newest non-`latest` tags kept per repository during registry cleanup (`latest` is always kept when present) |

## Retention Behavior

- `latest` is always preserved.
- `keep_last` applies per repository and counts non-`latest` tags.
- `keep_last = 0` skips registry tag/blob cleanup (runtime dangling prune still runs).
- Negative `keep_last` values are invalid.

## Related

- [CLI Images Command](../cli/images.md)
- [Configuration Reference](./reference.md)
