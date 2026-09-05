# Gordon v3 design

Status: accepted design baseline; not yet implemented

Date: 2026-09-04; host-ingress amendment: 2026-09-05

Decisions: [ADR-001](adr-001-v3-foundation.md), amended by [ADR-002: host ingress](adr-002-host-ingress.md). The ingress direction is accepted; its implementation and security proofs remain gated.

Supersedes: the distributed implementation archived as `v3-deprecated`

## Purpose

Gordon v3 is a fresh-start redesign focused on one security outcome:

> Compromising an Internet-facing Gordon component must not expose application secrets, the Podman socket, or control-plane private state.

V3 deliberately breaks compatibility with v2. It removes legacy configuration, commands, runtime detection, and migration paths instead of carrying them into the new architecture.

## Scope and non-goals

### In scope

- One Gordon distribution, host binary, and component image generation for one installation.
- Four isolated Gordon containers and one confined host ingress role.
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

The [multi-host evolution review](multi-host-evolution.md) records boundaries to
preserve and mono-host assumptions to revisit if cluster work is proposed. It is
a planning note, not an extension of this accepted scope or a cluster API contract.

## Security model

### Trust boundaries

Gordon runs four independent rootless containers, outside a shared Podman pod, plus a confined, non-root host ingress process from the same executable:

```text
Internet -> administrator-managed firewall/redirects
   |
   v
host ingress -- TCP listener handoff / bounded UDP relay --> edge
                                                            |
                                               apps / registry TLS passthrough

control ---------------- desired state and administration
   |                     authorizes ingress listener operations
runtime ---------------- Podman, workloads, volumes, secret values
```

Each container has:

- a distinct container, user, mount, and network namespace;
- its own root filesystem and private data volume;
- only the Unix sockets it requires;
- only the network attachments it requires;
- no added Linux capabilities;
- `no-new-privileges`;
- a read-only root filesystem where practical.

Only `runtime` receives the rootless Podman socket. Gordon components are separate containers because a Podman pod shares networking and would weaken the intended trust boundaries.

All four containers initially run under one trusted host account and one rootless Podman engine. Container UIDs are not independent host security principals. A compromise of that host account defeats every component boundary. Runtime has full authority over every container in that engine, including Gordon's own containers; runtime compromise therefore defeats the component boundaries as well. A future stronger design may place workloads and Gordon components in separate rootless engines, but that is not part of the accepted alpha baseline.

The host ingress process is also Internet-facing, through UDP. It must have an OS-enforced sandbox excluding secrets, control-private state, the Podman socket/storage and same-account process/filesystem escape paths. Running it unconfined under the Podman account would defeat the primary containment goal; non-root and `NoNewPrivileges` alone are insufficient. Public use remains blocked until this boundary is proven on the reference host (ADR-002).

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
- read registry credentials or traffic carried through TLS passthrough;
- obtain host UDP descriptors or direct ingress to arbitrary host-network destinations.

A compromised `registry` controls its OCI storage and may emit malformed or forged push events. It has no general runtime or configuration API. When auto-deploy is enabled, however, registry has bounded authority to trigger a release for an already configured repository-and-tag mapping. Control validates and deduplicates events, maps repositories to configured services, checks auto-deploy policy, and selects the resulting release. Without signature or attestation verification rooted outside registry, registry compromise can supply arbitrary content to those opted-in services; this is an accepted risk for alpha.

`control` and `runtime` form the trusted core. Runtime is the most privileged component because it owns the Podman socket and injects workload secrets.

## Deployment topology

V3 is strictly mono-host. One Gordon distribution provides the host CLI and one component image containing an octet-identical copy of that Gordon executable. The image runs that executable in four container serve modes. The installed executable also runs the host ingress role under a user systemd service; exact serve syntax is fixed during Alpha 1. "Distribution" and "installation generation" refer to Gordon itself; "release" remains reserved for immutable app deployments.

```text
host: gordon <version>
  |
  +-- owns installation state, generated Quadlets and ingress service
  |
  `-- systemd --user
      ├── host ingress (same installed executable; confined service)
      `── four Quadlet-managed containers
          ├── edge
          ├── registry
          ├── control
          `── runtime
              `── rootless Podman socket
```

A distribution identity binds its version or source revision, executable SHA-256, component-image OCI digest, and persistent-format versions. The build copies that exact executable into the image and verifies its hash before image publication; the signed manifest attests both that hash and the resulting image digest. Quadlets reference the image by digest, never by a mutable tag. Branch and commit builds are attributable to one exact clean revision but are not authenticated releases. A dirty local checkout receives an explicit identity containing both the commit and source-tree hash instead of reporting clean `HEAD`.

