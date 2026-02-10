# Images Command

List and prune runtime/registry images.

## gordon images

```bash
gordon images <subcommand>
```

Subcommands:

- `list` - List runtime images and registry tags.
- `prune` - Prune dangling runtime images and optionally registry data.

> **Note:** Images commands require remote mode (`--remote` + `--token`, or configured remotes).

## gordon images list

```bash
gordon images list
```

Shows image rows with repository, tag, size, creation time, image id, and dangling status.
Rows that exist only in the registry (not currently present in the runtime cache) are included with unavailable runtime fields shown as `-`.

## gordon images prune

```bash
gordon images prune [--dry-run] [--keep <n>] [--runtime-only]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Show prune behavior without applying changes |
| `--keep` | `0` | Number of previous tags to keep per repository (`latest` is always preserved when present; `0` disables registry cleanup) |
| `--runtime-only` | `false` | Prune dangling runtime images only; forces registry cleanup off |

`--keep` defaults to `0` for ad-hoc CLI safety (registry cleanup off by default), while scheduled pruning uses `images.prune.keep_last` from config (default `3`). Use config for recurring behavior; when a CLI `--keep` value is provided, that request value is used.

Behavior notes:

- `--runtime-only` forces `keep_last = 0`.
- `--keep` must be `>= 0`.
- `--dry-run` reports what would be pruned.
- When `--keep` is greater than `0`, registry cleanup keeps `latest` (if present) plus the requested number of most-recent non-`latest` tags.

## Examples

```bash
# List images
gordon images list

# Prune dangling runtime images only
gordon images prune --runtime-only

# Prune runtime images and keep latest + 3 previous tags per repository
gordon images prune --keep 3

# Preview cleanup without applying
gordon images prune --dry-run --keep 5
```

## Required Permissions

- `list` requires `admin:status:read`.
- `prune` requires `admin:config:write`.

## Related

- [CLI Commands](./index.md)
- [Images Configuration](../config/images.md)
