# Attachments Commands

Manage attachments on local or remote Gordon instances.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## Requirements

Attachments require `network_isolation.enabled = true` in your configuration (enabled by default). Without network isolation, containers use Docker's default bridge network which does not provide DNS resolution - your app won't be able to reach attachments by hostname (e.g., `postgres:5432`).

The CLI will warn you if you try to add an attachment without network isolation enabled.

## gordon attachments

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all attachments or attachments for a specific target |
| `add` | Add an attachment to a domain or network group |
| `push` | Build or push attachment images to the Gordon registry |
| `remove` | Remove an attachment from a domain or network group |

### Alias

`gordon attach` is an alias for `gordon attachments`:

```bash
gordon attach list
gordon attach add app.example.com postgres:18
gordon attach remove app.example.com postgres:18
```

---

## gordon attachments list

List all configured attachments.

```bash
# List all attachments
gordon attachments list
gordon attachments list --json

# List attachments for a specific domain or group
gordon attachments list app.example.com
gordon attachments list backend

# Remote (override)
gordon attachments list --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```
Target                    Attachments
--------------------------------------------------------------------------------
app.example.com           postgres:18, redis:7-alpine
api.example.com           postgres:18
backend (group)           rabbitmq:3-management
```

### JSON Output

```bash
gordon attachments list --json
```

```json
[
  {
    "target": "app.example.com",
    "attachments": ["postgres:18", "redis:7-alpine"]
  },
  {
    "target": "backend",
    "attachments": ["rabbitmq:3-management"]
  }
]
```

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output attachments as JSON |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

---

## gordon attachments add

Add an attachment to a domain or network group.

```bash
gordon attachments add <domain-or-group> <image>
gordon attachments add app.example.com postgres:18
gordon attachments add backend redis:7-alpine
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain-or-group>` | The domain name or network group name |
| `<image>` | The container image to attach |

### Options

| Option | Description |
|--------|-------------|
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Examples

```bash
# Add database to a domain
gordon attachments add app.example.com postgres:18

# Add cache to a network group (shared by all domains in the group)
gordon attachments add backend redis:7-alpine

# Remote (override)
gordon attachments add app.example.com postgres:18 --remote https://gordon.mydomain.com --token $TOKEN
```

---

## gordon attachments push

Push attachment images to the Gordon registry.

```bash
gordon attachments push <image> [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<image>` | The attachment image to build/tag and push |

### Options

| Option | Description |
|--------|-------------|
| `--build` | Build the image first using `docker buildx` |
| `-f, --file` | Path to Dockerfile (default: `./Dockerfile`, used with `--build`) |
| `--platform` | Target platform for buildx (default: `linux/amd64`) |
| `--build-arg` | Additional build args (repeatable, `KEY=VALUE`) |
| `--tag` | Override version tag |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Description

`gordon attachments push` pushes attachment images such as databases and caches to the Gordon registry so they are available when routes deploy. It does not trigger deployment. The image must already be configured as an attachment first.

It uses the same native chunked upload transport as `gordon push`, sending image
layers in 50MB chunks so pushes work through Cloudflare-proxied Gordon
instances. Keep the server's `max_blob_chunk_size` larger than the client chunk
size; the default `95MB` is compatible.

### Examples

```bash
# Push a pre-built attachment image
gordon attachments push pitlane-pgsql

# Build and push an attachment image
gordon attachments push pitlane-pgsql --build

# Push with a specific tag
gordon attachments push pitlane-pgsql --tag v18
```

---

## gordon attachments remove

Remove an attachment.

```bash
gordon attachments remove <domain-or-group> <image>
gordon attachments remove app.example.com postgres:18
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain-or-group>` | The domain name or network group name |
| `<image>` | The container image to remove |

### Options

| Option | Description |
|--------|-------------|
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Examples

```bash
# Remove database from a domain
gordon attachments remove app.example.com postgres:18

# Remove from network group
gordon attachments remove backend redis:7-alpine

# Remote (override)
gordon attachments remove app.example.com postgres:18 --remote https://gordon.mydomain.com --token $TOKEN
```

---

## Workflow Examples

### Add Database and Cache

```bash
# Add PostgreSQL database
gordon attachments add app.example.com postgres:18

# Add Redis cache
gordon attachments add app.example.com redis:7-alpine

# Verify
gordon attachments list app.example.com
```

### Shared Services via Network Groups

```bash
# First, ensure you have a network group defined in your config:
# [network_groups]
# "backend" = ["app.example.com", "api.example.com"]

# Add shared cache to the group
gordon attachments add backend redis:7-alpine

# Both app.example.com and api.example.com can now access the shared Redis
```

### First Deploy with Custom Attachment Image

```bash
# Configure the route and attachment
gordon bootstrap app.example.com myapp:latest --attachment pitlane-pgsql

# Push the custom attachment image first
gordon attachments push pitlane-pgsql --build

# Then push and deploy the route image
gordon push myapp:latest --domain app.example.com --build --no-confirm
```

### CI/CD Integration

```bash
# In your CI/CD pipeline
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=$GORDON_TOKEN

# Add new attachment
gordon attachments add app.example.com elasticsearch:8

# Trigger redeploy to pick up the new attachment
gordon routes deploy app.example.com
```

## Related

- [CLI Overview](./index.md)
- [Attachments Configuration](../config/attachments.md)
- [Network Groups](../config/network-groups.md)
