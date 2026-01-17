# Attachments Commands

Manage attachments on local or remote Gordon instances.

## Requirements

Attachments require `network_isolation.enabled = true` in your configuration. Without network isolation, containers use Docker's default bridge network which does not provide DNS resolution - your app won't be able to reach attachments by hostname (e.g., `postgres:5432`).

The CLI will warn you if you try to add an attachment without network isolation enabled.

## gordon attachments

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all attachments or attachments for a specific target |
| `add` | Add an attachment to a domain or network group |
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

# List attachments for a specific domain or group
gordon attachments list app.example.com
gordon attachments list backend

# Remote mode
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
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Add database to a domain
gordon attachments add app.example.com postgres:18

# Add cache to a network group (shared by all domains in the group)
gordon attachments add backend redis:7-alpine

# Remote
gordon attachments add app.example.com postgres:18 --remote https://gordon.mydomain.com --token $TOKEN
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
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Examples

```bash
# Remove database from a domain
gordon attachments remove app.example.com postgres:18

# Remove from network group
gordon attachments remove backend redis:7-alpine

# Remote
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
