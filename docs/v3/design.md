# Gordon v3 design

Status: accepted design baseline; not yet implemented  
Date: 2026-09-04  
Supersedes: the distributed implementation archived as `v3-deprecated`

## Purpose

Gordon v3 is a fresh-start redesign focused on one security outcome:

> Compromising an Internet-facing Gordon component must not expose application secrets, the Podman socket, or control-plane private state.

V3 deliberately breaks compatibility with v2. It removes legacy configuration, commands, runtime detection, and migration paths instead of carrying them into the new architecture.

## Scope and non-goals

### In scope

- Four isolated Gordon components on one host.
- Rootless Podman as the only container runtime.
- One declarative manifest per app.
- Apps composed of independently managed services.
- Explicit app entrypoints and protocol-neutral routes.
- Immutable releases pinned to OCI digests.
- Write-only, service-scoped secrets.
- Explicit deployment and partial rollback.
- Public authenticated OCI pushes and opt-in auto-deploy.
- A minimal CLI rebuilt around the v3 model.

### Non-goals

- V2 configuration or CLI compatibility.
- Automatic or live v2-to-v3 migration.
- Multi-host operation or clustering.
- Docker support.
- A shared Podman pod for Gordon components.
- Runtime-selectable deployment strategies.
- Generic readiness checks.
- Zero downtime for stateful, TCP, UDP, or mixed-protocol services.
- Feature parity with v2 during alpha.

## Security model

### Trust boundaries

Gordon runs as four independent containers, outside a shared Podman pod:

```text
Internet
   |
   +--> edge ----------- public application traffic
   |
   +--> registry ------- authenticated OCI push/pull

control ---------------- desired state and administration
   |
runtime ---------------- Podman, workloads, volumes, secret values
```

Each component has:

- a distinct container, user, mount, and network namespace;
- its own root filesystem and private data volume;
- only the Unix sockets it requires;
- only the network attachments it requires;
- no added Linux capabilities;
- `no-new-privileges`;
- a read-only root filesystem where practical.

Only `runtime` receives the rootless Podman socket. Gordon components are separate containers because a Podman pod shares networking and would weaken the intended trust boundaries.

All four containers initially run under one trusted host account and one rootless Podman engine. Container UIDs are not independent host security principals. A compromise of that host account defeats every component boundary. Runtime has full authority over every container in that engine, including Gordon's own containers; runtime compromise therefore defeats the component boundaries as well. A future stronger design may place workloads and Gordon components in separate rootless engines, but that is not part of the accepted alpha baseline.

### Compromise containment

A compromised `edge` may:

- observe public application traffic that it terminates;
- read its sanitized route projection;
- interrupt public traffic and registry passthrough.

It must not be able to:

- read application secrets;
- access the Podman socket;
- mutate desired state;
- invoke runtime operations;
- read registry credentials or traffic carried through TLS passthrough.

A compromised `registry` controls its OCI storage and may emit malformed or forged push events. It has no general runtime or configuration API. When auto-deploy is enabled, however, registry has bounded authority to trigger a release for an already configured repository-and-tag mapping. Control validates and deduplicates events, maps repositories to configured services, checks auto-deploy policy, and selects the resulting release. Without signature or attestation verification rooted outside registry, registry compromise can supply arbitrary content to those opted-in services; this is an accepted risk for alpha.

`control` and `runtime` form the trusted core. Runtime is the most privileged component because it owns the Podman socket and injects workload secrets.

## Deployment topology

V3 is strictly mono-host. The supported topology is:

```text
systemd --user / Quadlet
├── gordon-edge
├── gordon-registry
├── gordon-control
└── gordon-runtime
    └── rootless Podman socket
```

Quadlet, not Gordon, owns component startup and restart. V3 does not contain a component launcher, checkpointed split migration, runtime handoff, or self-orchestration system.

## Component ownership

