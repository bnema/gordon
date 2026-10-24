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

Pushing an image only stores it. Nothing runs, no route is created, no manifest is modified. Activation is explicit: use `gordon apps deploy` for manifest changes or
`gordon apps set-image` to run one pushed digest in one service.

## Declarative Apps

One standalone TOML file defines one globally named app with one or more explicitly named image-backed services. The app owns its entrypoints and routes; containers are replaceable runtime instances, not public identities.

```toml
name = "blog"

[env]
APP_ENV = "production"   # app-wide public env, injected into all services

[services.web]
image = "gordon.mydomain.com/blog:1.4.2"

[[services.web.http]]
host = "blog.mydomain.com"
port = 3000

[services.web.env]            # public env for this service only
LOG_LEVEL = "info"

[services.web.secrets]        # ENV name -> secret name (values stay in pass)
DATABASE_URL = "database-url"
```

- `apps apply --file FILE` validates and persists desired configuration only.
- `deploy APP` activates it. `apply --deploy` chains both using exactly the revision accepted by apply.
- `--dry-run` validates and previews without persistence or runtime effects.
- Version tags are recommended, not constrained to SemVer. `latest` remains valid; explicit deploy re-resolves mutable tags while restart uses the active pinned content.
- The file is intended for Git: it must never contain secret values. Confidential values use `secrets`; public values use `[env]` or `[services.<name>.env]`.
- Staging is an ordinary app in another TOML file. There is no pin, no preview environments, and no historical rollback command.

## Updates

Gordon deploys an app's services one at a time in sorted order. A stateless TCP or HTTP service (no volume, bind, device, or UDP interface) is replaced without downtime; every other service is replaced sequentially while Gordon holds its traffic, so clients wait instead of failing within a deadline. Gordon never runs two Gordon-managed generations of a service that owns a volume, a bind, or a device, and a UDP service is excluded because UDP sessions stay pinned to the backend they started on.

Preflight completes before anything is disrupted (image resolution and pull, secrets, volumes, networks, bind policy, reservations). If preflight fails, the running service is left untouched.

A stateless service (no volume, bind, device, or UDP interface) is replaced with a short overlap:

```
Preflight ─► start new ─► readiness ─► publish ACTIVE ─► route traffic ─► SIGTERM old ─► stop_grace ─► kill + remove old
```

The old container keeps serving and stays routed until the new one is ready and traffic points at it. Only then does the old container receive SIGTERM, with its own `stop_grace` (default 30s) before it is killed. While both run, they share the service's network alias, so a request from another service of the app may reach either generation. A database migration must therefore stay backward compatible with the previous version of the service. If the new container never becomes ready, it is removed and the old one is not touched. If the old container cannot be removed after the switch, the deploy still succeeds and reports a cleanup warning. Before the switch Gordon durably marks the old container for retirement, so the periodic recovery pass keeps retrying it, with the old container's `stop_grace`, once the new container is verified; later deploys do not lose the cleanup.

A service with a volume, a bind, a device, or a UDP interface is replaced sequentially:

```
Preflight ─► create new ─► hold traffic ─► stop + remove old ─► start new ─► readiness ─► publish ACTIVE ─► release held traffic
```

