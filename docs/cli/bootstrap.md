# Bootstrap Command

Create or update a route, then push and deploy an image in one command.

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
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

`gordon bootstrap` is the recommended first-deploy workflow.

- It creates the route when it does not exist.
- It updates the route when it already exists.
- It can attach services and set environment variables as part of the same command.
- It then pushes and deploys the image for the target route.

Unlike `gordon push`, `gordon bootstrap` does not require the route to exist first.

### Examples

```bash
# First deploy
gordon bootstrap app.example.com myapp:latest

# First deploy with a database attachment and environment variable
gordon bootstrap app.example.com myapp:latest --attachment postgres:18 --env APP_ENV=production

# Multiple attachments and environment variables
gordon bootstrap app.example.com myapp:latest \
  --attachment postgres:18 \
  --attachment redis:7-alpine \
  --env APP_ENV=production \
  --env LOG_LEVEL=info

# Remote target
gordon bootstrap app.example.com myapp:latest --remote https://gordon.mydomain.com --token $TOKEN
```

### Notes

- `gordon bootstrap` is idempotent for route configuration: rerunning it updates the route instead of failing.
- Use `gordon push` for later image-only deploys when the route already exists.

## Related

- [CLI Overview](./index.md)
- [Routes Commands](./routes.md)
- [Push Command](./push.md)