| Component | Owns | Must not own |
| --- | --- | --- |
| control | App manifests, AppSpec history, releases, route definitions, deployment status, secret metadata | Podman socket, OCI blobs, stored secret values |
| runtime | Podman operations, actual state, workload networks, volumes, stored secret values | Public listeners, desired app manifests, OCI registry storage |
| edge | Public application listeners, active sanitized route snapshot, public app certificates | App secrets, Podman socket, complete manifests, registry credentials |
| registry | OCI blobs, manifests, tags, registry TLS identity, durable push-event outbox | App secrets, Podman socket, deployment decisions |

The components do not share data volumes. Explicit host-mounted Unix-socket directories are communication capabilities, not shared data stores.

## Internal communication

### Transport

Internal APIs use HTTP with strict JSON DTOs over private Unix sockets. Gordon does not use gRPC or protobuf internally in v3.

Streams use NDJSON or server-sent events only where required, such as logs or edge snapshot updates.

Example sockets:

```text
control/admin.sock       local and SSH administration
control/edge.sock        read-only edge projection
control/registry.sock    registry push events only
runtime/control.sock     control-to-runtime operations only
```

Each socket exposes a fixed, minimal handler set. Possession of a mounted socket is the component capability; components do not exchange bearer tokens. Inputs are still treated as untrusted and validated.

### Administration

Control opens no TCP listener by default.

Local flow:

```text
CLI -> host-visible control/admin.sock -> control container
```

Remote flow:

```text
CLI -> OpenSSH Unix-socket forwarding -> remote control/admin.sock -> control container
```

A public control API is deferred beyond the initial alpha. If later enabled, it must be an explicit opt-in with HTTPS, strong authentication, authorization, rate limits, and separate threat analysis. Enabling it makes control Internet-facing and explicitly forfeits the primary v3 containment guarantee. Plain HTTP is never accepted.

Remote configuration defaults to SSH:

```toml
[remotes.production]
host = "user@server"
```

Equivalent defaults:

```text
transport = "ssh"
socket = <default control socket>
```

A future public endpoint would be explicit:

```toml
[remotes.public]
transport = "https"
url = "https://control.example.com"
```

This transport is reserved until its separate security ADR is accepted and implemented.

## Public traffic

The logical edge configuration has one main multiplexed listener:

```toml
[edge]
listen = ":443"
```

This is a desired public address, not yet a claim that an unprivileged container can bind host port 443 directly. Alpha 1 requires a clean-host ingress ADR and proof of concept covering rootless privileged ports, source-IP preservation, fixed versus dynamic TCP/UDP publication, address-specific binds, firewall interaction, and edge restarts. Dedicated TCP/UDP listeners remain blocked until that mechanism is selected.

Edge terminates HTTP/HTTPS application traffic. Registry traffic is selected by SNI and forwarded as raw TCP. Registry terminates its own TLS, so edge never receives registry credentials or decrypted OCI payloads.

Sharing edge introduces an accepted availability dependency: a failed or compromised edge can make registry unavailable, but must not compromise registry confidentiality.

Runtime pulls Gordon-hosted images through a separate private registry endpoint that is reachable by the rootless Podman engine and is not routed through edge. Runtime uses pull-only credentials; control and edge receive no OCI push credentials. The exact private endpoint, TLS identity, and rootless reachability are part of the required host-ingress proof.

## Configuration model

### Installation configuration

`gordon.toml` configures the Gordon installation and its components. It changes infrequently and is not modified by app commands.

It includes control, edge, registry, runtime, authentication-backend, and host-level settings.

### App manifests

Each app has one standalone TOML manifest kept by the user, normally in the application's Git repository:

```text
gordon.app.toml
```

A manifest contains exactly one app and has a globally unique app name:

```toml
name = "shop"
```

The app name is unique across manifests, secrets, releases, volumes, networks, containers, and route identities on one Gordon installation.

The client submits the complete manifest:

```console
gordon apps apply --file gordon.app.toml
```

Control is the sole server-side writer. The CLI never edits a remote TOML file directly. Partial mutation commands for services, entrypoints, and routes do not exist.

### Compact and expanded forms

Compact and expanded forms are both supported and documented together. They normalize to one domain model before validation.

