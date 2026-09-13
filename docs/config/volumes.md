# Volumes Configuration

Persistent app storage is declared in each app manifest. The installation-level `[volumes]` settings apply to non-app volume management and do not control declarative app storage.

## Declarative app volumes

Services declare persistent mounts in the app manifest:

```toml
[[service.volume]]
name = "database-data"
path = "/var/lib/postgresql/data"
```

A volume name is unique within its service. Gordon creates an incarnation-owned runtime volume, records the app, app UUID, service, and logical volume ownership, and reuses it across replacement and restart. Manifests never carry host paths, and sharing one volume between services is not supported. When an app must read an operator-approved host location, use a named administrative bind instead.

## Administrative bind mounts

Manifests cannot name host paths directly. The operator declares a named policy in `gordon.toml`; the manifest only references the policy name:

```toml
# gordon.toml
[app_mounts.app-logs]
source = "/srv/gordon/host-logs"   # required; the only host path this mount may come from
read_only = true
allowed_apps = ["metrics-agent"]   # required; exact, non-empty
allowed_services = ["web"]         # required; exact, non-empty
# root = "/srv/gordon"             # optional administrative boundary
```

```toml
# app manifest
[[service.bind]]
name = "app-logs"        # must match [app_mounts.<name>]
path = "/var/lib/collector/host-logs"   # container destination
readonly = true
```

- `source` must be absolute, normalized, and resolve through symlinks to a regular file or directory that stays under `root`. When `root` is omitted, the source's parent directory is the boundary, so a symlinked source may resolve within it but never escape it.
- `allowed_apps` and `allowed_services` are exact, non-empty allowlists; there is no wildcard, prefix, or empty-means-all form.
- Read-only precedence: `read_only` on the policy or `readonly` on the bind forces the resolved mount read-only. A bind never weakens its policy.
- Gordon re-resolves the policy immediately before every container create, restart, and recovery. Removing a policy or an allowlist entry blocks future deploys; running containers keep their current mounts until redeployed. `gordon serve` reload republishes validated policies atomically, and a failing edit keeps the previous policies live.
- Gordon never creates, copies, deletes, chowns, backs up, or prunes the host path; ownership and permissions stay with the operator.

A read-only log collector is the typical use. Its own state is a named volume; the host logs are exposed through a bind:

```toml
name = "metrics-agent"

[[service]]
name = "collector"
image = "registry.example.com/metrics/collector:2.1.0"

[[service.volume]]
name = "collector-state"
path = "/var/lib/collector"

[[service.bind]]
name = "app-logs"
path = "/var/lib/collector/host-logs"
readonly = true
```

Collection is one-way: the collector reads operator-owned logs and cannot write back through the bind.

Every Dockerfile `VOLUME` path must have a matching `[[service.volume]]` declaration. Deployment fails closed when an image declares an unmanaged volume.

Runtime volume names are implementation details. Use `gordon volumes list` and ownership labels to inspect them; do not derive ownership from a name, rename volumes, or edit Gordon's ownership records.

## Retention

Ordinary app operations retain data:

- deploy and restart reuse the service's volumes;
- removing a service retains its volumes;
- `gordon apps remove` retains volumes under the removed app's internal UUID;
- a new app that reuses the public name does not adopt retained volumes;
- administrative binds are unaffected by app operations: their host paths are never created, adopted, or retained by Gordon.

Gordon does not automatically delete retained app volumes. Back up persistent data before any manual deletion, and use database-native backup and restore procedures for databases.

## Manual migration

Gordon does not copy data between volumes. Stop every container that can write to the source, verify a backup, create an empty target volume, and perform the copy through the runtime so rootless UID/GID mappings are preserved. For Podman, a disposable helper container is the preferred generic method:

```bash
podman run --rm \
  --volume <source-volume>:/source:ro \
  --volume <target-volume>:/target \
  docker.io/library/alpine:latest \
  sh -c 'cp -a /source/. /target/'
```

Use an approved locally available helper image when hosts cannot pull Alpine. If direct access to storage paths is unavoidable with rootless Podman, enter its user namespace:

```bash
podman unshare cp -a <source-path>/. <target-path>/
```

Do not use a plain root `cp` as the default procedure: host ownership IDs may not match the container's user namespace. Applications that rely on ACLs, extended attributes, sparse files, or database consistency need an application-specific export/restore or copy tool. Validate ownership and application behavior against the target, and retain the source volume until the migration is accepted.

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

Retained, attached, unknown, contradictory, and unrelated volumes survive. A `gordon.managed=true` label or a matching name is not sufficient deletion authority. Administrative binds are out of scope entirely: they are operator-owned host paths, no ownership record exists for them, and they are never candidates. With current lifecycle metadata, no operation marks app volumes `released`, so prune normally succeeds with no deletions.

> **Warning:** Do not use `docker volume prune`, `podman volume prune`, or equivalent runtime cleanup for Gordon data. Those commands bypass Gordon's ownership and retention checks and can delete an unmounted retained volume.

See the [Volumes CLI reference](../cli/volumes.md) for flags and plan output.

## Related

- [App Manifest](./apps.md)
- [Volumes CLI](../cli/volumes.md)
- [Configuration Overview](./index.md)
