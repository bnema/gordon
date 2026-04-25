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

Display the Gordon server configuration including server settings,
auto-route, network isolation, routes, and external route domains. Sensitive filesystem paths and upstream external route targets are redacted by default.

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
    "registry_domain": "reg.example.com"
  },
  "auto_route": {
    "enabled": true,
    "allowed_domains": ["example.com", "*.staging.example.com"]
  },
  "network_isolation": {
    "enabled": true,
    "prefix": "gordon_"
  },
  "routes": [
    {"domain": "app.example.com", "image": "myapp:latest"}
  ],
  "external_routes": [
    {"domain": "reg.example.com"}
  ]
}
```

External route targets and `server.data_dir` are intentionally omitted from the default admin config response because they reveal internal network and filesystem layout.

### Auto-Route Allowed Domains

The `auto_route.allowed_domains` field lists domain patterns that auto-route may assign to containers. Manage this list with [`gordon autoroute allow`](./autoroute.md).

## Related

- [CLI Overview](./index.md)
- [Auto-Route Commands](./autoroute.md)
- [Status Command](./status.md)
- [Routes Command](./routes.md)
