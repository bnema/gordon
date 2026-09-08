# AGENTS.md — Gordon v3-alpha

Guidance for coding agents working on the `v3-alpha` integration branch and branches based on it.

## Project

Gordon v3 is a fresh-install, single-host container deployment platform with four isolated rootless Podman components. Edge is the merged traffic-plane container published by ordinary rootless Podman `-p` executed by runtime; there is no host ingress process and no relay IPC (ADR-004).

- Module: `github.com/bnema/gordon`
- Language: Go 1.27
- Reference host for Alpha 1: Ubuntu 26.04 LTS
- Runtime: rootless Podman only
- Service manager: `systemd --user` with Quadlet
- Database policy: no external database; private bbolt for control and a separate encrypted bbolt secret store for runtime

Read these before changing v3 behavior:

- `docs/v3/design.md` — accepted product and architecture baseline
- `docs/v3/adr-001-v3-foundation.md` — foundation decision and risks, as amended
- `docs/v3/adr-002-host-ingress.md` — superseded host-ingress fallback and historical proof evidence
- `docs/v3/adr-003-alpha-scope-and-trust.md` — current trust, storage, shutdown and rootless decisions; takes precedence over conflicting earlier ADR text except topology, where ADR-004 wins
- `docs/v3/adr-004-merged-edge.md` — accepted four-container merged-edge topology with runtime-owned publication
- `docs/v3/plans/README.md` — phased execution, starting from the resolved A1A.0/A1A.1 proofs

Those documents are normative. The repository still contains v2 code inherited from `main`; existing code is not evidence that a v2 behavior belongs in v3.

## Branch and PR workflow

`v3-alpha` is the long-lived v3 integration branch. **Never open a v3-alpha feature PR against `main`.**

1. Start every change from the latest `origin/v3-alpha`.
2. Create a focused branch and worktree under `.worktrees/<branch-name>`.
3. Keep each commit buildable and testable.
4. Use signed conventional commits: `type(scope): description`.
5. Push the feature branch and open a **Draft PR targeting `v3-alpha`**.
6. Remove the feature worktree and branch after merge or abandonment.

Do not commit directly to `v3-alpha`. Do not merge v3 into `main` until the maintainer explicitly starts that release process.

## V3 invariants

### Distribution and component lifecycle

One Gordon distribution has:

- one host executable;
- one OCI component image containing an octet-identical copy of that executable;
- four container serve modes: `control`, `runtime`, `edge`, and `registry` (exact syntax not yet fixed);
- one distribution identity binding source/version, executable SHA-256, OCI image digest, and persistent-format versions.

The host executable owns installation state and four generated Quadlets. Systemd supervises all four roles; Podman runs the four containers. Neither edge nor runtime is a supervisor and neither can launch components outside its owned scope or manage Podman/systemd beyond runtime's owned publication and workload operations. Distribution identity, checksums, readiness and recovery cover all four roles.

Alpha 1 supports fresh installation and same-generation recovery only. V3 component update and rollback are unavailable until a focused lifecycle ADR is accepted and implemented. Do not add an alpha channel to `gordon update`.

### Trust boundaries

Control, runtime, edge and registry are independent rootless containers, never one shared Podman pod.

- `control` owns desired state, AppSpecs, app releases, routes, publication reservations, and secret metadata.
- `runtime` alone receives the Podman socket; it owns workload mutation, actual state, volumes, stored secret values, and edge publication executed from control reservations.
- `edge` owns the merged traffic plane: published listeners, application and explicitly public registry TLS/routing, backend connections and client policy. It receives a sanitized route projection, never Podman/admin capabilities or private stores. Peers it observes are NAT-rewritten; trusted HTTP identity comes only from restricted upstream conveyance.
- `registry` owns OCI storage, authentication, private runtime pulls and a bounded push-event outbox. It is private by default and published only by explicit system-domain configuration, never an app lifecycle resource.

Edge is trusted but fallible: compromise can expose/alter terminated traffic and OCI credentials, including indirect workload/secret compromise through poisoned images and deploy. Do not restore the superseded registry-confidentiality or edge-impersonation proof gate. Keep direct app/edge isolation from Podman, administrative capabilities and private state; shared-kernel/host/runtime compromise is not contained by a VM-strength guarantee.

Runtime's Podman socket grants authority over the entire rootless engine, including Gordon's containers. This is an accepted alpha risk, not a strong isolation claim.

Ordinary control APIs use strict HTTP/JSON over role-specific Unix sockets. Control authorizes publication mappings; runtime executes them; edge receives the resulting configuration and cannot authorize new binds. Do not introduce internal TCP APIs, gRPC, protobuf, generic RPC or bearer-token plumbing.

