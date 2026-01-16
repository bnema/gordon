# Core Concepts

Understanding how Gordon works and why it's designed this way.

## Local-First Development

Your development machine likely has 8-16 cores and 16-32GB RAM. Your VPS has 1-2 cores and 1-4GB RAM. Why build containers on the weak machine?

Gordon flips the typical deployment model:

1. **Build locally** where you have computing power
2. **Push the finished image** to your VPS
3. **Gordon deploys automatically**

This means faster builds, less VPS resource usage, and a simpler deployment workflow.

## Push-to-Deploy

Gordon combines a Docker registry with automatic deployment:

```
┌──────────────┐      push       ┌──────────────┐
│ docker build │  ──────────────>│   Gordon     │
│ docker push  │                 │   Registry   │
└──────────────┘                 └──────┬───────┘
                                        │
                                        │ event: image.pushed
                                        v
                                 ┌──────────────┐
                                 │   Deploy     │
                                 │   Container  │
                                 └──────────────┘
```

When you push an image, Gordon:

1. Stores the image in its registry
2. Fires an `image.pushed` event
3. Looks up the route for that image
4. Deploys a new container
5. Updates the proxy routing
6. Stops the old container

## Zero-Downtime Updates

Gordon ensures your app stays available during updates:

1. **New container starts** while old container is still running
2. **Health check** waits for new container to be ready
3. **Traffic switches** to the new container
4. **Old container stops** after traffic has moved

```
Time ─────────────────────────────────────────────>

Old Container:  [═══════════════════]
                                    ↓ stop
New Container:           [═════════════════════════>
                         ↑ start    ↑ traffic routed
```

## Routes

Routes map domains to container images:

```toml
[routes]
"app.mydomain.com" = "myapp:latest"
"api.mydomain.com" = "myapi:v2.1.0"
```

When a request comes in for `app.mydomain.com`, Gordon:

1. Looks up the route configuration
2. Finds the running container for `myapp:latest`
3. Proxies the request to that container

### HTTP vs HTTPS Routes

By default, routes expect HTTPS (terminated by Cloudflare). For HTTP-only routes:

```toml
[routes]
"http://internal.local" = "internal-app:latest"
```

## Network Isolation

Each app runs in its own isolated Docker network:

```
┌────────────────────────────────────────────────┐
│ gordon-app-mydomain-com                        │
│                                                │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐  │
│  │   App    │───>│ Postgres │    │  Redis   │  │
│  │ :3000    │    │ :5432    │    │ :6379    │  │
│  └──────────┘    └──────────┘    └──────────┘  │
│                                                │
└────────────────────────────────────────────────┘
```

Benefits:

- Containers can't access each other's services
- Services are only accessible by name within their network
- No port conflicts between apps

## Attachments

Attachments are service dependencies for your apps:

```toml
[attachments]
"app.mydomain.com" = ["postgres:latest", "redis:latest"]
```

Gordon deploys attachments to the same network as your app. Services are accessible by their image name:

```javascript
// In your app
const db = await connect("postgresql://postgres:5432/mydb");
const cache = await connect("redis://redis:6379");
```

## Network Groups

Network groups allow multiple apps to share services:

```toml
[network_groups]
"backend" = ["app.mydomain.com", "api.mydomain.com"]

[attachments]
"backend" = ["shared-postgres:latest", "shared-redis:latest"]
```

Both `app.mydomain.com` and `api.mydomain.com` can access the shared services.

## Volumes

Gordon automatically creates persistent storage from Dockerfile `VOLUME` directives:

```dockerfile
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]
```

Volume behavior:

- **auto_create**: Volumes are created automatically (default: true)
- **prefix**: Volume names are prefixed with `gordon-` (configurable)
- **preserve**: Volumes persist across container updates (default: true)

## Environment Variables

Gordon loads environment variables from files based on the domain:

```
~/.gordon/env/
├── app_mydomain_com.env
├── api_mydomain_com.env
└── admin_mydomain_com.env
```

Domain dots become underscores: `app.mydomain.com` → `app_mydomain_com.env`

Variables are merged in order:

1. Dockerfile `ENV` directives (lowest priority)
2. `.env` file values (highest priority)

### Secret Providers

Environment files support secret provider syntax:

```bash
# From Unix password manager (pass)
DATABASE_PASSWORD=${pass:myapp/db-password}

# From SOPS encrypted files
API_SECRET=${sops:secrets.yaml:api.secret}
```

## Configuration Hot-Reload

Gordon watches its config file and reloads automatically:

1. Edit `~/.config/gordon/gordon.toml`
2. Save the file
3. Gordon reloads routes, attachments, and network groups
4. Containers sync to match new configuration

You can also trigger a manual reload:

```bash
gordon reload
```

This sends `SIGUSR1` to the running Gordon process.

## Event System

Gordon uses an internal event system for coordination:

| Event | Trigger | Action |
|-------|---------|--------|
| `image.pushed` | Image pushed to registry | Deploy container |
| `config.reload` | Config file changed | Sync containers |
| `manual.reload` | `gordon reload` command | Sync containers |
| `manual.deploy` | `gordon deploy <domain>` command | Deploy specific route |
| `container.deployed` | Container started | Update proxy cache |

## Container Labels

Gordon uses labels to track managed containers:

| Label | Purpose |
|-------|---------|
| `gordon.managed=true` | Identifies Gordon-managed containers |
| `gordon.domain` | Domain this container serves |
| `gordon.image` | Image name and tag |
| `gordon.route` | Route this container handles |
| `gordon.attachment=true` | Container is an attachment service |
| `gordon.attached-to` | Which route this attachment serves |

## Proxy Port Selection

When a container exposes multiple ports, Gordon needs to know which one serves HTTP:

```dockerfile
FROM gitea/gitea:latest
LABEL gordon.proxy.port=3000  # Route HTTP to port 3000
EXPOSE 22   # SSH
EXPOSE 3000 # HTTP
```

Without the label, Gordon uses the first exposed port.

## Related

- [Configuration Reference](./config/index.md)
- [Docker Labels Reference](./reference/docker-labels.md)
- [Environment Variables](./config/env.md) 
