# Routes Commands

Manage routes on local or remote Gordon instances.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon routes

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all routes |
| `add` | Add a new route |
| `remove` | Remove a route |
| `deploy` | Deploy a specific route |

---

## gordon routes list

List all configured routes.

```bash
gordon routes list
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```
Domain                    Image                     Status
--------------------------------------------------------------------------------
app.example.com           myapp:latest              running
api.example.com           myapi:v2.1.0              running
admin.example.com         admin-panel:latest        stopped
```

---

## gordon routes add

Add a new route.

```bash
gordon routes add <domain> <image>
gordon routes add myapp.example.com myapp:latest
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name for the route |
| `<image>` | The container image to deploy |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon routes add myapp.example.com myapp:latest
gordon routes add api.example.com myapi:v2.1.0

# Remote (override)
gordon routes add myapp.example.com myapp:latest --remote https://gordon.mydomain.com --token $TOKEN
```

---

## gordon routes remove

Remove a route.

```bash
gordon routes remove <domain>
gordon routes remove myapp.example.com
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name of the route to remove |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon routes remove myapp.example.com

# Remote (override)
gordon routes remove myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
```

---

## gordon routes deploy

Deploy or redeploy a specific route.

```bash
gordon routes deploy <domain>
gordon routes deploy myapp.example.com
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name of the route to deploy |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

Triggers a fresh image pull and container redeployment for the specified route.

### Examples

```bash
# Local
gordon routes deploy myapp.example.com

# Remote (override)
gordon routes deploy myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
```

## Related

- [CLI Overview](./index.md)
- [Routes Configuration](../config/routes.md)
