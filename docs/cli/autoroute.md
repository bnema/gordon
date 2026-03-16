# Auto-Route Commands

Manage the auto-route domain allowlist on local or remote Gordon instances.

When auto-route is enabled, Gordon handles image pushes in two ways:

1. **Image-name matching** — when a pushed image matches an existing route's configured image, Gordon auto-deploys that route. Since the route already exists, no allowlist check is needed.
2. **Label-based creation** — when a pushed image carries `gordon.domain` labels, Gordon creates new routes for those domains. The domain allowlist restricts which domains may be created this way, preventing untrusted images from registering arbitrary domains.

The allowlist gates only the creation of new routes from labels. Routes created manually with `gordon routes add` or already present in configuration are not subject to the allowlist.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon autoroute

Manage auto-route settings.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `allow` | Manage auto-route allowed domains |

---

## gordon autoroute allow

Manage auto-route allowed domains.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `add` | Add a domain pattern to the allowlist |
| `list` | List allowed domains |
| `remove` | Remove a domain pattern |

---

## gordon autoroute allow add

Add a domain pattern to the auto-route allowlist.

```bash
gordon autoroute allow add <pattern>
gordon autoroute allow add example.com
gordon autoroute allow add "*.staging.example.com"
gordon autoroute allow add example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<pattern>` | Domain pattern to allow for auto-route |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

Patterns must be lowercase and must not have trailing dots.
A bare `*` matches all domains. Wildcards in `*.domain.tld` form match exactly one subdomain level, so `*.example.com` matches `app.example.com` but not `api.app.example.com`.

### Examples

```bash
# Allow an exact domain
gordon autoroute allow add example.com

# Allow one subdomain level under staging.example.com
gordon autoroute allow add "*.staging.example.com"

# Remote (override)
gordon autoroute allow add example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```text
✓ Allowed domain added
```

---

## gordon autoroute allow list

List auto-route allowed domains.

```bash
gordon autoroute allow list
gordon autoroute allow list --json
gordon autoroute allow list --remote https://gordon.mydomain.com --token $TOKEN
```

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output allowed domains as JSON |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

Shows the current allowlist used to restrict which domains auto-route may claim.
When no patterns are configured, the command prints a friendly empty-state message instead of a table.

### Examples

```bash
# Local
gordon autoroute allow list

# JSON
gordon autoroute allow list --json

# Remote (override)
gordon autoroute allow list --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```text
example.com
*.staging.example.com
```

When the allowlist is empty:

```text
No allowed domains configured
```

### JSON Output

```bash
gordon autoroute allow list --json
```

```json
{
  "domains": ["example.com", "*.staging.example.com"]
}
```

---

## gordon autoroute allow remove

Remove a domain pattern from the auto-route allowlist.

```bash
gordon autoroute allow remove <pattern>
gordon autoroute allow remove example.com
gordon autoroute allow remove example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<pattern>` | Domain pattern to remove from the allowlist |

### Options

| Option | Description |
|--------|-------------|
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

Removes an exact allowlist entry.
Use the same pattern string you added, including the `*.` prefix for wildcard entries.

### Examples

```bash
# Remove an exact domain
gordon autoroute allow remove example.com

# Remove a wildcard domain
gordon autoroute allow remove "*.staging.example.com"

# Remote (override)
gordon autoroute allow remove example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```text
✓ Allowed domain removed
```

## Related

- [CLI Overview](./index.md)
- [Routes Command](./routes.md)
- [Config Command](./config.md)