All Gordon setup must remain unprivileged and use rootless Podman/user systemd. No required dedicated system account, system service or privileged installer step. The administrator owns firewalld/equivalent, privileged-port redirections and any privileged host prerequisite such as lingering. Gordon validates/reports prerequisites; it must not mutate firewall rules or host sysctls. App route listeners are not duplicated in an installation-level port catalogue.

Edge is the public attack surface: an ordinary self-hosted-proxy exposure. Non-root and `NoNewPrivileges` alone do not isolate a host process, but no Gordon host process remains; container denial proofs replace the moot host-confinement gates. Do not relax containment to accommodate a prototype.

For all Gordon and app containers:

- never use Docker or rootful Podman;
- never add `--privileged`, host PID/IPC/network, or host devices;
- never mount the Podman socket outside `runtime`;
- use no added Linux capabilities, `no-new-privileges`, and a read-only root filesystem where practical;
- preserve SELinux/AppArmor, seccomp, user-namespace, resource-limit, and least-network-access protections.

App manifests never receive host bind mounts. Gordon component mounts are limited to their owned data, runtime's separately stored read-only secret master key, and explicit capability-socket directories documented by the design and socket/storage contracts.

Gordon-owned containers, networks, volumes, and labels are reserved. Workload reconciliation and garbage collection must not mutate them. The only accepted exception is runtime's narrow, idempotent reconciliation of edge publication and app-ingress network attachments.

### Product model

The public model is:

```text
app -> services -> runtime containers
```

- One standalone TOML manifest defines one globally named app.
- `gordon apps apply --file ...` persists desired configuration only; it never mutates runtime.
- `gordon deploy <app>` is the only operation that activates a pending AppSpec.
- Runtime receives digest-pinned OCI references only.
- Entrypoints describe service interfaces; routes are the only public exposure primitive. Reserve hosts and listeners across desired, active, and in-flight state, including during rollback. Activation requires runtime/edge readiness. Edge acknowledges route withdrawal on shared listeners; dedicated/final shared-listener withdrawal additionally requires verified runtime mapping removal and bounded transport cleanup.
- Public environment is app-wide only. All service-specific values use write-only secrets, even when non-confidential; reject public-environment/secret name collisions.
- Secrets are write-only and service-owned; rollback uses current values, not historical ones. Runtime encrypts values before storing them in private bbolt; its random key is in a separate private directory mounted read-only only to runtime. Missing/wrong key for an existing store fails closed, never regenerates or clears data. This protects a database copy alone, not runtime/host compromise, injected environments or backups with the key.
- Control uses its own bbolt database for revisions/releases/intent/operations/reservations/metadata/tombstones. No SQL/migration framework in alpha; version formats and reject incompatible ones. Separate-role transactions do not make external effects atomic.
- HTTP/HTTPS-only services without persistent volumes use start/check/switch/stop overlap in Alpha 5; no concurrency declaration/classifier. The app owns overlap and shutdown safety. Others recreate with interruption. `stop_timeout` is per service, defaults to `30s`, allows finite positive overrides, and ends in force kill if needed. Keep target/deadline recovery and bounded stream cleanup; a signal is not proof of drain.
- Image labels provide no defaults/overrides, probes, routing, deployment or alpha display metadata. Gordon management identity cannot be supplied or overridden by inherited labels.
- Releases record effective configuration and provenance. Service-targeted deploy and push/auto-deploy require matching desired/active source revisions and effective configuration, including after synthetic rollback.
- Execution intent is durable and separate from releases. Stopped apps stay stopped after reboot and queued events; only successful full-deploy activation changes their durable intent to running. Interrupted deploy follows its journal, not generic resurrection of a prior release. Restart uses the active release and current secrets.
- Volumes are named Podman volumes owned by one service; no host bind mounts or shared service volumes.
- Each app has a private network; edge joins only generated ingress networks for routed services.
- UDP uses recreate with interruption and bounded in-memory edge associations. Stop admission and invalidate affected per-listener epochs before backend replacement; reopen only after readiness with fresh epochs and reject stale replies, including across restart. No live session migration or session restoration after edge restart or reboot. Recover only authorized listeners/routes, with empty UDP sessions.
- Edge failure interrupts its TCP connections and loses UDP sessions. Do not promise transparent edge restart.

Do not restore v2 route-owned containers, implicit deploy-on-apply, mutable runtime tags, global secret sharing, or ad hoc route mutations.

## Scope discipline

V3 deliberately has no v2 compatibility, migration path, Docker support, cluster mode, shared Gordon pod, or alpha feature-parity requirement.

Do not copy or cherry-pick the archived `v3-deprecated` implementation wholesale. Reimplement only a clearly applicable invariant or test case, using the smallest design that fits the accepted v3 model.

Before introducing a new architecture, security, interface, migration, or rollout decision:

