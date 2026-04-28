# Pin Command

Pin a route to a specific image tag.

## gordon pin

### Synopsis

```bash
gordon pin <domain> [options]
gordon pin list <domain>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The route domain to pin |

### Options

| Option | Description |
|--------|-------------|
| `--tag` | Target tag (skips interactive selection) |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |
| `--json` | Output as JSON (for `pin list`) |

### Description

`gordon pin` deploys a selected image tag for the given route. Tags are
listed from the Gordon registry, with semver tags sorted in descending
order first, followed by non-semver tags.

Use cases:
- **Rollback** — revert to a previous stable version
- **Forward** — advance to a newer version
- **Lock** — pin to a specific version instead of following `latest`
- **Test** — switch to an experimental tag

`gordon pin list <domain>` lists available tags without deploying and
marks the current tag.

If the selected tag matches the current running tag, no action is taken.

### Examples

```bash
# Interactive selection
gordon pin myapp.example.com

# Pin to a specific tag
gordon pin myapp.example.com --tag v1.2.0

# List available tags for a domain
gordon pin list myapp.example.com

# List available tags as JSON
gordon pin list myapp.example.com --json
```

### Notes

- Local by default; remote mode optional. See [CLI Overview](./index.md) for targeting options.
- Tags are read from the Gordon registry configured on the server.

## Related

- [CLI Overview](./index.md)
- [Push Command](./push.md)
- [Routes Command](./routes.md)
- [Deployment Rollback Strategies](../deployment/rollback.md)
