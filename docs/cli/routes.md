# Routes Commands

Manage routes on local or remote Gordon instances.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon routes

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all routes |
| `show` | Show details for a single route |
| `add` | Create or update a route |
| `remove` | Remove a route |
| `deploy` | Deploy a specific route |

---

## gordon routes list

List all configured routes.

```bash
gordon routes list
gordon routes list --json
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

### JSON Output

```bash
gordon routes list --json
```

```json
[
  {
    "domain": "app.example.com",
    "image": "myapp:latest",
    "status": "running"
  },
  {
    "domain": "api.example.com",
    "image": "myapi:v2.1.0",
    "status": "running"
  }
]
```

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output routes as JSON |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

---

## gordon routes add

Create a new route or update an existing route.

```bash
gordon routes add <domain> <image>
gordon routes add myapp.example.com myapp:latest
```

If the route already exists, Gordon updates it to the new image instead of failing.
The image does not need to be pushed to the Gordon registry before you add the route.

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

# Update an existing route
gordon routes add myapp.example.com myapp:v2

# Remote (override)
gordon routes add myapp.example.com myapp:latest --remote https://gordon.mydomain.com --token $TOKEN
```

### Notes

- `gordon routes add` is idempotent: it creates the route when missing and updates it when present.
- You can add the route before the image is pushed. Deploy happens when the image is later available or when you deploy an available image.

---

## gordon routes show

Show detailed information about a single route.

```bash
gordon routes show <domain>
gordon routes show myapp.example.com
gordon routes show myapp.example.com --json
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name of the route to inspect |

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output route details as JSON |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

Displays the configured image for the route plus any available container and HTTP health information.
In local-only mode, health data may be unavailable.

### Examples

```bash
# Local
gordon routes show myapp.example.com

# JSON
gordon routes show myapp.example.com --json

# Remote (override)
gordon routes show myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### JSON Output

```json
{
  "domain": "myapp.example.com",
  "image": "myapp:latest",
  "container_status": "running",
  "http_status": 200
}
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
