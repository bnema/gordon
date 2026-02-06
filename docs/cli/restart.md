# Restart Command

Restart a running container for a route.

## gordon restart

### Synopsis

```bash
gordon restart <domain> [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The route domain to restart |

### Options

| Option | Description |
|--------|-------------|
| `--with-attachments` | Also restart attached services (databases, caches) |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

Restarts the container for the specified route. This is useful after updating
environment variables or secrets without performing a full redeploy.

When `--with-attachments` is set, Gordon also restarts any attachment containers
for the route.

### Examples

```bash
# Restart main container only
gordon restart myapp.example.com

# Restart with attachments
gordon restart myapp.example.com --with-attachments
```

### Notes

- Local mode works when run on the Gordon host. It uses the same deploy-signal path as `gordon deploy <domain>`.
- `--with-attachments` is only supported in remote mode.
- In remote mode, target your Gordon admin endpoint (for example `https://gordon.example.com`), not the registry host (for example `https://reg.example.com`).

## Related

- [CLI Overview](./index.md)
- [Secrets Command](./secrets.md)
