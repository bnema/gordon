# Docker Labels Reference

Labels used by Gordon for container and image metadata.

## Container Labels

Gordon stamps these ownership labels on every container it creates:

| Label | Value | Description |
|-------|-------|-------------|
| `gordon.managed` | `"true"` | Identifies Gordon-managed containers |
| `gordon.app` | App name | App this container serves |
| `gordon.app.service` | Service name | Service this container runs |
| `gordon.app.revision` | Revision | Active revision that created it |
| `gordon.created` | Timestamp | When Gordon created the container |

Queries by logical identity use labels, never name parsing. Resources without these labels (old or foreign containers) are preserved, never adopted or deleted.

### Legacy Labels

Containers created before v2.50 may carry these labels. They are read-only provenance hints for prune/backup guards — Gordon never infers app state from them:

| Label | Value | Description |
|-------|-------|-------------|
| `gordon.domain` | Domain name | Pre-v2.50 domain this container served |
| `gordon.image` | Image:tag | Original image from configuration |
| `gordon.route` | Domain name | Pre-v2.50 route this container handled |
| `gordon.attachment` | `"true"` | Pre-v2.50 attachment service marker |
| `gordon.attached-to` | Domain/group | Pre-v2.50 attachment target |

### Backup Labels

Labels used by the backup subsystem:

| Label | Value | Description |
|-------|-------|-------------|
| `gordon.backup` | `"true"` / `"false"` | Enables or disables backup behavior for a container |
| `gordon.backup.type` | e.g. `"postgresql"` | Explicit database type override |
| `gordon.backup.version` | e.g. `"17"` | Explicit database version override |
| `gordon.backup.schedule` | e.g. `"hourly,daily"` | Schedule override hint |
| `gordon.backup.sidecar` | `"true"` | Identifies backup sidecar containers |

## Image Labels

No image-label inference exists in v2.50: Gordon never creates routes, deploys, or resolves image names from Dockerfile labels. Push with a domain-like name or a `gordon.domain` label is treated as an ordinary image name. Readiness and proxy ports come from the app manifest (`[service.readiness]`, `[[service.http]]`), not from image labels.

## Container Naming

Gordon names containers `gordon-<app>--<service>--<instance>` where `<instance>` is the creating operation ID. Retiring containers keep their instance names until removal.

## Inspecting Labels

View labels on a container:

```bash
docker inspect <container> --format '{{json .Config.Labels}}' | jq
```

Example output:

```json
{
  "gordon.managed": "true",
  "gordon.app": "blog",
  "gordon.app.service": "web",
  "gordon.app.revision": "rev-3",
  "gordon.created": "2024-01-15T10:30:00Z"
}
```

## Filtering Containers

Find Gordon-managed containers:

```bash
# All Gordon containers
docker ps -f "label=gordon.managed=true"

# Containers for a specific app
docker ps -f "label=gordon.app=blog"

# Backup sidecars
docker ps -f "label=gordon.backup.sidecar=true"
```

## Related

- [Configuration Overview](../config/index.md)
- [App Manifest](../config/apps.md)
- [Concepts](../concepts.md)
