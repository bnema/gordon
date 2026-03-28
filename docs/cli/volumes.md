# gordon volumes

List and clean up Docker volumes managed by Gordon.

## Commands

### `gordon volumes list`

List all Gordon-managed volumes.

```bash
gordon volumes list
gordon volumes list --json
```

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### `gordon volumes prune`

Remove orphaned volumes no longer associated with a running container.

```bash
gordon volumes prune
gordon volumes prune --dry-run
gordon volumes prune --no-confirm
```

| Flag | Description |
|------|-------------|
| `--dry-run` | Show what would be removed without deleting |
| `--no-confirm` | Skip confirmation prompt |
| `--json` | Output as JSON |

## Related

- [Volumes Configuration](../config/volumes.md)
- [Images](./images.md)
