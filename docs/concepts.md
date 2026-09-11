# Core Concepts

Understanding how Gordon works and why it's designed this way.

## Local-First Development

Your development machine likely has 8-16 cores and 16-32GB RAM. Your VPS has 1-2 cores and 1-4GB RAM. Why build containers on the weak machine?

Gordon flips the typical deployment model:

1. **Build locally** where you have computing power
2. **Push the finished image** to your VPS registry
3. **Apply the app manifest** declaring the image
4. **Deploy** to activate it

This means faster builds, less VPS resource usage, and a simpler deployment workflow.

## Push, Apply, Deploy

Gordon combines a Docker registry with a declarative app runtime:

```
┌──────────────┐      push       ┌──────────────┐
│ docker build │  ──────────────>│   Gordon     │
│ docker push  │   (OCI only,    │   Registry   │
└──────────────┘   never deploys)└──────┬───────┘
                                        │
                    ┌───────────────────┴───────────────────┐
                    │  gordon apps apply --file blog.toml   │  desired state
                    │  gordon apps deploy blog              │  activation
                    └───────────────────┬───────────────────┘
                                        v
                                 ┌──────────────┐
                                 │   App        │
                                 │  Containers  │
                                 └──────────────┘
```

Pushing an image only stores it. Nothing runs, no route is created, no manifest is modified. Activation is always an explicit `gordon apps deploy`.

## Declarative Apps

One standalone TOML file defines one globally named app with one or more explicitly named image-backed services. The app owns its entrypoints and routes; containers are replaceable runtime instances, not public identities.

```toml
name = "blog"

[env]
APP_ENV = "production"   # app-wide public env, injected into all services

[[service]]
name = "web"
image = "gordon.mydomain.com/blog:1.4.2"

[[service.http]]
host = "blog.mydomain.com"
port = 3000

[service.secrets]        # ENV name -> secret name (values stay in pass)
DATABASE_URL = "database-url"
```

- `apps apply --file FILE` validates and persists desired configuration only.
- `deploy APP` activates it. `apply --deploy` chains both using exactly the revision accepted by apply.
- `--dry-run` validates and previews without persistence or runtime effects.
- Version tags are recommended, not constrained to SemVer. `latest` remains valid; explicit deploy re-resolves mutable tags while restart uses the active pinned content.
- The file is intended for Git: it must never contain secret values. Service-specific values use `secrets` even when non-confidential.
- Staging is an ordinary app in another TOML file. There is no pin, no preview environments, and no historical rollback command.

## Updates

For HTTP services without volumes, Gordon keeps the old container serving until the replacement passes readiness, then switches traffic, drains with a bounded deadline, and retires that service's old container. Deployment stops at the first service failure: already successful services are preserved, later services stay unchanged.

```
Time ─────────────────────────────────────────────>

Old Container:  [═══════════════════]
                                    ↓ retire
New Container:           [═════════════════════════>
                         ↑ start    ↑ traffic routed
```

TCP, UDP, mixed, and volume-owning services replace with interruption — no zero-downtime promise. An open UDP socket is not application readiness. Gordon never restarts an old volume-owning image automatically after a replacement may have written data.

## Deletion and Cleanup Lifecycle

Gordon separates workload removal from destructive data cleanup:

- **desired** — the persisted manifest revision waiting to be activated.
- **active** — the pinned, running definition (never inferred from containers).
- **stopped** — durable stopped intent: workloads are down, data preserved, reboot keeps them stopped.
- **retained** — volumes and secrets kept after app removal under the old internal UUID, visible but never implicitly adopted by a new app reusing the name.

Safe removal is the default. `gordon apps remove` withdraws workloads and frees the name; volumes and secrets are retained as owned orphans. There is deliberately no `purge`: destructive volume deletion requires a separately accepted destructive-action contract. Ordinary apply/deploy/restart/stop/remove never delete user volumes.

## App HTTP Hosts

An app's HTTP interfaces declare the hosts Gordon serves:

```toml
[[service.http]]
host = "app.example.com"
port = 3000
```

When a request comes in for `app.example.com`, Gordon:

1. Looks up the host in the ACTIVE projection (merged with installation external routes)
2. Finds the recorded loopback backend for that host (never a container IP — rootless-first)
3. Proxies the request to that backend

Hostnames must be plain hostnames. `https` behavior per host follows the `tls` mode (`auto`, `always`, `never`).

## Networks

Each app gets a private network automatically. Services can additionally join named shared networks, created and reused only within verified Gordon ownership:

```toml
[[network.shared]]
network = "backend"
services = ["web", "worker"]
```

Deploy adds AND removes memberships without disconnecting unrelated services. Short DNS names resolve privately; app-qualified aliases apply on shared networks.

## Volumes

Services declare persistent storage in the app manifest. When `volumes.auto_create` is enabled, Gordon also creates app-owned named volumes for undeclared Dockerfile `VOLUME` paths:

```toml
[[service.volume]]
name = "web-data"
path = "/data"
```

Docker/Podman own the named volumes; Gordon records app UUID, service, and logical volume ownership. App manifests allow neither bind mounts nor service-shared volumes. Replacement and restart reuse volumes. Removing a service or app retains its volumes under the original app UUID, and a new app reusing the public name never adopts them.

Use `gordon volumes prune --dry-run` to inspect Gordon's ownership-aware plan. Do not use `docker volume prune` or an equivalent runtime command for Gordon data: it bypasses Gordon's retention checks and can delete unmounted retained volumes.

## Environment and Secrets

App-wide public env is declared in the manifest:

```toml
[env]
APP_ENV = "production"
```

Service-specific values use `secrets` even when non-confidential:

```toml
[service.secrets]
DATABASE_URL = "database-url"
```

Secret values stay in pass under `gordon/apps/<uuid>/<service>/<name>`, keyed by the stable internal UUID so a removed app's secrets are never adopted by a new app reusing the name. Secret updates affect the next deploy/restart, not running containers. Write values with `gordon apps secrets set`; only key names are ever echoed back, never values.

## Installation Reload

Gordon watches `gordon.toml` and reloads installation-only settings (entrypoints, TLS, limits, external routes). Reload never activates pending app desired state, never re-resolves image tags, and never starts app workloads.

```bash
gordon reload
```

`gordon reload` sends `SIGUSR1` to the running Gordon process. `gordon.toml` holds installation settings only — app workloads live in app files.

## Event System

Gordon uses an internal event system for coordination. Registry storage events no longer deploy anything: push events never deploy, create routes, or modify manifests.

## Backups and Recovery

Gordon runs PostgreSQL logical backups and volume archives to S3 for explicitly declared app targets. Declarations live in the app manifest (`[[service.database]]`, `[service.backup]`); storage infrastructure (destinations, schedules, retention) stays global.

Stored backups are never deleted when declarations change — only schedules update on deploy.

For configuration details and usage examples, see the [Backups Configuration guide](./config/backups.md), [Backup CLI reference](./cli/backup.md), and [Configuration Reference](./config/reference.md).

## Container Identity

Gordon stamps app ownership labels on every container and volume it creates:

| Label | Purpose |
|-------|---------|
| `gordon.managed=true` | Identifies Gordon-managed resources |
| `gordon.app` | App public name |
| `gordon.app.service` | Service name |
| `gordon.app.revision` | Active revision that created it |

Queries by logical identity use labels, never name parsing. Unknown resources (no labels, old labels) are preserved, never adopted or deleted.

## Related

- [Configuration Reference](./config/index.md)
- [Apps CLI](./cli/apps.md)
- [Getting Started](./getting-started.md)
