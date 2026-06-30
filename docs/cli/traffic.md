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
      "bytes_out": 8192,
      "smart_tcp": {
        "http_accepted": 0,
        "h2c_accepted": 0,
        "https_fallback_accepted": 0,
        "tls_passthrough_accepted": 0,
        "raw_fallback_accepted": 0,
        "entrypoint_cidr_refused": 0,
        "raw_fallback_cidr_refused": 0,
        "proxy_refused": 0,
        "unknown_no_fallback_refused": 0,
        "malformed_rejected": 0,
        "sniff_timeout": 0,
        "client_hello_too_large": 0
      }
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
    "bytes_out": 8192,
    "smart_tcp": {
      "http_accepted": 0,
      "h2c_accepted": 0,
      "https_fallback_accepted": 0,
      "tls_passthrough_accepted": 0,
      "raw_fallback_accepted": 0,
      "entrypoint_cidr_refused": 0,
      "raw_fallback_cidr_refused": 0,
      "proxy_refused": 0,
      "unknown_no_fallback_refused": 0,
      "malformed_rejected": 0,
      "sniff_timeout": 0,
      "client_hello_too_large": 0
    }
  }
}
```

## Related

- [Traffic configuration](../config/traffic.md)
- [Server status](./status.md)
