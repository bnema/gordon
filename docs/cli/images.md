# Images Command

List and prune runtime/registry images.

## gordon images

```bash
gordon images <subcommand>
```

Subcommands:

- `list` - List runtime images.
- `prune` - Prune dangling runtime images and optionally registry data.

> **Note:** Images commands require remote mode (`--remote` + `--token`, or configured remotes).

## gordon images list

```bash
gordon images list
```

Shows image rows with repository, tag, size, creation time, image id, and dangling status.

## gordon images prune

```bash
gordon images prune [--dry-run] [--keep <n>] [--runtime-only]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Show prune behavior without applying changes |
| `--keep` | `0` | Number of latest tags to keep per repository (`0` disables registry cleanup) |
| `--runtime-only` | `false` | Prune dangling runtime images only; forces registry cleanup off |

Behavior notes:

- `--runtime-only` forces `keep_last = 0`.
- `--keep` must be `>= 0`.
- `--dry-run` reports what would be pruned.

## Examples

```bash
# List images
gordon images list

# Prune dangling runtime images only
gordon images prune --runtime-only

# Prune runtime images and keep 3 newest tags per repository
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
