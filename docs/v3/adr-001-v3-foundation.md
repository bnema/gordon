# ADR-001: Rebuild Gordon v3 around isolated local components and declarative apps

- Status: Accepted; not yet implemented
- Date: 2026-09-04
- Decision owners: Gordon maintainers
- Related: [V3 design](./design.md), issue #245, PR #244
- Supersedes: the implementation archived on branch `v3-deprecated`

## Context

Gordon v2 runs registry, reverse proxy, administration, secret handling, and container-runtime access in one process and under one host identity. A remote-code-execution vulnerability in any Internet-facing path can therefore expose every file readable by that identity, application secrets, and the Docker- or Podman-compatible runtime socket.

The first v3 implementation attempted to address this by splitting Gordon into `control`, `runtime`, `edge`, and `registry` roles. It combined that security boundary with gRPC and protobuf contracts, component tokens, distributed snapshots, durable events, a compatibility harness, automatic component launching, checkpointed migration, zero-downtime cutover, and runtime handoff.

That branch grew to hundreds of divergent commits and more than eighty thousand added lines across 424 changed files. It builds, but the complete deployed system proved difficult to operate, debug, and maintain. At the same time, Gordon's public workload model remained route-centric and was being redesigned separately in issue #245 and PR #244.

A second big-bang refactor would repeat the same failure. Keeping the v2 monolith would leave the primary security risk unresolved.

## Decision drivers

1. A compromise of edge or registry must not expose application secrets or the Podman socket.
2. V3 should be substantially simpler than the archived implementation.
3. Configuration ownership must be explicit and deterministic.
4. Public routes must not own workload lifecycle.
5. Runtime deployments must use immutable OCI identities.
6. The system must remain usable when one control component restarts.
7. Every incremental change on `v3-alpha` must leave an installable, testable system.
8. One Gordon distribution must install and maintain one coherent generation of the host CLI and all four components.
9. V3 may break compatibility and omit v2 features rather than preserve accidental complexity.

## Decision

### Start again from main

The former `v3` branch is archived as `v3-deprecated`. The new `v3-alpha` branch starts from `main`. Code is not merged or cherry-picked wholesale from the archived implementation. Individual invariants and test scenarios may be reimplemented when they fit the new design.

V3 is fresh-install only. It does not parse v2 configuration, preserve v2 CLI aliases, or provide automatic, live, or offline v2 migration.

### Use four isolated containers on one host

Gordon runs `control`, `runtime`, `edge`, and `registry` as separate rootless Podman containers with distinct container namespaces, mounts, networks, and data ownership. They do not share a Podman pod.

All components initially run under one trusted host account and rootless Podman engine; container UIDs are not separate host security principals. Only runtime receives the rootless Podman socket, which grants full authority over that engine, including Gordon's containers. Host-account or runtime compromise defeats the component boundaries and is an accepted alpha risk. Docker, rootful engines, multi-host operation, and clustering are outside v3 scope.

### Ship one distribution as one host binary and one component image

One Gordon distribution provides the host CLI and one component image containing an octet-identical copy of that Gordon executable. The image invokes that executable in four role-specific serve modes: `control`, `runtime`, `edge`, and `registry`. The exact command syntax is an Alpha 1 implementation detail; separate role binaries and independently versioned component images are not allowed. "Distribution" and "installation generation" identify Gordon itself; "release" remains reserved for immutable app deployments.

A distribution identity binds its version or source revision, executable SHA-256, component-image OCI digest, and persistent-format versions. The build copies that exact executable into the image and verifies its hash before publication. Generated Quadlets pin the image digest. Tagged distributions publish a signed manifest attesting the executable hash and image digest. Before executing a downloaded Gordon binary, the shell bootstrap verifies that signature with a pinned release public key and verifies the executable hash. Initial `curl | sh` delivery still trusts HTTPS; an independent root of trust requires obtaining and verifying the installer through a separately trusted channel. Branch and commit modes are unauthenticated alpha inputs attributable to one exact clean revision. Dirty local builds include a source-tree hash in their identity rather than reporting clean `HEAD`.