The installed host binary and all five running roles (four containers plus ingress) must match one intended distribution identity. A future component update will necessarily observe mixed generations while replacing processes; that is permitted only as an explicit transitional state and must never be reported healthy. Alpha 1 supports fresh installation and recovery of that same incomplete installation only. Installing a different generation, component update, and component rollback remain blocked on the lifecycle ADR.

### Component lifecycle ownership

Gordon owns the declarative lifecycle of its installation. A minimal shell bootstrap obtains and verifies or builds the host binary, then invokes its host-side installation command. That command:

- allows one Gordon installation per host account and rootless Podman engine;
- serializes mutations with an installation lock;
- records intended, staged, running, and failure state in a persistent journal;
- uses atomic, idempotent steps so the same generation can resume after interruption;
- installs the matching component image and creates installation directories and initial configuration;
- generates the four Quadlet definitions and the confined host ingress service;
- asks the user systemd manager to reload, enable, start, or stop the installation target;
- verifies process state, role readiness, dependencies, and distribution identity without granting a mutation capability to public components;
- reports partial operations as degraded or incomplete without claiming success.

Quadlet translates Gordon's Podman declarations into systemd units. Systemd owns continuous process supervision, boot activation, dependency ordering, and restart-after-failure. The installer must configure and test the user-manager lingering policy required for boot without an interactive login. Podman creates and runs the containers. Ingress is a fifth resident role, not a supervisor: it cannot launch components or manage systemd/Podman. Gordon does not reimplement systemd.

Generated Quadlets and the ingress service are Gordon-owned outputs, not user configuration. Gordon records their generation and checksums, writes them atomically, and refuses to overwrite unknown edits without an explicit recovery operation that preserves a backup. Supported customization belongs in `gordon.toml`.

This lifecycle applies only to Gordon's five managed roles. Application containers remain desired by control and reconciled by runtime through the Podman socket. Gordon v3 does not contain the archived checkpointed split migration, runtime handoff, or autonomous self-upgrade system. Component update support remains blocked until the focused lifecycle ADR defines safe replacement order, persistent-format compatibility, rollback or roll-forward, and interruption recovery.

## Component ownership

| Component | Owns | Must not own |
| --- | --- | --- |
| control | App manifests, AppSpec history, releases, route definitions, deployment status, secret metadata | Podman socket, OCI blobs, stored secret values |
| runtime | Podman operations, actual state, workload networks, volumes, stored secret values | Public listeners, desired app manifests, OCI registry storage |
| ingress (host) | Authorized host binds, TCP descriptor handoff, bounded UDP sessions and relay | Firewall policy, TLS/routing decisions, app secrets, Podman, component supervision |
| edge | Handed-off TCP listeners, HTTP/TCP/UDP routing, client policy, active sanitized route snapshot, public app certificates | Host UDP descriptors, app secrets, Podman socket, complete manifests, registry credentials |
| registry | OCI blobs, manifests, tags, registry TLS identity, durable push-event outbox | App secrets, Podman socket, deployment decisions |

The components do not share data volumes. Explicit host-mounted Unix-socket directories are communication capabilities, not shared data stores. Ingress administration is accessible only to control, separately from the edge data capability; edge cannot request new host binds or choose arbitrary UDP destinations.

Gordon component containers, networks, volumes, and Quadlet-managed resources use reserved names and ownership labels. Runtime workload reconciliation and future garbage collection must exclude them. Runtime's narrowly defined, idempotent reconciliation of edge's app-ingress network attachments is its only permitted workload-side mutation of Gordon container resources; implementation proofs must ensure it cannot become a generic component-management path. Control-authorized ingress listener operations do not grant runtime additional authority.

## Internal communication

### Transport

Ordinary internal control APIs use HTTP with strict JSON DTOs over private Unix sockets. ADR-002 adds a narrow Unix IPC transport for live TCP descriptor transfer (`SCM_RIGHTS`) and framed UDP sessions between ingress and edge. Its format, peer validation and recovery protocol remain implementation gates; it is not generic RPC. Gordon does not use gRPC or protobuf internally in v3.

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

This is a logical public address, not a requirement for an unprivileged process to bind host 443 directly. The administrator configures firewalld or equivalent, including public access and any redirect such as 443 to 8443. Gordon binds the configured local destination; it does not change firewall rules or host sysctls. Public-to-local mapping syntax and reservation checks must distinguish both addresses before implementation, without introducing an installation-level catalogue of app ports.

