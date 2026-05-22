# Routes Commands

Manage routes on local or remote Gordon instances.

Most remote-capable commands use client config or an active remote by default.
`gordon routes list` and `gordon routes status` are different: when neither
`--remote` nor `GORDON_REMOTE` is set, they show local routes first, then each
saved remote under its own heading. See [CLI Overview](./index.md).

## gordon routes

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List routes by domain and image |
| `status` | Show detailed route status |
| `show` | Show details for a single route |
| `add` | Create or update a route |
| `remove` | Remove a route |
| `deploy` | Deploy a specific route |

---

## gordon routes list

List routes by domain and image only.

When no target is selected, Gordon shows local routes first, then each saved
remote under its own heading. Use `--remote` or `GORDON_REMOTE` to show one
target only.

```bash
gordon routes list
gordon routes list --json
gordon routes list --remote https://gordon.mydomain.com --token $TOKEN
GORDON_REMOTE=prod gordon routes list
```

### Output

```text
Routes

Local
  app.example.com           myapp:latest
  api.example.com           myapi:v2.1.0

Remote: hetzner-vps
  gordon.example.com        gordon-webapp:latest

Remote: igor
  grafana.supri.xyz         grafana
  test.supri.xyz            hello-test
```

### JSON Output

```bash
gordon routes list --json
```

```json
[
  {
    "kind": "local",
    "name": "local",
    "routes": [
      {
        "domain": "app.example.com",
        "image": "myapp:latest"
      }
    ]
  },
  {
    "kind": "remote",
    "name": "igor",
    "url": "https://gordon.supri.xyz",
    "routes": [
      {
        "domain": "grafana.supri.xyz",
        "image": "grafana"
      }
    ]
  }
]
```

Single-target mode still returns a one-element array. Sections can also include
an `error` field when a target is unavailable.

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output routes as JSON |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

---

## gordon routes status

Show detailed route status for each target.

`routes status` uses the same target selection rules as `routes list`. When no
target is selected, Gordon shows local status first, then each saved remote
under its own heading.

```bash
gordon routes status
gordon routes status --json
gordon routes status --remote https://gordon.mydomain.com --token $TOKEN
GORDON_REMOTE=prod gordon routes status
```

### Output

```text
Route Status

Local
  <rich tree for local routes>

Remote: hetzner-vps
  <rich tree for that remote>

Remote: igor
  <rich tree for that remote>
```

The rich view keeps network grouping, container status, HTTP probe status, and
attachments within each target.

### JSON Output

```json
[
  {
    "kind": "local",
    "name": "local",
    "routes": [
      {
        "domain": "app.example.com",
        "image": "myapp:latest",
        "container_id": "abcd1234",
        "container_status": "running",
        "http_status": 200,
        "network": "gordon-shared",
        "attachments": [
          {
            "name": "postgres",
            "image": "postgres:18",
            "status": "running"
          }
        ]
      }
    ]
  }
]
```

Single-target mode still returns a one-element array. Sections can also include
an `error` field when a target is unavailable.

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output routes as JSON |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
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
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
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
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
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

## gordon routes diagnose

Diagnose route configuration, runtime state, preserved volumes, and orphaned attachment containers.

```bash
gordon routes diagnose <domain>
gordon routes diagnose myapp.example.com --json
```

Use this after route deletion or failed deployment to see whether runtime state still exists and which safe cleanup commands to run next.

### Output

```text
Route diagnosis: myapp.example.com
Image: myapp:latest
Container: running abc123def456
Preserved volume: gordon-myapp-example-com-data
Orphaned attachment: postgres
Hint: persistent volumes are preserved by default
Hint: run 'gordon attachments prune --stop' to stop orphaned attachment containers while preserving volumes
```

### JSON Output

```json
{
  "domain": "myapp.example.com",
  "configured": true,
  "route": {"domain": "myapp.example.com", "image": "myapp:latest", "https": true},
  "runtime": {"domain": "myapp.example.com", "container_id": "abc123def456", "container_status": "running"},
  "volumes": [{"name": "gordon-myapp-example-com-data", "in_use": false}],
  "orphaned_attachments": [{"container_id": "pg123", "name": "postgres", "image": "postgres:16", "status": "running", "owner": "myapp.example.com"}],
  "hints": ["persistent volumes are preserved by default"]
}
```

### Next Steps

```bash
gordon routes purge myapp.example.com          # dry-run retained resources
gordon routes purge myapp.example.com --attachments --force
```

---

## gordon routes purge

Review retained resources for a route and execute supported explicit cleanup actions only when forced.

```bash
gordon routes purge myapp.example.com              # dry-run
gordon routes purge myapp.example.com --attachments --force
gordon routes purge myapp.example.com --volumes    # report preserved volumes for manual review
```

### Dry-run Output

```text
Purge dry-run: myapp.example.com
Preserved attachments:
  postgres (running)
Purge candidate: route_container abc123def456
Preserved volume: gordon-myapp-example-com-data
Hint: dry-run only; add --force with explicit category flags to execute supported purge actions
```

### Forced Attachment Cleanup Output

```text
Purge completed: myapp.example.com
Removed containers:
  postgres
Hint: attachment volumes and data were preserved
```

Purge is intentionally conservative. Volumes are reported and preserved unless a dedicated volume deletion flow supports safe targeted deletion.

---

## gordon routes remove

Remove a route.

Route removal is safe by default. Gordon removes the route from configuration and reconciles the active route container so it is no longer served or restarted by Gordon's monitor. Stateful data is preserved. Volumes and attachment data are not deleted unless a separate explicit purge flow requests destructive cleanup.

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
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon routes remove myapp.example.com

# Remote (override)
gordon routes remove myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN

# Typical output
Route removed: myapp.example.com

Removed containers:
  gordon-myapp.example.com (abc123def456)

Preserved attachments:
  postgres (running)
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
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
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