Compact service:

```toml
[services]
frontend = "shop/frontend:v1.4.0"
```

Expanded service:

```toml
[services.frontend]
image = "shop/frontend:v1.4.0"
auto_deploy = true
```

Compact entrypoint:

```toml
[entrypoints]
web = "frontend:3000/http"
```

Expanded entrypoint:

```toml
[entrypoints.web]
service = "frontend"
port = 3000
protocol = "http"
```

Compact forms use defaults only. A single item cannot be declared in both forms. Validation errors should show the equivalent expanded fields.

There is no app-level `image` shorthand or implicit service. Every app has at least one explicitly named service.

## App model

```text
App
├── public environment
├── Services
│   ├── image source
│   ├── service-scoped secrets
│   ├── service-owned volumes
│   └── optional named networks
├── Entrypoints
└── Routes
```

### Services

A service is a logical workload backed by one image. A container is a runtime instance and is not a stable configuration identity.

A service has one active instance. A second instance exists only temporarily while replacing an eligible web frontend.

Persistent dependencies such as PostgreSQL or Valkey may be services inside an app when dedicated to that app. A dependency shared by multiple apps is modeled as its own app.

### Environment and secrets

Public, non-sensitive environment values are global to the app and injected into every service:

```toml
[env]
APP_ENV = "production"
LOG_LEVEL = "info"
```

There is no service-specific public environment block. Any value intended for only one service uses the secret mechanism, even when the value is not confidential.

Services declare their secret names:

```toml
[services.database]
image = "docker.io/library/postgres:18"
secrets = ["POSTGRES_PASSWORD"]
```

The secret identity is service-scoped:

```text
shop/database/POSTGRES_PASSWORD
```

Secret operations are write-only:

```console
gordon secrets set --app shop --service database POSTGRES_PASSWORD
gordon secrets list --app shop --service database
gordon secrets remove --app shop --service database POSTGRES_PASSWORD
```

No local or remote API returns a stored value. Control sees a value transiently during set/update, sends it to runtime, and retains metadata only. Runtime offers set, replace, delete, existence-check, and deployment injection operations, but no read operation.

Write-only semantics protect against accidental API disclosure and at-rest exposure in control. They do not protect a service's secrets from a compromised control plane that is already authorized to deploy that service. Secret mutation does not alter a running container; the new value is injected only on its next deploy or restart. Deployment fails before runtime mutation when any declared secret is absent. Removing a secret still declared by the desired or active AppSpec is rejected.

Secrets are injected as environment variables with the same name. Membership in an app grants no implicit secret access, and secrets cannot be shared by reference across services.

`env_file` is not supported.

### Volumes

Volumes are rootless Podman named volumes owned by one service:

```toml
[services.database.volumes.data]
target = "/var/lib/postgresql/data"
```

The canonical identity includes app, service, and volume names. Host bind mounts and volumes shared between services are forbidden in app manifests.

Releases reuse stable service volumes. Rollback never duplicates or deletes persistent data.

### Networks

Every app automatically receives a private Podman network. All services join it and use service names as DNS aliases.

A service can additionally join an explicitly named private network. This is the only primitive for sharing network access between apps. A shared database, for example, is an independent app whose database service and selected client services join the same named private network.

Gordon does not model provider, consumer, or dependency attachments.

For public routing, the intended topology uses an automatic ingress network per app containing edge and only services targeted by active routes. Edge never joins the app's complete internal network. Runtime is explicitly permitted to reconcile edge's app-ingress network attachments even though Quadlet remains responsible for creating and restarting the edge container. Runtime must restore those attachments after an edge restart. This topology and the rootless host-ingress mechanism require a clean-host Podman/Quadlet proof before Alpha 1 is complete.

## Entrypoints and routes

An app entrypoint is a stable interface backed by a service port. Declaring it never exposes traffic.

```toml
[entrypoints.web]
service = "frontend"
port = 8080
protocol = "http"
```

All public mappings are called routes, regardless of protocol. Routes are nested under their app and target an entrypoint in that app.

