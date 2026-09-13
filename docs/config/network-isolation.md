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

## Service-to-Service Networking

Every app container joins an incarnation-owned private network. Services of the same app communicate over that network and can resolve each other by service alias.

Different apps are isolated by default: a container on one app network cannot reach another app's network. Cross-app communication happens only when both services declare the same `[[network.shared]]` membership, which attaches the explicitly enrolled containers to a shared network.

Readiness helpers never join shared networks and exist only on the target app's private network.

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
