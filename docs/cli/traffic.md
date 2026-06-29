# gordon traffic

Inspect Gordon traffic plane status.

## Subcommands

| Command | Description |
|---------|-------------|
| `gordon traffic status` | Show traffic entrypoints, counters, and reload status |

## Usage

```bash
gordon traffic status --remote prod
gordon traffic status --remote https://gordon.example.com --json
```

## Flags

| Flag | Description |
|------|-------------|
| `--json` | Output machine-readable JSON |
| `--remote`, `-r` | Remote Gordon instance or saved remote name |

## JSON Output

```json
{
  "last_reload_status": "ok",
  "entrypoints": [
    {
      "name": "postgres",
      "address": "0.0.0.0:5432",
      "protocol": "tcp",
      "active": true,
      "active_tcp_connections": 1,
      "active_udp_sessions": 0,
      "total_accepted": 12,
      "total_refused": 0,
      "total_errors": 0,
      "bytes_in": 4096,
      "bytes_out": 8192
    }
  ],
  "routers": [],
  "services": [],
  "counters": {
    "active_tcp_connections": 1,
    "active_udp_sessions": 0,
    "total_accepted": 12,
    "total_refused": 0,
    "total_errors": 0,
    "bytes_in": 4096,
    "bytes_out": 8192
  }
}
```

## Related

- [Traffic configuration](../config/traffic.md)
- [Server status](./status.md)