1. verify whether the design or an ADR already decides it;
2. ask the maintainer clear questions one at a time;
3. record consequential decisions in a focused ADR;
4. run a critical senior/red-team pass before implementation.

Prefer standard-library and native Podman/systemd mechanisms over new dependencies. Avoid speculative abstractions and deferred feature scaffolding.

## Alpha delivery order

A1A.0 proved packaged-stack native publication NAT-rewrites source with no pasta forwarder/Pesto available; the maintainer abandoned the native path. A1A.1 proved no same-account host-process confinement mechanism works. ADR-004 resolves the topology: merged edge container with runtime-owned publication, no host ingress process, no relay IPC. Edge-wide interruption on published-port-set changes is accepted as a documented alpha limit. Do not revive the native path, the host relay, or any parallel path.

For the merged-edge topology, Alpha 1 is blocked until contracts and clean-host proofs establish:

1. runtime-owned publication for `80/443`, dedicated TCP/UDP, documented NAT limits with trusted-proxy HTTP identity only from restricted upstream conveyance, firewall behavior, edge restarts, and private runtime-to-registry pulls;
2. Unix-socket paths, UID/GID mappings, ownership, modes, directory mounts, recreation, startup ordering, and SELinux/AppArmor behavior;
3. publication readiness/independent withdrawal/stale-bind cleanup, interrupted-operation recovery, bounded bidirectional UDP sessions with epoch rejection and disruptive recreate/restart behavior before UDP exposure. Request/response prototypes and manual replay are not production recovery proofs.

Implement Alpha 1 incrementally after those proofs:

1. four minimal container serve modes with the merged edge traffic plane;
2. distribution identity and role readiness;
3. one digest-pinned component image;
4. branch, commit, and local installer inputs;
5. locked, journaled, idempotent host installation;
6. atomic Quadlet generation and systemd target;
7. private sockets and SSH administration;
8. clean Ubuntu 26.04 installation, reboot, authority, and failure tests.

Do not start Alpha 2 workload features until Alpha 1 installs and passes on a clean reference host. Alpha 2 must already include minimal digest-pinned immutable releases, separate ingress networks, durable execution intent, stop, and interruption/reboot tests. Persistence, reservation, edge-snapshot, and workload-recovery ADRs precede those features; do not defer their invariants to Alpha 4.

Before public endpoints, specify supported TLS modes, certificate ownership/renewal and proxy-origin/client trust; these details are still open, not a reason to restore registry isolation from trusted edge. Before Alpha 5 overlap, define multi-entrypoint TCP checks, switch/signal/deadline/stream cleanup and crash recovery; do not add a concurrency eligibility mechanism. Do not automatically restore an older volume-owning service after a replacement may have written its data.

Native CrowdSec integration is deferred beyond alpha. Optional administrator-managed upstream protection is not a required or assumed security baseline.

## Architecture and Go style

Keep dependency flow inward:

```text
adapters -> boundaries/ports -> use cases -> domain
```

- Domain and use cases must not import concrete adapters.
- Put cross-layer interfaces at the consumer-owned boundary.
- Keep transport DTOs out of domain types.
- Keep role composition in the application/bootstrap layer.
- Treat current v2 package and CLI patterns as reusable only when they fit v3 ownership.

Imports use three groups: standard library, external modules, then Gordon modules.

Wrap errors with actionable context and preserve causes with `%w`. Use `errors.Is` for sentinel errors. CLI commands return errors and write through Cobra's configured output; do not print directly to global stdout.

Use `zerowrap` for structured component logs. Never log secret values, credentials, complete untrusted payloads, or internal capability paths unnecessarily.

## Commands and checks

```bash
# Build
go build ./...

# Full test suite
go test ./...

# Required race suite when practical
go test ./... -race

# Narrow package or test
go test ./internal/usecase/container/...
go test ./path/to/package -run '^TestName$' -v

# Generate boundary mocks after interface changes
mockery

# Mandatory before every code commit
golangci-lint run ./...
```

Use table-driven tests by default. Prefer deterministic fakes over sleeps. Test security boundaries negatively: prove forbidden sockets, mounts, networks, capabilities, and secret reads are absent.

For documentation-only commits, run at minimum `git diff --check` and verify Markdown fences. Code commits must pass `golangci-lint run ./...` before commit, even when narrower tests already pass.

## Definition of done

Before opening or updating a Draft PR:

- behavior matches `docs/v3/design.md` and accepted ADRs;
- the change is scoped to one incremental outcome;
- tests cover success, failure, recovery, and relevant authority boundaries;
- generated mocks and docs are current;
- `golangci-lint run ./...` passes for code changes;
- commits are signed and conventional;
- the PR targets `v3-alpha`, not `main`;
- remaining uncertainty and deferred behavior are stated explicitly.
