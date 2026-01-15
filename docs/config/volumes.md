# Volumes Configuration

Configure automatic persistent storage for containers.

## Configuration

```toml
[volumes]
auto_create = true
prefix = "gordon"
preserve = true
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `auto_create` | bool | `true` | Automatically create volumes from Dockerfile VOLUME |
| `prefix` | string | `"gordon"` | Prefix for volume names |
| `preserve` | bool | `true` | Keep volumes when containers are removed |

## How It Works

Gordon automatically creates Docker volumes from Dockerfile `VOLUME` directives:

```dockerfile
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]
```

When Gordon deploys this container:

1. Reads `VOLUME` directives from image metadata
2. Creates named volumes with `prefix-domain-path` naming
3. Mounts volumes to the container
4. Preserves data across container updates

## Volume Naming

Volumes are named: `{prefix}-{domain}-{path}`

| Domain | Volume Path | Volume Name |
|--------|-------------|-------------|
| `app.mydomain.com` | `/data` | `gordon-app-mydomain-com-data` |
| `db.mydomain.com` | `/var/lib/postgresql/data` | `gordon-db-mydomain-com-var-lib-postgresql-data` |

## Persistence

### Default: Preserve Volumes

```toml
[volumes]
preserve = true
```

With `preserve = true`:
- Volumes persist when containers are updated
- Data survives container restarts
- Volumes remain even if container is removed

### Remove with Container

```toml
[volumes]
preserve = false
```

With `preserve = false`:
- Volumes are removed when containers are removed
- Useful for stateless containers
- Frees up disk space automatically

## Examples

### Database Container

```dockerfile
# my-postgres.Dockerfile
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]
ENV POSTGRES_DB=myapp
ENV POSTGRES_USER=app
```

Gordon automatically:
- Creates `gordon-db-mydomain-com-var-lib-postgresql-data` volume
- Mounts it to `/var/lib/postgresql/data`
- Preserves data across postgres container updates

### Application with Uploads

```dockerfile
FROM node:18
WORKDIR /app
VOLUME ["/app/uploads", "/app/data"]
COPY . .
CMD ["npm", "start"]
```

Creates two volumes:
- `gordon-app-mydomain-com-app-uploads`
- `gordon-app-mydomain-com-app-data`

### Custom Prefix

```toml
[volumes]
prefix = "prod"
```

Volume names become:
- `prod-app-mydomain-com-data`
- `prod-db-mydomain-com-var-lib-postgresql-data`

## Managing Volumes

### List Volumes

```bash
docker volume ls | grep gordon
```

### Inspect Volume

```bash
docker volume inspect gordon-app-mydomain-com-data
```

### Backup Volume

```bash
docker run --rm \
  -v gordon-db-mydomain-com-var-lib-postgresql-data:/data \
  -v $(pwd):/backup \
  alpine tar -czf /backup/db-backup.tar.gz -C /data .
```

### Restore Volume

```bash
docker run --rm \
  -v gordon-db-mydomain-com-var-lib-postgresql-data:/data \
  -v $(pwd):/backup \
  alpine tar -xzf /backup/db-backup.tar.gz -C /data
```

## Volume Cleanup

If you have orphaned volumes:

```bash
# List all gordon volumes
docker volume ls -f name=gordon

# Remove specific volume (warning: deletes data!)
docker volume rm gordon-old-app-data

# Prune unused volumes (be careful!)
docker volume prune
```

## Related

- [Attachments](./attachments.md)
- [Configuration Overview](./index.md)
