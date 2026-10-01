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

Remove app-owned volumes that were explicitly released and are no longer used by any container.

```bash
gordon volumes prune
gordon volumes prune --dry-run
gordon volumes prune --no-confirm
```

| Flag | Description |
|------|-------------|
| `--dry-run` | Report the plan without deleting |
| `--no-confirm` | Skip confirmation prompt |
| `--json` | Output as JSON |

#### What survives

A volume is removed only when all of the following hold:

- A durable ownership record explicitly marks it `released`.
- The runtime labels agree with that record: the app, the app incarnation UUID, and the service.
- No container mounts it.

Everything else survives. Attached and retained volumes, volumes carrying only `gordon.managed`, volumes with no Gordon provenance, and volumes whose ownership cannot be established all survive. Ownership is never inferred from the volume name, and labels alone never make a volume deletable.

With current metadata no lifecycle marks a volume `released`, so a volume prune normally reports a zero-deletion success. The command still lists every candidate with its verdict and reason, and `--json` returns the same plan shape as `gordon images prune`.

## Related

- [Volumes Configuration](../config/volumes.md)
- [Images](./images.md)
