# Config Commands

Inspect Gordon server configuration.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon config

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `show` | Show server configuration |

---

## gordon config show

Display the full Gordon server configuration including server settings,
auto-route, network isolation, routes, and external routes.

```bash
gordon config show
gordon config show --json
gordon config show --remote https://gordon.mydomain.com --token $TOKEN
```

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### JSON Output

```json
{
  "server": {
    "port": 1111,
    "registry_port": 5000,
    "registry_domain": "reg.example.com",
    "data_dir": "/var/lib/gordon"
  },
  "auto_route": {
    "enabled": true
  },
  "network_isolation": {
    "enabled": true,
    "prefix": "gordon_"
  },
  "routes": [
    {"domain": "app.example.com", "image": "myapp:latest"}
  ],
  "external_routes": {
    "reg.example.com": "localhost:5000"
  }
}
```

## Related

- [CLI Overview](./index.md)
- [Status Command](./status.md)
- [Routes Command](./routes.md)