[ADR-002](adr-002-host-ingress.md) selects a confined host ingress role. Control authorizes listener changes from the deploying release, never from apply alone. Ingress transfers TCP listeners to the already-running edge and closes its copies after acknowledged handoff. It retains UDP sockets and relays datagrams through bounded, private sessions; replies can target only the original client of an ingress-owned session. Host UDP sockets are never passed to edge. Edge keeps TLS, routing, backend selection, CIDR policy and connection/session handling.

TCP handoff requires descriptor type, `LISTEN` state, family and authorized bind-address validation, plus negative tests of residual host-network authority. TCP peer identity comes from the handed-off socket; UDP identity comes from authenticated ingress metadata, not client payloads. Each UDP session includes the kernel-observed local destination and family/interface/scope where needed, so ingress can choose the correct reply source for wildcard and multi-address binds. Both require full-path allow/deny tests. Neither mechanism reverses prior source NAT, and ordinary edge-to-backend proxying still exposes edge's address at the backend. Backend original-source requirements remain gated.

Alpha 1 still requires clean-host proofs for host-process confinement, dynamic listener ownership and withdrawal, interrupted handoff, restart/reboot recovery, address-specific and dual-stack conflicts, firewall coexistence and ingress-network isolation. General bidirectional UDP sessions, binary fidelity, limits and performance must pass before UDP exposure. A request/response prototype is not evidence of game-server compatibility.

Edge terminates HTTP/HTTPS application traffic. Registry traffic is selected by SNI and forwarded as raw TCP. Registry terminates its own TLS, so edge never receives registry credentials or decrypted OCI payloads.

Sharing edge introduces an accepted availability dependency: a failed or compromised edge can make registry unavailable, but must not compromise registry confidentiality.

TLS passthrough alone does not establish that confidentiality guarantee. Before public registry access is enabled, a certificate-lifecycle ADR must define issuance, renewal, and client trust so a compromised edge cannot obtain a registry identity accepted by clients. In particular, evaluate registry-domain ACME HTTP-01 and TLS-ALPN-01 challenges against edge's control of public ports. Keeping registry's existing private key out of edge is necessary but insufficient. The trust mechanism remains undecided; no public registry implementation may assume that SNI routing enforces it.

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

There is no service-specific public environment block. Any value intended for only one service uses the secret mechanism, even when the value is not confidential. This deliberately keeps two configuration paths: public app-wide values in the manifest and write-only service-owned values outside it. The manifest declares the required secret names, not a complete reproducible set of service values. Those values must be provisioned separately and are not restored by release rollback.

A declared secret name must not collide with an app-wide public environment key; validation rejects the collision rather than silently choosing a value.

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

Releases reuse stable service volumes. Rollback never duplicates or deletes persistent data and does not reverse database or filesystem migrations. An older image is not evidence that the current volume remains compatible. Automatic best-effort restoration must not restart an older volume-owning service after a replacement may have written its data; report the degraded state for operator recovery instead. An explicit rollback of such a service requires the operator to establish data compatibility outside Gordon.

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

Apply validation canonicalizes DNS names to lowercase ASCII, rejects duplicate exact hosts, and initially rejects wildcard hosts. Dedicated listeners must not overlap installation-wide for the same transport protocol, including wildcard-versus-specific address binds and IPv4/IPv6 dual-stack conflicts. Installation listeners, including edge's shared HTTP/HTTPS ports, participate in this check. A route's protocol must match its target entrypoint. HTTP routes target `http` entrypoints and edge terminates public TLS; SNI passthrough targets `tcp` entrypoints and leaves TLS untouched. Registry's configured SNI is reserved and conflicts with no app route. Route and certificate conflicts fail during apply, before persistence.

Control reserves route hosts and listener bindings across desired AppSpecs, active routes, and in-flight operations. Reservations belonging to the same app may overlap across those states, but a candidate configuration must remain internally conflict-free. Removing a route from desired state does not free its active reservation. It becomes available to another app only after no desired or in-flight reference remains, edge has acknowledged withdrawal, and ingress has confirmed release of the relevant host listener/session ownership.

TCP handoff gives ingress no remote revocation power over edge's copy: an ACK describes cooperative withdrawal, not proof against retained descriptors. Timeout, refusal or uncertain closure fails withdrawal and retains the reservation. Reassignment then requires operator-controlled installation recovery to stop descriptor holders and verify release, without restoring the withdrawn listener or granting runtime/ingress new restart authority.

