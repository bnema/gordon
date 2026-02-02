# Rollback Command

Roll back a route to a previous image tag.

## gordon rollback

### Synopsis

```bash
gordon rollback <domain> [options]
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

`gordon rollback` lists available tags in the Gordon registry for the route's
repository, then deploys the selected version. Semver tags are sorted in
descending order first, followed by non-semver tags.

If the selected tag matches the current running tag, no action is taken.

### Examples

```bash
# Interactive selection
gordon rollback myapp.example.com

# Roll back to a specific tag
gordon rollback myapp.example.com --tag v1.2.0
```

### Notes

- Remote mode required. See [CLI Overview](./index.md) for targeting options.
- Tags are read from the Gordon registry configured on the server.

## Related

- [CLI Overview](./index.md)
- [Push Command](./push.md)
- [Routes Command](./routes.md)
