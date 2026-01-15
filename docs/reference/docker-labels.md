# Docker Labels Reference

Labels used by Gordon for container and image metadata.

## Container Labels

Gordon adds these labels to managed containers:

| Label | Value | Description |
|-------|-------|-------------|
| `gordon.managed` | `"true"` | Identifies Gordon-managed containers |
| `gordon.domain` | Domain name | Domain this container serves |
| `gordon.image` | Image:tag | Original image from configuration |
| `gordon.route` | Domain name | Route this container handles |
| `gordon.created` | Timestamp | When Gordon created the container |

### Attachment Labels

Additional labels for attachment containers:

| Label | Value | Description |
|-------|-------|-------------|
| `gordon.attachment` | `"true"` | Container is an attachment service |
| `gordon.attached-to` | Domain/group | Route or network group this serves |

## Image Labels

Labels you can set in your Dockerfile:

| Label | Example | Description |
|-------|---------|-------------|
| `gordon.proxy.port` | `"3000"` | Port to proxy HTTP traffic to |

### Proxy Port Label

When an image exposes multiple ports, Gordon needs to know which one serves HTTP:

```dockerfile
# Gitea exposes SSH (22) and HTTP (3000)
FROM gitea/gitea:latest
LABEL gordon.proxy.port=3000
EXPOSE 22
EXPOSE 3000
```

Without this label, Gordon uses the first exposed port.

**When to use:**
- Image exposes multiple ports
- First exposed port isn't the HTTP service
- You want explicit control over routing

## Container Naming

Gordon names containers:

| Pattern | Example |
|---------|---------|
| `gordon-{domain}` | `gordon-app-mydomain-com` |
| `gordon-{domain}-new` | `gordon-app-mydomain-com-new` (during updates) |
| `gordon-{domain}-{service}` | `gordon-app-mydomain-com-postgres` (attachments) |

## Inspecting Labels

View labels on a container:

```bash
docker inspect gordon-app-mydomain-com --format '{{json .Config.Labels}}' | jq
```

Example output:

```json
{
  "gordon.created": "2024-01-15T10:30:00Z",
  "gordon.domain": "app.mydomain.com",
  "gordon.image": "myapp:latest",
  "gordon.managed": "true",
  "gordon.route": "app.mydomain.com"
}
```

## Filtering Containers

Find Gordon-managed containers:

```bash
# All Gordon containers
docker ps -f "label=gordon.managed=true"

# Containers for specific domain
docker ps -f "label=gordon.domain=app.mydomain.com"

# Attachment containers
docker ps -f "label=gordon.attachment=true"

# Attachments for specific route
docker ps -f "label=gordon.attached-to=app.mydomain.com"
```

## Examples

### Standard Application Container

```bash
docker inspect gordon-app-mydomain-com --format '{{json .Config.Labels}}'
```

```json
{
  "gordon.managed": "true",
  "gordon.domain": "app.mydomain.com",
  "gordon.image": "myapp:v2.1.0",
  "gordon.route": "app.mydomain.com",
  "gordon.created": "2024-01-15T10:30:00Z"
}
```

### Attachment Container

```bash
docker inspect gordon-app-mydomain-com-postgres --format '{{json .Config.Labels}}'
```

```json
{
  "gordon.managed": "true",
  "gordon.attachment": "true",
  "gordon.attached-to": "app.mydomain.com",
  "gordon.created": "2024-01-15T10:29:00Z"
}
```

### Multi-Port Dockerfile

```dockerfile
FROM node:18
LABEL gordon.proxy.port=3000

WORKDIR /app
COPY . .

# HTTP server on 3000
EXPOSE 3000
# Metrics on 9090
EXPOSE 9090
# WebSocket on 8080
EXPOSE 8080

CMD ["npm", "start"]
```

Gordon routes HTTP to port 3000.

## Related

- [Configuration Overview](../config/index.md)
- [Attachments](../config/attachments.md)
- [Concepts](../concepts.md)
