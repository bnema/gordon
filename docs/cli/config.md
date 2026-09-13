# Config Commands

Inspect Gordon server configuration.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon config

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `show` | Show server configuration |
| `validate` | Statically validate a local configuration file |

---

## gordon config show

Display the Gordon installation configuration including server settings,
network isolation, volumes, and external route domains. App routes live
under `gordon apps show`, never here. Sensitive filesystem paths and upstream external route targets are redacted by default.

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
  "network_isolation": {
    "enabled": true,
    "prefix": "gordon"
  },
  "volumes": {
    "auto_create": true,
    "prefix": "gordon",
    "preserve": true
  },
  "external_routes": [
    {"domain": "reg.example.com"}
  ]
}
```

External route targets and `server.data_dir` are intentionally omitted from the default admin config response because they reveal internal network and filesystem layout.

---

## gordon config validate

Statically validates a candidate configuration file before it is installed.
This command is local-only: `--remote` is rejected. Validation is static —
runtime, ACTIVE-state, secret, pull, and listener checks are not performed.
A file that fails validation exits non-zero; `--json` is still written first.

```bash
gordon config validate --file ./gordon.toml
gordon config validate --file ./gordon.toml --json
```

### Flags

| Flag | Description |
|------|-------------|
| `--file` | Local candidate configuration file (required) |
| `--json` | Output as JSON |

### JSON Output

On success:

```json
{
  "valid": true,
  "diagnostics": [],
  "scope": "static"
}
```

On failure, `valid` is `false` and `diagnostics` carries the failure:

```json
{
  "valid": false,
  "diagnostics": [
    {"code": "config-invalid", "key": "", "message": "configuration failed static validation"}
  ],
  "scope": "static"
}
```

## Related

- [CLI Overview](./index.md)
- [Status Command](./status.md)
- [Apps Commands](./apps.md)