Held HTTP requests and TCP connections wait for the new container; UDP routes are withdrawn. When the hold deadline passes or the replacement fails, held HTTP requests get `503` with `Retry-After: 5` and held TCP connections are closed. `traffic.handover.max_hold = "0s"` withdraws traffic instead. See [Handover](./config/traffic.md#handover).

If replacement fails after the old container was removed, the failure is explicit: Gordon does not recreate the old container during that operation and does not promise automatic data rollback. A stateful service's recovery inhibition stays in place until its never-published candidate is confirmed removed; a later boot/start/restart then clears it and rebuilds the generation `ACTIVE` records from its pinned digest, while a candidate that cannot be removed fails recovery closed.

Deployment stops at the first service failure: services already deployed in that run are kept and are not rolled back. This covers TCP, UDP, mixed, and volume-owning services. An open UDP socket is not application readiness, and Gordon never restarts an old volume-owning image automatically after a replacement may have written data.

A deploy interrupted after the replacement container was created but before it was published leaves the failure explicit: the candidate is recorded in the deployment journal, and Gordon removes it before it creates or rebuilds any generation of that app — at boot and before every mutation. For a stateless service the old container keeps serving and is kept. A deploy interrupted after the switch but before the old container was stopped leaves two containers running; the retirement mark makes the periodic recovery pass stop the old one, but only once that same pass has verified and routed the published container. `gordon apps stop` and `gordon apps remove` also stop any such old container, and fail, keeping the mark, if they cannot. Boot recovery never routes a persisted port it has not verified. An interrupted operation is finalized instead of staying in flight: as a failure, or as the success it was when every step had already completed. If a recorded candidate cannot be removed, reconciliation fails closed: the operation is not finalized, stays the latest journal, and every later mutation is refused so a newer operation can never mask the orphan. The next recovery pass republishes routing within its 15-second cadence if the final traffic publication was the step that failed.

Deploy is the only command that applies changes: it recreates a service whose image digest, spec, app environment, or secret values changed, and keeps an unchanged one, reported `unchanged`.

`gordon apps restart` restarts the same container and applies nothing: it holds traffic, restarts the pinned container, verifies readiness, and releases held traffic to it. No second container is created. When the recorded container is gone, restart rebuilds the service from its pinned `ACTIVE` digest and publishes it.

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
[[services.web.http]]
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

Services declare persistent storage in the app manifest. Every Dockerfile `VOLUME` path must be declared explicitly; deployment rejects unmanaged image volumes:

```toml
[[services.web.volume]]
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

Public values for one service use `[services.<name>.env]` and override `[env]` keys for that service. Confidential values use `secrets`:

```toml
[services.web.env]
LOG_LEVEL = "info"

[services.web.secrets]
DATABASE_URL = "database-url"
```

Secret values stay in pass under `gordon/apps/<uuid>/<service>/<name>`, keyed by the stable internal UUID so a removed app's secrets are never adopted by a new app reusing the name. Running containers keep the values they were created with; `gordon apps deploy` applies new values. `restart` does not. Write values with `gordon apps secrets set`; only key names are ever echoed back, never values.

## Installation Reload

Gordon watches `gordon.toml` and reloads installation-only settings (entrypoints, TLS, limits, external routes). Reload never activates pending app desired state, never re-resolves image tags, and never starts app workloads.

```bash
gordon daemon reload
```

`gordon daemon reload` sends `SIGUSR1` to the running Gordon process. `gordon.toml` holds installation settings only — app workloads live in app files.

## Event System

Gordon uses an internal event system for coordination. Registry storage events no longer deploy anything: push events never deploy, create routes, or modify manifests.

## Backups and Recovery

Gordon runs PostgreSQL logical backups and volume archives to S3 for explicitly declared app targets. Declarations live in the app manifest (`[[services.<name>.database]]`, `[services.<name>.backup]`); storage infrastructure (destinations, schedules, retention) stays global.

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

## CI Set-image

CI pushes an image, reads the registry-confirmed digest, then sets that
exact digest. Mutable tags, local image IDs, and the `latest` alias are not
set-image inputs. Set-image changes the desired service image and activates
it; it does not apply arbitrary app configuration.

`server.ci_domain` exposes only OIDC exchange, push, set-image, and scoped
operation lookup. It does not serve image pulls or admin operations.
`server.gordon_domain` remains the main registry and admin host and can stay
private. The daemon pulls through the main registry, not the CI host.
See [CI Deployment](./deployment/generic-ci.md) and
[Set-image policies](./config/set-image.md).

## Related

- [Configuration Reference](./config/index.md)
- [Apps CLI](./cli/apps.md)
- [Getting Started](./getting-started.md)
