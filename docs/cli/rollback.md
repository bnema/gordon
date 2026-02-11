# Rollback Command

Roll back a route to a previous image tag.

## gordon rollback

### Synopsis

```bash
gordon rollback <domain> [options]
gordon rollback list <domain>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The route domain to roll back |

### Options

| Option | Description |
|--------|-------------|
| `--tag` | Target tag (skips interactive selection) |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

`gordon rollback` deploys a selected tag for the route's repository.
Semver tags are sorted in descending order first, followed by non-semver tags.

`gordon rollback list <domain>` only lists available tags and marks the current
tag without deploying.

If the selected tag matches the current running tag, no action is taken.

### Examples

```bash
# Interactive selection
gordon rollback myapp.example.com

# Roll back to a specific tag
gordon rollback myapp.example.com --tag v1.2.0

# List available tags for a domain
gordon rollback list myapp.example.com
```

### Notes

- Remote mode required. See [CLI Overview](./index.md) for targeting options.
- Tags are read from the Gordon registry configured on the server.

## Related

- [CLI Overview](./index.md)
- [Push Command](./push.md)
- [Routes Command](./routes.md)
