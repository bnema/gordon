# Network Isolation

Installation network policy for Gordon-managed networks.

## Configuration

```toml
[network_isolation]
enabled = true
network_prefix = "gordon"
internal = false
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Enable Gordon-managed network policy |
| `network_prefix` | string | `"gordon"` | Prefix filter for `gordon networks list` |
| `internal` | bool | `false` | Create isolated Docker networks with Docker's `Internal` flag, blocking direct external egress from containers on those networks. |

Per-app isolation is declared in app files, not here: each app gets a private network automatically, and services can join named shared networks with `[[network.shared]]` (see [App Manifest](./apps.md)). Deploy adds AND removes memberships without disconnecting unrelated services. Shared networks are created/reused only within verified Gordon ownership.

## Inspecting Networks

View Gordon-managed networks:

```bash
gordon networks list
docker network ls | grep gordon
```

Inspect a network:

```bash
docker network inspect <network-name>
```

## Related

- [App Manifest](./apps.md)
- [Configuration Overview](./index.md)
