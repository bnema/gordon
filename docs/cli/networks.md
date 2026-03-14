# Networks Commands

Inspect Gordon-managed Docker networks.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon networks

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List Gordon-managed Docker networks |

---

## gordon networks list

Display Docker networks managed by Gordon, including which containers
are connected to each network.

```bash
gordon networks list
gordon networks list --json
gordon networks list --remote https://gordon.mydomain.com --token $TOKEN
```

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### JSON Output

```json
[
  {
	"name": "gordon_myapp",
	"driver": "bridge",
	"containers": ["container1", "container2"]
  }
]
```

## Related

- [CLI Overview](./index.md)
- [Routes Command](./routes.md)
- [Attachments Command](./attachments.md)