The installed host binary owns the declarative lifecycle of those four Gordon components. It provisions their Quadlets, asks the user systemd manager to perform lifecycle operations, and verifies process state, role readiness, dependencies, and distribution identity. A future update may temporarily run mixed generations only in an explicit transitional state that is never reported healthy. Alpha 1 is limited to fresh installation and recovery of the same incomplete generation; installing a different generation, update, and component rollback remain blocked on the lifecycle ADR.

Responsibility is deliberately layered:

- Gordon owns the intended distribution identity, configuration, generated Quadlets, installation lock and journal, and installation-level operations;
- Quadlet translates the Podman declarations into systemd units;
- systemd owns boot activation, dependency ordering, continuous supervision, and restart-after-failure;
- Podman creates and runs the four rootless containers.

The shell installer only obtains and verifies or builds the host binary, then invokes Gordon's host-side installer. Gordon permits one installation per host account and rootless engine and performs atomic, locked, journaled, idempotent steps so an interrupted install can resume. Generated Quadlets are checksummed Gordon outputs; supported customization belongs in `gordon.toml`, and unknown edits are preserved and require an explicit recovery operation. The installer configures and tests the user-manager lingering policy needed for boot without login.

Gordon is not a fifth resident supervisor. It does not reimplement systemd or restore the archived checkpointed migration, runtime handoff, or autonomous self-upgrade machinery. Component update support requires a focused lifecycle ADR defining states, replacement order, persistent-format compatibility, backup, rollback or roll-forward, and interruption recovery.

### Use local capability sockets

Components communicate through role-specific Unix sockets using HTTP and strict JSON DTOs. Each socket exposes only the operations needed by its mounted consumer. Internal gRPC, protobuf, bearer tokens, and generic component RPC surfaces are not used.

Control opens no TCP administration port by default. Local clients use its Unix socket. Remote clients forward that socket through SSH. A public HTTPS control endpoint is deferred pending a separate threat-model ADR; enabling one would make control Internet-facing and explicitly forfeit the primary containment guarantee. Plaintext HTTP is unsupported.

### Separate public components from the trusted core

Edge terminates public application HTTP/HTTPS. It receives only a sanitized, generation-controlled routing projection. It has no app secrets, Podman access, or desired-state mutation API.

Registry is publicly reachable for authenticated OCI operations. Edge selects registry traffic by SNI and forwards the encrypted TCP stream without terminating TLS. Registry owns its TLS identity and a durable push-event outbox, but has no general configuration or runtime API. Auto-deploy deliberately grants registry bounded authority to trigger a release for one configured repository-and-tag target. Without artifact verification rooted outside registry, registry compromise can deploy arbitrary content to that opted-in service; this is accepted for alpha.

Control and runtime form the trusted core. Control owns desired state and deployment decisions. Runtime owns actual Podman state and persisted secret values.

### Replace route-owned workloads with declarative apps

Each app is defined by one user-owned TOML manifest and has a globally unique name. An app contains named services, app-wide public environment, named entrypoints, and routes. A container is only a runtime instance.

Every service is explicitly named, references one image, and has one active instance. There is no app-level image shorthand or implicit default service. Compact and expanded syntax are both accepted but normalize to one model.

An app has an automatic private network. Named private networks allow explicitly selected services from different apps to communicate. The intended public-routing topology adds an automatic per-app ingress network containing edge and only routed services. Gordon component resources use reserved names and ownership labels and are excluded from workload reconciliation and garbage collection. Runtime's narrow, idempotent reconciliation of edge's app-ingress attachments is the only allowed workload-side component mutation; Quadlet remains responsible for creating and restarting edge. Alpha 1 is blocked on a clean-host ADR and proof covering this exception, rootless host ingress, source-IP preservation, privileged ports, dynamic TCP/UDP listeners, socket permissions, UID mappings, and SELinux labels.

App entrypoints describe stable service interfaces. Declaring one does not expose it. Routes are the only exposure primitive and support HTTP, TCP, and UDP. HTTP uses edge's shared listener; dedicated TCP and UDP routes contain their own listen address.

### Separate configuration from runtime mutation

`gordon apps apply --file <manifest>` replaces the complete desired AppSpec after normalization and validation. It only persists configuration. It never mutates containers, networks, volumes, routes, or images. V3-alpha deliberately uses last-successful-write-wins for complete manifests and returns both former and resulting revisions.

