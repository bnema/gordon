# Status Command

Show Gordon server status and the app fleet summary.

## gordon status

Display installation identity plus one status line per app (from desired/active state, no container inspection). Per-service detail lives under `gordon apps show APP` and `gordon apps status APP`.

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

Gordon Domain: gordon.example.com
Registry Port: 5000
Server Port: 8088
Apps: 3
Network Isolation: false

Container Status:
  blog: active
  shop: deploying
  old-site: stopped
```

### Information Displayed

| Field | Description |
|-------|-------------|
| Gordon Domain | Public Gordon domain from configuration |
| Registry Port | Docker registry port |
| Server Port | Gordon admin port |
| Routes | Total apps in desired/active state |
| Network Isolation | Whether installation network policy is enabled |
| Container Status | Fleet status per app (see states below) |

### App States

| State | Description |
|-------|-------------|
| active | App deployed and converged on desired state |
| deploying | Desired state diverges from effective state |
| pending | App applied but never deployed |
| stopped | Durable stopped intent (stays stopped across reboot) |

## Flags

The status command uses global flags for remote access:

| Flag | Description |
|------|-------------|
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GORDON_REMOTE` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
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

### Quick Fleet Check

```bash
# Check for non-converged apps
gordon status --remote https://gordon.mydomain.com --token $TOKEN | grep -E "(deploying|pending|stopped)"
```

## Required Permissions (Remote Only)

Remote status calls require `admin:status:read` scope in the authentication token.

```bash
# Generate token with required scope
gordon auth token generate --subject admin --scopes admin:status:read
```

## Related

- [Serve Command](./serve.md)
- [Apps Commands](./apps.md)
- [CLI Overview](./index.md)
- [Remote CLI Management](/wiki/guides/remote-cli.md)