HTTP uses edge's shared listener implicitly:

```toml
[routes.web]
host = "game.example.com"
entrypoint = "web"
```

Dedicated TCP and UDP routes own their listen address:

```toml
[routes.game-tcp]
listen = ":27015/tcp"
entrypoint = "game-tcp"

[routes.game-udp]
listen = ":27015/udp"
entrypoint = "game-udp"
```

A private RCON route can bind a private address and restrict peers:

```toml
[routes.rcon]
listen = "100.64.0.1:27020/tcp"
entrypoint = "rcon"
trusted_cidrs = ["100.64.0.0/10"]
```

Route identity is `<app>/<route>`. Removing or changing a route never creates, stops, or deletes a service by itself.

Apply validation canonicalizes DNS names to lowercase ASCII, rejects duplicate exact hosts, and initially rejects wildcard hosts. For dedicated routes, `(listen address, port, transport protocol)` is unique installation-wide. A route's protocol must match its target entrypoint. HTTP routes target `http` entrypoints and edge terminates public TLS; SNI passthrough targets `tcp` entrypoints and leaves TLS untouched. Registry's configured SNI is reserved and conflicts with no app route. Route and certificate conflicts fail during apply, before persistence.

Registry passthrough is a system-generated route derived from registry configuration, not a fake app.

## Image policy

References without a registry hostname target Gordon's registry exclusively:

```text
shop/frontend:v1.4.0
```

External images require a complete hostname:

```text
docker.io/library/postgres:18
ghcr.io/example/worker:v2.1.0
```

Gordon never searches multiple registries. The literal tag `latest` is rejected in AppSpecs and cannot be overridden in v3-alpha.

Tags are ergonomic source selectors, not runtime identities. At deployment, control resolves each selector to an OCI digest. Runtime only receives digest-pinned references:

```text
docker.io/library/postgres@sha256:...
```

If an image cannot be resolved, deployment fails before activating a new release. The stored manifest remains valid and unchanged.

## Apply, releases, and deployment

### Apply

`apps apply` only synchronizes configuration:

```text
receive complete manifest
-> normalize
-> validate
-> compare
-> persist atomically as a new AppSpec revision
```

It never pulls, builds, starts, stops, restarts, removes, or otherwise mutates runtime resources. Routes from a new AppSpec do not become active during apply.

Complete-manifest updates intentionally use last-successful-write-wins semantics in v3-alpha. Apply returns the former and resulting revision and records the authenticated actor when available. Optimistic concurrency may be added later, but alpha does not imply it through an incomplete client-side check.

### Releases

A release is immutable and contains:

- one normalized AppSpec revision;
- exact OCI digests for all services;
- the resolved service runtime definitions;
- the route projection associated with that release.

### Deploy

`gordon deploy <app>` is the only operation that activates a new AppSpec revision. It resolves images, creates a release, performs the runtime change, and activates the release's routes after its required backends are available.

`gordon deploy <app> --service <name>` never activates a pending AppSpec. It is valid only when desired and active AppSpec revisions match; it resolves the selected service's image from that active spec and creates a composed release changing only that service digest.

Release activation follows persisted phases:

```text
prepared -> mutating -> routes-published -> active
                         \-> failed
```

Control persists the operation ID and phase before each external effect. It marks a release active only after runtime reports the intended service set and edge acknowledges the intended route generation. On restart, control observes runtime and edge before resuming or failing an operation; it never blindly replays a mutation. Recreate services are changed in a deterministic service-name order. If a later change fails, control keeps the former route generation, performs bounded best-effort restoration of already changed recreate services, and reports the exact mixed or restored actual state as degraded.

`push --deploy` and auto-deploy may update service digests only when the AppSpec revision is already active. If a newer AppSpec awaits deployment, the image push succeeds but deployment is refused until `gordon deploy <app>` activates the pending specification.

### Auto-deploy

Auto-deploy is disabled by default and configured per service. It applies only to repositories in Gordon's registry and requires an exact repository-and-tag match.