`gordon deploy <app>` is the only command that activates a new AppSpec revision. It resolves image selectors to OCI digests, creates an immutable release, applies runtime changes, and publishes the release's routes through persisted `prepared`, `mutating`, `routes-published`, and terminal phases. Activation requires observed runtime state and edge acknowledgement; restart recovery observes before acting and never blindly replays mutations.

`gordon deploy <app> --service <name>` is allowed only when desired and active AppSpec revisions match and creates a composed release changing that service digest only. `push --deploy` and auto-deploy follow the same restriction and cannot implicitly activate pending configuration. Auto-deploy selectors must be unique installation-wide; ambiguous push targets require explicit app and service selection.

### Use immutable releases and scoped rollback

Runtime receives only digest-pinned OCI references. Unqualified image references target Gordon's registry; external references require a full registry hostname. The literal tag `latest` is rejected with no alpha override.

A release combines an immutable normalized AppSpec with exact service digests. Full rollback and service-level rollback both create new releases; historical releases are never mutated. A service rollback uses the currently active app-level environment, entrypoints, routes, networks, and other services, and substitutes only the selected former service definition and digest. The synthetic release must pass complete validation before mutation.

### Minimize secret and storage authority

Public environment values are app-wide and injected into every service. There is no service-local public environment. Values intended for one service use write-only secrets, even if not confidential.

Secrets are identified by app, service, and name. They cannot be read through any Gordon API or shared by reference between services. Control handles values transiently during set/update; runtime persists and injects them as same-name environment variables. This prevents direct API disclosure but does not protect service secrets from a compromised control plane authorized to deploy that service. Secret changes affect only a later deploy/restart, missing declared secrets block deployment, and removal of a desired or active reference is rejected.

App manifests allow only service-owned Podman named volumes. Host bind mounts and cross-service shared volumes are forbidden. `stop` preserves all durable state. `remove` deletes app metadata and secrets but preserves volumes plus a tombstone that reserves the app name and prevents implicit data adoption. `purge` also deletes volumes and the tombstone after explicit confirmation. Registry images have an independent lifecycle. Runtime pulls them through a separate private registry endpoint with pull-only credentials, not through public edge passthrough; the exact rootless endpoint is part of the Alpha 1 ingress proof.

### Limit zero-downtime behavior

Deployment strategy is inferred and not configurable. Only stateless services exclusively routed through HTTP/HTTPS and without persistent volumes are eligible for replacement without a transport outage. Gordon starts the new instance, waits for Podman `running` and an accepting HTTP backend TCP port, switches edge routes, and removes the old instance.

All stateful, worker, TCP, UDP, RCON, mixed-protocol, and volume-owning services use recreate and may experience downtime. V3 has no readiness configuration and does not interpret OCI `HEALTHCHECK`.

### Reduce the CLI and feature set

V3-alpha does not promise v2 parity. Declarative app operations live under `apps`; frequent runtime actions such as `deploy`, `rollback`, `restart`, `stop`, `remove`, `purge`, `logs`, `status`, and `push` remain top-level.

Attachments, autoroute, bootstrap, pin, reload, partial route mutations, and token-printing commands are removed. Backups, previews, CA/TLS inspection, traffic diagnostics, and resource garbage collection are deferred until redesigned around v3 ownership.

### Install alpha from source references

Stable installation remains signed-distribution- and version-based. The future v3 `gordon update` will accept only a verified signed distribution manifest, but the command is unavailable until the lifecycle ADR is accepted and implemented. Alpha testing uses `install.sh` with one mutually exclusive source selector:

```text
GORDON_BRANCH=v3-alpha
GORDON_COMMIT=<sha>
GORDON_LOCAL=1
GORDON_VERSION=<tag>
```

Alpha 1 will implement branch, commit, and local modes that derive one explicit distribution identity, build both the host executable and digest-pinned component image from it, and invoke that executable to provision installation directories and Quadlets, start an installation target through the user systemd manager, and verify all four roles. The executable copied into the image must have the same SHA-256 as the host executable. Installation is locked, journaled, idempotent, and limited to a clean host or same-generation recovery. Branch, commit, and local inputs are not authenticated releases. The shell remains bootstrap transport only; after installation, Gordon owns installation-level operations while systemd supervises the containers. These modes do not exist in the current installer and do not introduce an alpha update channel.

