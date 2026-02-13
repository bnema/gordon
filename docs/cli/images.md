# Images Command

List and prune runtime/registry images.

## gordon images

```bash
gordon images <subcommand>
```

Subcommands:

- `list` - List runtime images and registry tags.
- `prune` - Prune dangling runtime images and old registry tags.

> **Note:** Images commands require remote mode (`--remote` + `--token`, or configured remotes).

## gordon images list

```bash
gordon images list
```

Shows image rows with repository, tag, size, creation time, image id, and dangling status.
Rows that exist only in the registry (not currently present in the runtime cache) are included with unavailable runtime fields shown as `-`.

## gordon images prune

```bash
gordon images prune [--dry-run] [--keep-releases <n>] [--dangling] [--registry] [--no-confirm]
```

By default, prune removes dangling runtime images **and** applies registry tag retention (keeping `latest` + 3 previous non-`latest` tags per repository). A confirmation prompt is shown before destructive operations.

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Show prune behavior without applying changes |
| `--keep-releases` | `3` | Number of previous non-`latest` tags to keep per repository (`latest` is always preserved) |
| `--dangling` | `false` | Restrict scope to dangling runtime images only |
| `--registry` | `false` | Restrict scope to registry tag retention only |
| `--no-confirm` | `false` | Skip the confirmation prompt |

### Scope resolution

- **No scope flags** (default): both runtime and registry cleanup run.
- **`--dangling`**: only runtime dangling images are pruned; registry is skipped.
- **`--registry`**: only registry tag retention runs; runtime is skipped.
- **`--dangling --registry`**: both scopes run (same as default, but explicit).

### Retention semantics

- `latest` is always preserved when present.
- `--keep-releases` counts non-`latest` tags, ordered by most recent first.
- `--keep-releases=0` with registry scope enabled still runs registry cleanup but keeps no non-`latest` tags.

## Examples

```bash
# List images
gordon images list

# Prune everything with defaults (dangling + registry, keep latest + 3)
gordon images prune

# Prune dangling runtime images only
gordon images prune --dangling

# Prune registry tags only, keeping latest + 5 previous
gordon images prune --registry --keep-releases 5

# Preview cleanup without applying
gordon images prune --dry-run

# Skip confirmation prompt
gordon images prune --no-confirm
```

## Required Permissions

- `list` requires `admin:status:read`.
- `prune` requires `admin:config:write`.

## Related

- [CLI Commands](./index.md)
- [Images Configuration](../config/images.md)