Registry emits a minimal durable event containing repository, tag, digest, timestamp, and event ID. Control owns deduplication, policy evaluation, app/service lookup, and release creation. A repository-and-tag selector with auto-deploy enabled must map to exactly one service installation-wide. Ambiguous mappings are rejected during apply. `push --deploy` uses the same mapping and requires explicit `--app` and `--service` selectors when zero or multiple active targets match.

### Rollback

A full rollback creates a new release from a previous immutable release. A service rollback creates a synthetic release from the currently active AppSpec and route projection, replacing only the selected service's resolved service definition and digest with a previous successful version. App-wide environment, entrypoints, routes, networks, and all other services remain from the active AppSpec. The composition is rejected before mutation if the former service definition is incompatible with those current app-level fields.

```console
gordon rollback shop
gordon rollback shop --to 39
gordon rollback shop --service frontend
gordon rollback shop --service frontend --to 39
```

Gordon validates the composed release before mutation. Historical secret values are not versioned; rollback resolves the current values for the selected service's secret references.

## Deployment strategy

Deployment strategy is not configurable.

An eligible web service uses replacement without a transport-level outage when it:

- is targeted by HTTP/HTTPS routes;
- has no active TCP or UDP route;
- has no persistent volume;
- can run old and new instances concurrently.

The single algorithm is:

```text
start new container
-> wait for Podman running
-> verify that its HTTP entrypoint accepts TCP
-> atomically switch edge routes
-> stop old container
```

V3 has no readiness configuration and does not consume OCI `HEALTHCHECK`. It does not promise application-level readiness.

Every stateful, worker, TCP, UDP, RCON, mixed-protocol, or volume-owning service uses recreate and may experience downtime.

## Lifecycle commands

```console
gordon stop shop
```

Removes active routes and containers while preserving manifest revisions, releases, secrets, and volumes. `gordon deploy shop` starts it again.

```console
gordon remove shop
```

Stops the app and removes server-side manifests, releases, secrets, runtime networks, and containers. Service volumes are retained as identified orphans together with a minimal tombstone reserving the former app name. Registry images are untouched. A new app cannot reuse that name or adopt those volumes implicitly while the tombstone exists.

```console
gordon purge shop
```

Performs remove and deletes app-owned volumes and the tombstone after explicit confirmation containing the app name. It remains valid after `remove` because the tombstone retains only the ownership metadata required to locate and authorize deletion. Registry images remain independently managed.

## Failure behavior

| Failure | Required behavior |
| --- | --- |
| control unavailable | Existing workloads and routes continue. Administration and mutations fail. Registry queues push events durably. |
| runtime unavailable | Existing Podman containers continue. Edge and registry continue. Runtime mutations fail clearly. |
| edge unavailable | Workloads and administration continue. Public app traffic and registry passthrough are unavailable. |
| registry unavailable | Existing apps and routes continue. OCI push/pull fail. Cached digest-pinned images may still be deployable. |

Edge persists only its last valid sanitized route snapshot and public certificates. It may start from that snapshot when control is unavailable. With no valid snapshot, it fails closed. Invalid or older snapshots never replace the active one.

The general rule is that losing the control plane must not interrupt already active workloads or routes.

## CLI surface

### Alpha baseline

Declarative resources:

```text
gordon apps apply --file <manifest>
gordon apps list
gordon apps show <app>
```

Frequent operations remain top-level:

```text
gordon deploy <app> [--service]
gordon rollback <app> [--service] [--to]
gordon restart <app> [--service]
gordon stop <app>
gordon remove <app>
gordon purge <app>
gordon logs <app> [--service]
gordon status [app]
gordon push <image> [--build] [--deploy]
```

Inspection and supporting resources:

```text
gordon routes list [app]
gordon routes show <app>/<route>
gordon secrets set/list/remove
gordon images list
gordon volumes list
gordon networks list
gordon remotes add/list/remove/use
gordon serve --role control|runtime|edge|registry
gordon version
gordon completion
```

### V2 feature tracking

V3-alpha intentionally omits features until they are redesigned on v3 primitives.