## Consequences

### Positive

- An edge or registry compromise no longer directly grants Podman or secret-store access.
- The trust model is enforced by OS/container boundaries and mounted capabilities, not package conventions alone.
- Mono-host Unix sockets eliminate most distributed-system and internal TLS complexity.
- The app manifest is a portable, Git-friendly source of truth.
- Apply and deploy have distinct, predictable effects.
- Routes no longer own containers.
- Digest-pinned releases provide reproducible deployment and rollback.
- Per-service secrets and volumes reduce workload blast radius.
- The smaller CLI and fresh-start policy permit substantial code deletion.
- Incremental alpha milestones can be tested on real clean installations.

### Negative

- V3 cannot upgrade an existing v2 installation in place.
- Docker users must migrate to rootless Podman.
- Four containers and Quadlets are operationally heavier than one host process.
- Installation and future update must coordinate a host binary, a component image, four units, persistent formats, and partial-failure recovery while preserving one distribution identity.
- The initial single host account and engine do not isolate Gordon components from host-account or runtime compromise.
- Registry availability depends on edge's public TCP mux on a single-IP installation.
- Control and runtime remain a trusted core; a full control compromise may indirectly request harmful runtime actions.
- Runtime compromise remains severe because it owns Podman and injected secret values.
- Service-local non-secret values must use the write-only secret workflow.
- Shared filesystems and host bind mounts are unavailable to app manifests.
- Zero downtime is intentionally limited to eligible web frontends.
- Several v2 features will be absent during alpha and may never return.

### Operational

- The installed Gordon binary owns generated Quadlets and must detect local edits or generation drift instead of silently accepting an incoherent installation.
- Installation status must distinguish intended, staged, running, degraded, and rollback-required distribution states.
- User-manager lingering must be configured and tested so components start after reboot without an interactive login.
- Component data and socket permissions require explicit installer and Quadlet tests.
- Edge must preserve its last valid sanitized snapshot so a control restart does not interrupt traffic.
- Registry must persist and retry push events while control is unavailable.
- Runtime must reconstruct actual state from Podman after restart rather than trust stale process memory.
- Deployment status must distinguish desired AppSpec, active release, and observed runtime state.

## Alternatives considered

### Keep the v2 monolith and only reorganize packages

Rejected. Package boundaries do not stop code execution under one UID from reading secrets or using the runtime socket.

### Repair and continue the archived v3 implementation

Rejected. It combines too many independently risky changes and encodes workload and migration assumptions that the new app model supersedes.

### Run all Gordon containers in one Podman pod

Rejected. Shared networking increases lateral reach from edge to trusted components and weakens role-specific network policy.

### Split components across hosts with network gRPC

Rejected for v3. Gordon is a single-server deployment platform. Multi-host RPC, TLS, discovery, compatibility, and recovery are unnecessary complexity.

### Put registry behind HTTP termination at edge

Rejected. A compromised edge could capture OCI credentials and payloads. Raw SNI passthrough preserves registry's independent TLS boundary.

### Keep registry private and proxy every image through control

Rejected. It removes standard OCI push workflows, makes control a large-blob data path, and increases control's exposed parsing and resource surface.

### Require a VPN for registry access

Rejected as the default because it makes common CI push workflows cumbersome. A private registry remains an optional hardening choice.

### Use automatic app deployment during apply

Rejected. It makes configuration synchronization mutate runtime, couples validation to registry availability, and makes CI effects harder to predict.

### Use tags directly at runtime

Rejected. Tags are mutable. Every release records and deploys immutable OCI digests.

### Support arbitrary health checks and deployment strategies

Rejected for initial v3. Multiple probe and rollout engines substantially increase behavior and test surface. V3 keeps one narrow web replacement algorithm.

### Preserve full v2 feature parity before alpha

Rejected. It would force obsolete ownership and command models into the new architecture and recreate a big-bang rewrite.

## Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Compromised registry causes malicious image deployment | Accept bounded auto-deploy authority only for unique configured targets; runtime enforces no host binds/capabilities; future signature policy may add stronger provenance |
| Compromised edge attacks app internals | Edge joins only generated ingress networks and only routed services join those networks |
| Socket mount accidentally grants excessive API access | Separate directories and handlers per capability; installer ownership tests; no generic RPC socket |
| Stale edge routing after control failure | Persist the last valid monotonic snapshot; reject invalid or older generations; expose applied generation in status |
| Apply overwrites another operator's desired state | Complete-manifest semantics and future optimistic-concurrency details in a focused API ADR |
| Failed stateful deployment causes downtime | Do not promise zero downtime; retain immutable former release and perform bounded best-effort recovery |
| Alpha installer silently moves branch state | Resolve and report exact commit; support `GORDON_COMMIT` for reproduction; include a source-tree hash for dirty local builds |
| Version text identifies different artifacts | Bind the executable hash, OCI digest, and format versions in a distribution identity; verify the same executable is copied into the image; pin image digests; sign tagged distribution manifests |
| Partial update leaves mixed component generations | Limit Alpha 1 to fresh install; never report transitional mixed state healthy; require a lifecycle ADR before update support |
| Component rollback meets incompatible persisted data | Version formats and require the lifecycle ADR to define preflight, backup, reversible migration, and roll-forward policy |
| Workload reconciliation mutates Gordon resources | Reserve names and ownership labels; exclude component resources; keep edge attachment reconciliation narrow and idempotent |
| Generated Quadlets drift from Gordon's intended state | Record checksums and generation; fail clearly before overwriting unrecognized local changes and preserve a backup |
| Deferred v2 features are forgotten | Keep the feature tracking matrix in `docs/v3/design.md` and require explicit ownership before reintroduction |

## Validation requirements

The decision is considered implemented only when automated tests prove:

1. Edge and registry cannot see the Podman socket or application-secret storage.
2. Control cannot retrieve persisted secret values through its runtime API.
3. Components expose only their role-specific Unix-socket handlers.
4. Control restart does not interrupt already routed applications.
5. Registry queues and retries authenticated push events.
6. Apply performs no runtime mutation.
7. Runtime only receives digest-pinned image references.
8. An invalid deployment leaves the former route projection active where the deployment strategy permits.
9. A service rollback does not restart unrelated services.
10. App removal preserves volumes and purge requires explicit confirmation.
11. Artifacts build with Go 1.27, and the branch installer provisions a clean Ubuntu 26.04 LTS rootless Podman host from an exact commit while refusing to replace another generation.
12. The distribution identity binds source/version, executable hash, component-image digest, and persistent-format versions; before executing a tagged binary, the shell verifies its hash against a manifest signature rooted in a pinned release key.
13. The installed host executable and its octet-identical copy in the image match that identity, and generated Quadlets pin one image digest in four distinct serve roles with only their documented capabilities.
14. The installer lock prevents concurrent mutation; an interrupted Alpha 1 installation persists an incomplete state and can resume the same generation idempotently.
15. Process running, role ready, dependencies ready, and identity verified are distinguishable; mixed generations are never reported healthy.
16. User-manager lingering starts the installation after reboot without an interactive login.
17. Workload reconciliation cannot mutate or garbage-collect Gordon-owned resources except for the narrow tested edge-network attachment operation.
18. The selected host-ingress mechanism preserves required source addresses and supports the documented HTTP, registry SNI, TCP, and UDP listeners under rootless Podman.
19. Socket directories survive component restart with exact ownership, mode, mount, and SELinux behavior.

## Follow-up decisions

Focused ADRs should define, when implementation reaches them:

- AppSpec and release persistence;
- internal HTTP DTO versioning;
- Unix-socket filesystem and UID mapping, which is a prerequisite for Alpha 1 rather than a deferrable implementation detail;
- rootless host ingress and private runtime-to-registry pulls, also prerequisites for Alpha 1;
- edge snapshot generation and acknowledgement;
- registry event outbox semantics;
- shared named-network declaration;
- certificate lifecycle;
- lifecycle states, component replacement order, persistent-format compatibility, backup, rollback or roll-forward, interruption recovery, and generated-Quadlet drift policy before any component update support;
- release and orphan retention policies.
