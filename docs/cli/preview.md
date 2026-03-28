# gordon preview

Create and manage ephemeral preview environments. Each preview gets its own subdomain, container, and optionally cloned volumes from the base route.

## Commands

### `gordon preview create`

Build, push, and deploy a preview environment.

```bash
gordon preview create myapp
gordon preview create myapp --ttl 72h
gordon preview create myapp --no-build --no-data
```

| Flag | Description |
|------|-------------|
| `--ttl` | Override TTL duration (e.g., `72h`) |
| `--no-build` | Skip image build (use existing image) |
| `--no-data` | Skip volume cloning from base route |
| `--platform` | Target platform for build (default `linux/amd64`) |

### `gordon preview list`

List active preview environments with status and remaining TTL.

```bash
gordon preview list
gordon preview list --json
```

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### `gordon preview delete`

Tear down a preview environment and clean up its resources.

```bash
gordon preview delete myapp-feature-x
```

### `gordon preview extend`

Extend the TTL of an active preview.

```bash
gordon preview extend myapp-feature-x
gordon preview extend myapp-feature-x --ttl 48h
```

| Flag | Description |
|------|-------------|
| `--ttl` | Additional TTL to add (default `24h`) |

## Related

- [Preview Configuration](../config/preview.md)
- [Deploy](./serve.md#gordon-deploy)
