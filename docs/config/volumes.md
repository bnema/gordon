# Volumes Configuration

Persistent app storage is declared in each app manifest. The installation-level `[volumes]` settings apply to non-app volume management and do not control declarative app storage.

## Declarative app volumes

Services declare persistent mounts in the app manifest:

```toml
[[service.volume]]
name = "database-data"
path = "/var/lib/postgresql/data"
```

A volume name is unique within its service. Gordon creates an incarnation-owned runtime volume, records the app, app UUID, service, and logical volume ownership, and reuses it across replacement and restart. App manifests do not support bind mounts or sharing one volume between services.

Every Dockerfile `VOLUME` path must have a matching `[[service.volume]]` declaration. Deployment fails closed when an image declares an unmanaged volume.

Runtime volume names are implementation details. Use `gordon volumes list` and ownership labels to inspect them; do not derive ownership from a name, rename volumes, or edit Gordon's ownership records.

## Retention

Ordinary app operations retain data:

- deploy and restart reuse the service's volumes;
- removing a service retains its volumes;
- `gordon apps remove` retains volumes under the removed app's internal UUID;
- a new app that reuses the public name does not adopt retained volumes.

Gordon does not automatically delete retained app volumes. Back up persistent data before any manual deletion, and use database-native backup and restore procedures for databases.

## Pruning

Use Gordon's ownership-aware command:

```bash
gordon volumes prune --dry-run
gordon volumes prune
```

A volume is eligible only when all of these requirements hold:

1. Gordon has a durable ownership record marking it `released`.
2. Runtime labels agree with the record's app, app incarnation UUID, and service.
3. No container mounts the volume.

Retained, attached, unknown, contradictory, and unrelated volumes survive. A `gordon.managed=true` label or a matching name is not sufficient deletion authority. With current lifecycle metadata, no operation marks app volumes `released`, so prune normally succeeds with no deletions.

> **Warning:** Do not use `docker volume prune`, `podman volume prune`, or equivalent runtime cleanup for Gordon data. Those commands bypass Gordon's ownership and retention checks and can delete an unmounted retained volume.

See the [Volumes CLI reference](../cli/volumes.md) for flags and plan output.

## Related

- [App Manifest](./apps.md)
- [Volumes CLI](../cli/volumes.md)
- [Configuration Overview](./index.md)