Historical releases do not reserve routes indefinitely: deploy and rollback revalidate and acquire reservations before runtime mutation. Apply validation and reservation changes are one atomic control-side operation; they do not change edge or Podman.

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

- the immutable effective normalized AppSpec;
- source AppSpec revision (the base active release's revision for service composition) and the source release identities;
- exact OCI digests for all services;
- the resolved service runtime definitions;
- the route projection associated with that release.

### Deploy

`gordon deploy <app>` is the only operation that activates a new AppSpec revision. It resolves images, creates a release, performs the runtime change, and activates the release's routes after its required backends are available.

`gordon deploy <app> --service <name>` never activates a pending AppSpec. It is valid only when desired and active source AppSpec revisions match and the desired normalized configuration equals the active release's effective configuration. Revision equality alone is insufficient after a synthetic rollback. It resolves the selected service's image selector from that effective configuration and creates a composed release changing only that service digest.

Control serializes app mutations, including apply, deploy, rollback, secret mutation, restart, stop, remove, and purge. An operation captures its configuration and source release before effects; a concurrent request cannot change that operation's inputs. Global route reservations and edge snapshot publication also require installation-wide coordination so deployments of different apps cannot overwrite each other's routes. The persistence and edge-snapshot ADRs must define these atomicity boundaries before Alpha 2.

Release activation follows persisted phases:

```text
prepared -> mutating -> routes-published -> active
                         \-> failed
```

Control persists the operation ID and phase before each external effect. It marks a release active only after runtime reports the intended service set, edge acknowledges the intended route generation, and ingress confirms the required listener handoffs or UDP relay readiness. On restart, control observes runtime, edge and ingress before resuming or failing an operation; it never blindly replays a mutation. Recreate services are changed in a deterministic service-name order. If a later change fails, control keeps the former route generation, performs bounded best-effort restoration of already changed recreate services subject to the volume-safety restriction above, and reports the exact mixed or restored actual state as degraded.

`push --deploy` and auto-deploy use the same revision and effective-configuration checks as service-targeted deploy. If a newer AppSpec awaits deployment or a synthetic rollback has changed the effective configuration, the image push succeeds but deployment is refused until a full `gordon deploy <app>` activates the desired specification. They cannot implicitly reconcile that divergence.

### Auto-deploy

Auto-deploy is disabled by default and configured per service. It applies only to repositories in Gordon's registry and requires an exact repository-and-tag match.

Registry emits a minimal durable event containing repository, tag, digest, timestamp, and event ID. Control owns deduplication, policy evaluation, app/service lookup, and release creation. A repository-and-tag selector with auto-deploy enabled must map to exactly one service installation-wide. Ambiguous mappings are rejected during apply. `push --deploy` uses the same mapping and requires explicit `--app` and `--service` selectors when zero or multiple active targets match.

### Rollback

A full rollback creates a new release from a previous immutable release. A service rollback creates a synthetic release from the active release's effective configuration and route projection, replacing only the selected service's resolved service definition and digest with a previous successful version. App-wide environment, entrypoints, routes, network declarations, and all other services remain from the active release's effective AppSpec. Runtime reconciles actual network attachments from those declarations; observed Podman state is not a configuration source. The composition is rejected before mutation if the former service definition is incompatible with those retained fields.

Rollback never rewrites desired configuration or historical releases. A service rollback creates a new release identity, retains the base active release's source AppSpec revision as provenance, and records the base and donor release identities plus the composed effective AppSpec. That retained revision does not claim that the effective configuration is unchanged. A full rollback retains the selected historical release's source AppSpec revision. Status exposes any divergence between desired and effective configuration; subsequent service-targeted deploy, push deploy, and auto-deploy follow the checks above.

```console
gordon rollback shop
gordon rollback shop --to 39
gordon rollback shop --service frontend
gordon rollback shop --service frontend --to 39
```

Gordon validates the composed release before mutation. Historical secret values are not versioned; rollback resolves the current values for the selected service's secret references.

## Deployment strategy

Deployment strategy is not configurable. The web replacement algorithm below remains gated on a focused rollout ADR before Alpha 5. That ADR must establish how Gordon knows concurrent instances are safe; HTTP routes and absence of volumes cannot prove this for an image that may also run migrations, schedulers, or workers. This design does not yet choose a concurrency-eligibility mechanism or add a user-selectable strategy.

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
-> drain existing requests/connections up to a bounded deadline
-> stop old container
```

V3 has no readiness configuration and does not consume OCI `HEALTHCHECK`. It does not promise application-level readiness or uninterrupted connections beyond the drain deadline. The rollout ADR must define multi-entrypoint checks, the drain deadline, and treatment of long-lived connections such as WebSockets.

Every stateful, worker, TCP, UDP, RCON, mixed-protocol, or volume-owning service uses recreate and may experience downtime.

## Lifecycle commands

Control persists an app's execution intent (`running` or `stopped`) separately from desired configuration, the last active release, and observed containers. Apply does not change execution intent. A newly applied app is stopped. A full deploy of a stopped app changes durable intent to running only when its new release successfully activates. Its journaled in-flight operation may create resources and resume after interruption, but generic reboot reconciliation must not resurrect a retained prior release. On failure, intent remains stopped and operation recovery cleans up partial resources rather than treating them as an active app.

`restart` uses the active release's effective configuration and pinned digests, with current secret values. It never resolves newer image tags or activates pending configuration. Restart, rollback, service-targeted deploy, push deploy, and auto-deploy refuse a stopped app; only a full `deploy` can request running state again. Queued events cannot undo `stop`.

```console
gordon stop shop
```

Persists stopped intent before removing active routes and containers, while preserving manifest revisions, releases, secrets, and volumes. Interrupted stop resumes cleanup rather than resurrecting the app. `gordon deploy shop` starts it again.

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
| ingress unavailable | Handed-off TCP traffic can continue. UDP and new host binds are unavailable. Healthy edge is not restarted merely to recover ingress. |
| edge unavailable | Workloads and administration continue. Public app traffic and registry passthrough are unavailable. |
| registry unavailable | Existing apps and routes continue. OCI push/pull fail. Cached digest-pinned images may still be deployable. |

Edge persists only its last valid sanitized route snapshot and public certificates. It may start from that snapshot when control is unavailable, but public readiness also requires validated listener recovery with ingress. With no valid snapshot, it fails closed. Invalid or older snapshots never replace the active one. Ingress recovery must use authorized applied state and observed ownership, not arbitrary bind requests from edge or a pending AppSpec; the protocol and minimal persisted metadata remain gated by ADR-002.

The general rule is that losing the control plane must not interrupt already active workloads or routes.

Host reboot is distinct from a component restart. Before Alpha 2, the workload-recovery ADR must define how persisted execution intent and active releases restore running apps, keep stopped apps stopped, and recover interrupted operations. It must identify who starts workload containers and when edge's persisted backends become valid again. Do not assume component Quadlets or user-manager lingering alone restart applications. Reboot recovery may wait for the trusted core; pending AppSpecs and newly resolved tags must never replace the last active release implicitly.

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

The host ingress role also uses the installed executable; its serve-mode spelling is not yet fixed. It has no public administration endpoint and is not a replacement for edge.

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

Alpha 1 uses Go 1.27 and Ubuntu 26.04 LTS as its reference build and clean-host validation baseline.

Stable installation remains version-based. The future v3 `gordon update` will consume only signed, tagged distributions, but component update is unavailable until the lifecycle ADR is accepted and implemented. A signed distribution manifest defines its future input by binding version and source commit to the executable hash, component-image digest, and persistent-format versions.

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

`GORDON_VERSION`, `GORDON_BRANCH`, `GORDON_COMMIT`, and `GORDON_LOCAL` will be mutually exclusive. In future version mode, before executing the downloaded Gordon binary, the shell bootstrap verifies the distribution-manifest signature with a pinned release public key and verifies the executable SHA-256 from that manifest. The initial `curl | sh` still trusts HTTPS delivery of the bootstrap itself; users requiring an independent root of trust must obtain and verify the installer through a separately trusted channel. Branch and commit modes resolve one exact clean revision; local mode records a source-tree hash when the checkout is dirty. Source modes are explicitly unauthenticated alpha inputs and use the resulting identity for both artifacts.

The shell is bootstrap transport only: after pre-execution verification or a local/source build, it invokes Gordon's host-side installer. Gordon acquires the installation lock and resumable journal before it:

1. verifies rootless Podman, Quadlet, the user systemd manager, and boot-without-login policy;
2. builds or acquires the matching component image and records its immutable digest;
3. installs the host CLI and distribution identity;
4. creates installation directories, configuration, networks, and capability-socket directories;
5. atomically generates four Quadlets that invoke the digest-pinned image in distinct serve roles, plus the confined ingress service invoking the same installed executable;
6. reloads the user systemd manager and enables/starts the installation target;
7. verifies process state, readiness, dependencies, confinement, and identity for all five roles;
8. records success or an explicitly incomplete, resumable state and reports the exact distribution identity.

Alpha 1 accepts only a clean host or resumption of the same incomplete generation. It does not replace another generation and makes no component update or rollback promise. The v3 `gordon update` command remains unavailable. Enabling it requires the lifecycle ADR to define transitional states, replacement order, persistent-format compatibility, backup, rollback versus roll-forward, and recovery after interruption. Source modes do not create an alpha update channel. They do not exist in the current v2 installer and are not usable until Alpha 1 implements and verifies them.

## Incremental delivery

### Alpha 1: isolated foundation

- Branch and installer flow.
- ADR-002 host-ingress direction plus clean-host confinement, IPC/UID/LSM and Podman/Quadlet implementation proofs.
- Four separately confined containers and one confined non-root host ingress role under the documented trust model.
- Rootless Podman only, with runtime's full-engine authority explicitly tested.
- One host binary and one digest-pinned component image built from one distribution identity.
- Four role-specific serve modes from that image.
- A locked, journaled, idempotent host installer limited to fresh install and same-generation recovery.
- Atomic Quadlet and ingress-service generation with an installation target managed by the host binary.
- Live TCP handoff, bounded UDP relay, effective withdrawal and automatic ingress/edge recovery tests; no firewall mutation.
- Identity, readiness, lingering, partial-failure, and clean Ubuntu 26.04 bootstrap tests.
- Private Unix sockets and SSH administration.
- Socket recreation, startup-order, ownership, mode, mount, and SELinux tests.
- Status and ownership tests proving only runtime sees Podman.

### Alpha 2: minimal web app

- App manifest and globally unique name.
- Configuration-only apply.
- Explicit deploy with digest-pinned images from an external registry and a minimal immutable release.
- Persistence, reservation, edge-snapshot, and workload-recovery ADRs covering the first deploy, not deferred until registry support.
- One service, private app network, separate per-app ingress network, HTTP entrypoint, HTTP route.
- End-to-end edge traffic and negative network-access tests.
- Durable execution intent, minimal stop, and interrupted-deploy/stop recovery.
- Component restart and host reboot tests proving the active release is recovered without activating pending configuration.

### Alpha 3: multi-service security

- Multiple services.
- Global public environment.
- Service-scoped write-only secrets.
- Service-owned volumes.
- Named private networks across apps.
- Multi-service ingress isolation and volume-safe failure recovery.

### Alpha 4: registry and rollback

- Certificate-lifecycle ADR and proof against registry identity impersonation by a compromised edge.
- Public authenticated OCI registry through raw SNI passthrough.
- Extend the existing digest-pinned release flow to Gordon-hosted images.
- Push deploy and opt-in auto-deploy.
- Durable registry outbox.
- Full and service-level rollback.

### Alpha 5: routes and lifecycle

- HTTP, TCP, and UDP routes.
- Rollout ADR establishing concurrency-safe eligibility and bounded connection draining before web replacement is enabled.
- Restart, remove, and purge; extend the existing stop/recovery flow to all supported protocols.
- Complete multi-service and multi-protocol failure-matrix tests.
- End-to-end security and recovery checks.

Every commit on `v3-alpha` must remain buildable and testable. Every alpha stage must install on a clean host through the same installer and clearly report unsupported features.

## Remaining implementation details

The following details are intentionally left to focused implementation ADRs, provided they preserve this design:

- AppSpec/release persistence, effective configuration identity, execution intent, and app-operation serialization before Alpha 2;
- route reservations and edge snapshot publication/acknowledgement under concurrent app operations before Alpha 2;
- workload startup ownership and reboot/interruption recovery before Alpha 2;
- exact HTTP DTOs and streaming format;
- Unix-socket directory layout and Quadlet UID mappings;
- named shared-network declaration syntax;
- certificate issuance, storage, renewal, and client trust preventing registry impersonation by edge before public registry access;
- bounded event-outbox limits, ordering, deduplication, stale-event handling, and retry schedule before auto-deploy;
- concurrency-safe web replacement eligibility, multi-entrypoint checks, and bounded draining before Alpha 5;
- release retention and garbage-collection defaults;
- lifecycle states, component replacement order, persistent-format compatibility, backup, rollback or roll-forward, and interrupted-update recovery before any component update support.
