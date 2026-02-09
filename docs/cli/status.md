# Status Command

Show Gordon server status and container health.

## gordon status

Display server configuration and container status for all routes.

```bash
gordon status
gordon status --remote https://gordon.mydomain.com --token $TOKEN
```

`gordon status` works in local mode and remote mode.

- Local mode reads status from in-process services.
- Remote mode reads status from the target admin API.

### Output

```
Gordon Status

Domain: registry.example.com
Registry Port: 5000
Server Port: 8080
Routes: 3
Auto-Route: true
Network Isolation: false

Container Status:
  app.example.com: running
  api.example.com: running
  worker.example.com: stopped
```

### Information Displayed

| Field | Description |
|-------|-------------|
| Domain | Registry domain from configuration |
| Registry Port | Docker registry port |
| Server Port | Gordon admin API port |
| Routes | Total configured routes |
| Auto-Route | Whether auto-routing is enabled |
| Network Isolation | Whether network isolation is enabled |
| Container Status | Status of each route's container |

### Container States

| State | Description |
|-------|-------------|
| running | Container is running and healthy |
| stopped | Container was stopped |
| exited | Container exited (check logs for errors) |
| paused | Container is paused |
| unknown | Unable to determine container state |

## Flags

The status command uses global flags for remote access:

| Flag | Description |
|------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GORDON_REMOTE` | Remote Gordon URL |
| `GORDON_TOKEN` | Authentication token |

## Examples

### Check Local or Remote Status

```bash
# Local
gordon status

# Using flags
gordon status --remote https://gordon.mydomain.com --token $TOKEN

# Using environment variables
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=your-token
gordon status
```

### Quick Health Check

```bash
# Check if all containers are running
gordon status --remote https://gordon.mydomain.com --token $TOKEN | grep -E "(running|stopped|exited)"
```

## Required Permissions (Remote Only)

Remote status calls require `admin:status:read` scope in the authentication token.

```bash
# Generate token with required scope
gordon auth token generate --subject admin --scopes admin:status:read
```

## Related

- [Serve Command](./serve.md)
- [Routes Command](./routes.md)
- [Remote CLI Management](/wiki/guides/remote-cli.md)