| V2 surface | V3-alpha status | Direction |
| --- | --- | --- |
| `attachments` | removed | Replace with app-private and named private networks |
| `autoroute` | removed | Re-evaluate; no implicit public exposure |
| `bootstrap` | removed | Replace with manifest apply, secret set, push, and deploy |
| `pin` | removed | Replace with immutable releases and OCI digests |
| `reload` | removed | No replacement; control owns persisted specs |
| route add/remove/purge | removed | Routes are changed only through the complete app manifest |
| `auth show-token` | removed | Never print stored credentials |
| previews | deferred | Redesign as temporary apps/releases |
| backups | deferred | Redesign around service-owned volumes |
| CA/TLS inspection | deferred | Redesign around edge and registry ownership |
| traffic status | deferred | Fold into route and edge status |
| image/volume prune | deferred | Add explicit resource GC after ownership rules are proven |

This table must remain in the v3 backlog. A v2 feature returns only after its ownership and security semantics are explicit.

## Alpha installation

Stable installation remains version-based. `gordon update` only consumes signed, tagged releases and is not used for alpha testing.

The Alpha 1 installer will support:

```console
curl -fsSL https://gordon.bnema.dev/install | GORDON_BRANCH=v3-alpha sh
```

A precise commit is reproducible:

```console
curl -fsSL https://gordon.bnema.dev/install | GORDON_COMMIT=<sha> sh
```

A checkout can be installed with:

```console
GORDON_LOCAL=1 ./install.sh
```

`GORDON_VERSION`, `GORDON_BRANCH`, `GORDON_COMMIT`, and `GORDON_LOCAL` will be mutually exclusive. Branch and commit modes will fetch the exact source revision, build the Gordon image with rootless Podman, install the matching host CLI, generate/update Quadlets, and report the source commit. They will not alter the semantics of stable `gordon update`. These modes do not exist in the current v2 installer and are not usable until Alpha 1 implements and verifies them.

## Incremental delivery

### Alpha 1: isolated foundation

- Branch and installer flow.
- A clean-host ingress and Unix-socket/UID/SELinux ADR plus Podman/Quadlet proof of concept.
- Four separately confined containers under the documented trusted host account.
- Rootless Podman only, with runtime's full-engine authority explicitly tested.
- Quadlet generation.
- Private Unix sockets and SSH administration.
- Socket recreation, startup-order, ownership, mode, mount, and SELinux tests.
- Status and ownership tests proving only runtime sees Podman.

### Alpha 2: minimal web app

- App manifest and globally unique name.
- Configuration-only apply.
- Explicit deploy.
- One service, private app network, HTTP entrypoint, HTTP route.
- End-to-end edge traffic.

### Alpha 3: multi-service security

- Multiple services.
- Global public environment.
- Service-scoped write-only secrets.
- Service-owned volumes.
- Named private networks across apps.
- Per-app ingress networks.

### Alpha 4: registry and releases

- Public authenticated OCI registry through raw SNI passthrough.
- Digest-only runtime deployments.
- Immutable releases.
- Push deploy and opt-in auto-deploy.
- Durable registry outbox.
- Full and service-level rollback.

### Alpha 5: routes and lifecycle

- HTTP, TCP, and UDP routes.
- Web-only replacement without transport outage.
- Restart, stop, remove, and purge.
- Component restart and failure-matrix tests.
- End-to-end security and recovery checks.

Every commit on `v3-alpha` must remain buildable and testable. Every alpha stage must install on a clean host through the same installer and clearly report unsupported features.

## Remaining implementation details

The following details are intentionally left to focused implementation ADRs, provided they preserve this design:

- exact on-disk formats for AppSpec, releases, and deployment status;
- exact HTTP DTOs and streaming format;
- Unix-socket directory layout and Quadlet UID mappings;
- named shared-network declaration syntax;
- certificate storage and renewal mechanics inside edge and registry;
- bounded event-outbox limits and retry schedule;
- release retention and garbage-collection defaults;
- safe component update ordering for the first tagged v3 release.
