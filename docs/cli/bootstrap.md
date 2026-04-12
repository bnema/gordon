# Bootstrap Command

Create or update route configuration, attachments, and secrets in one command.

## gordon bootstrap

### Synopsis

```bash
gordon bootstrap <domain> <image> [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The route domain to create or update |
| `<image>` | The image to assign to the route |

### Options

| Option | Description |
|--------|-------------|
| `--attachment` | Add an attachment to the route (repeatable) |
| `--env` | Set an environment variable for the route (repeatable, `KEY=VALUE`) |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Description

`gordon bootstrap` is the recommended first-step setup workflow.

- Creates the route when it does not exist.
- Updates the route when it already exists.
- Can attach services and set environment variables as part of the same command.
- Does not push or deploy the image.

Unlike `gordon push`, `gordon bootstrap` does not require the route to exist first. Run `gordon push` separately after bootstrap to upload and deploy an image.

### Examples

```bash
# First-time route setup
gordon bootstrap app.example.com myapp:latest

# Then push and deploy the image
gordon push --domain app.example.com --build --no-confirm

# First-time route setup with a database attachment and environment variable
gordon bootstrap app.example.com myapp:latest --attachment postgres:18 --env APP_ENV=production

# Multiple attachments and environment variables
gordon bootstrap app.example.com myapp:latest \
  --attachment postgres:18 \
  --attachment redis:7-alpine \
  --env APP_ENV=production \
  --env LOG_LEVEL=info

# Remote target
gordon bootstrap app.example.com myapp:latest --remote https://gordon.mydomain.com --token $TOKEN

# Push custom attachment image first
gordon attachments push pitlane-pgsql --build

# Then push and deploy the route image
gordon push myapp --build --no-confirm
```

### Notes

- `gordon bootstrap` is idempotent for route configuration: rerunning it re-applies config instead of failing.
- Run `gordon push` after bootstrap to upload and deploy the image.
- If attachments use custom images that are not available from a public registry, push those attachment images with `gordon attachments push` before deploying the route image.

## Related

- [CLI Overview](./index.md)
- [Attachments Commands](./attachments.md)
- [Routes Commands](./routes.md)
- [Push Command](./push.md)
