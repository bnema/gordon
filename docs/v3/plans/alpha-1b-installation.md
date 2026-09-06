# Alpha 1B: installable isolated foundation

Status: planned; N0 must resolve before ingress work, followed by applicable F1–F3 contracts/proofs.

Five-role/relay/ingress-service tasks below are the conditional fallback only. On native success, remove those tasks and update publication/ownership/readiness contracts to the proven four-container topology before implementation, as required by ADR-003. Do not build both paths.

## Context and scope

Use the [shared baseline and checks](README.md) and [Alpha 1A outputs](alpha-1a-foundation-proofs.md). Outcome: clean Ubuntu 26.04 installation of one coherent distribution, five supervised roles, private administration, validated isolation and same-generation interrupted-install recovery. No app deployment or component update.

Existing anchors: `main.go`, `internal/adapters/in/cli/{root,serve,controlplane_local,controlplane_resolver}.go`, `internal/app/{run,kernel}.go`, `Dockerfile`, `install.sh`, `.goreleaser.yaml`, `.github/workflows/ci.yml`, `pkg/version/`.

Proposed new work locations, created only as needed: `internal/app/roles/`, `internal/usecase/installation/`, `internal/adapters/out/{podman,systemd}/`, `internal/adapters/in/http/roleapi/`, `internal/adapters/in/ingress/`, `internal/adapters/dto/v3/`, corresponding consumer ports and domain identity types. Accepted F1–F3 contracts decide concrete names and schemas; do not invent a parallel generic RPC framework.

## Tasks

- [ ] **A1B.1 — Replace v2 dispatch with minimal role composition** — depends on: F1/F3 accepted.
  - Keep `main.go` as the single executable entry. Rebuild `cli.NewRootCmd` and `newServeCmd` around the four container role modes and accepted host ingress/installer syntax. Compose each role in the app layer with only its dependencies.
  - Remove legacy command registration, default monolithic `app.Run` dispatch, local `NewKernel` construction and v2 config fallback from reachable v3 paths. Retain version/completion/help and only implemented alpha operations. Unsupported functionality must fail explicitly, never execute v2 behavior.
  - Initial control/edge/registry roles may expose only their implemented readiness/private contracts; no public registry until R1. Runtime alone constructs the Podman client. Ingress cannot construct Podman/systemd/secret dependencies.
  - Test unknown/missing role, wrong role configuration, forbidden v2 flags/config/commands, cancellation and startup failure. Test CLI output through Cobra writers, including parseable JSON for implemented inspection commands.
  - Delete unreachable v2 wiring and its obsolete mocks/dependencies incrementally with regression coverage. Do not create build tags or a compatibility switch that ship both products.
  - Done: C2/C3/C5/C6/C10; role dependency tests prove separation and root help no longer advertises legacy behavior.

- [ ] **A1B.2 — Implement private APIs, ingress transport and readiness** — depends on: A1B.1; F1/F2/F3 accepted.
  - Implement the accepted capability matrix, strict DTO validation, bounded requests/streams, peer checks, socket recreation and clean cancellation. Ports are consumer-owned; do not expose full runtime methods on a common socket. Edge is trusted for routed app/registry traffic, but receives no private stores or administration capabilities.
  - Only if the fallback remains selected, implement TCP relay, UDP associations/epochs and authorized listener reconciliation from A1A.3–4. Port the proof assertions, not shortcuts. Keep ingress admin separate from edge data and host sockets solely in ingress. Native success instead requires the accepted Podman publication contract, not this relay.
  - Persist only the agreed listener recovery state. Exclude UDP sessions. Handle malformed peers, unknown/stale generations, uncertain withdrawal and slow readers without unbounded goroutines/buffers.
  - Implement role readiness with separate process/role/dependency/identity observations. A live listener alone is not proof of public readiness. An unavailable dependency produces a specific degraded result without starting unrelated components.
  - Test unauthorized method/role/body, wrong socket ownership, peer reconnection, cancelled streaming, invalid/older snapshots, restart/reboot epochs and ingress/edge failure. Use real Unix sockets plus deterministic state fakes before C8.
  - Done: C2–C6/C10 and A1A transport/authority scenarios pass against production roles, not only proof peers.

- [ ] **A1B.3 — Build one attributable distribution** — depends on: A1B.1; F3 accepted.
  - Change `Dockerfile`/build tooling so one built host executable is copied into the component image; never independently compile two allegedly identical binaries. Remove v2 Docker CLI/runtime payloads not required by accepted role contracts.
  - Record source/version, executable SHA-256, component image digest and persistent format versions using the accepted identity format. Validate exact bytes inside the built image before declaring success. Quadlets will use the resulting immutable digest.
  - Resolve branch/commit builds to one clean full revision. Dirty local builds identify both commit and source-tree hash; never label a dirty artifact clean. Source identities are attributable but unauthenticated.
  - For signed tagged distributions, implement or explicitly gate signed-manifest publication and pinned-key verification before any downloaded executable is run. Test tampered manifest, wrong signer/hash, mismatched image and absent signature; no unsigned fallback for version mode. No `gordon update` implementation.
  - Preserve useful `pkg/version` formatting only where it does not replace artifact verification with a version string or image label. Test role reports against observed executable/expected generation.
  - Done: C2/C3/C5/C10 plus an image extraction/hash proof. Build/report failures leave no falsely verified artifact.

- [ ] **A1B.4 — Locked and journaled host installation** — depends on: A1B.2–3.
  - Reduce `install.sh` to verified acquisition or source build followed by the accepted host installer command. Validate mutually exclusive source selectors before effects. Bootstrap may not secretly install v2 or change another generation.
  - Host installation validates platform, rootless engine, socket permissions, applicable confinement, user systemd/Quadlet and boot-without-login prerequisites. Report administrator-owned prerequisites including lingering; no sudo, system-account/service creation or firewall/sysctl mutation. Later secret provisioning must use the separate runtime-only read-only key mount, not environment variables.
  - Under the installation lock, persist intended state before creating owned directories, private data/capability mounts, networks, generated units or starting services. Use the F3 atomic/fsync/idempotency contract and exact source/image identity.
  - Generate four independent hardened Quadlets, confined ingress user service and installation target. Record generation/checksums; detect unknown edits before overwrite and preserve backups in explicit recovery. Systemd supervises continuously; ingress and runtime do not become component supervisors.
  - Fault-inject before/after each persistent record, file replacement and external effect. Test duplicate install requests, disk/write failures, permission failures, port conflicts, missing image, image/hash mismatch and restart after partial unit generation.
  - Refuse replacing a complete different generation and adopting unknown resources. Same-generation incomplete installation observes existing resources and resumes without duplicate networks/volumes or data loss. Unknown/incompatible persistent formats fail closed.
  - Done: C2–C6/C10 plus C8 clean branch/commit/local installation and interrupted same-generation resume. Complete success requires all five roles ready with the expected identity.

- [ ] **A1B.5 — Private administration, network authority and useful status** — depends on: A1B.2, A1B.4.
  - Implement local CLI → control admin Unix socket and remote CLI → OpenSSH Unix-socket forwarding → the same endpoint. Replace v2 remote bearer/TCP and local service construction; do not implement public control HTTPS during alpha.
  - Implement `remotes add/list/remove/use`, `status`, `version` and completion for supported operations. Handle pinned/normal SSH host trust, authentication failure, forwarding failure, socket absence and cancellation without fallback to insecure TCP or orphan SSH children.
  - Status distinguishes intended/staged/running/failure distribution state, role readiness and dependency/identity errors; return bounded sanitized diagnostics without secret values or needless capability paths.
  - Install and verify the F2 private registry-pull path and ingress-network contract. No public OCI push yet. Test edge/registry/ingress inability to reach Podman, trusted-core storage or arbitrary host destinations.
  - Reserve Gordon resource names/management identity; image-inherited labels cannot mark workloads as components. Expose no generic component mutation in runtime workload ports; only the accepted narrow edge attachment operation is allowed.
  - Done: C2–C6/C10 and C8 real local/SSH administration, denial matrix and private pull independent of edge availability.

- [ ] **A1B.6 — Clean-host, reboot and failure release gate** — depends on: A1B.1–5.
  - Extend the A1A harness with full installer scenarios and document exact invocations in `dev/v3/README.md`. Test all source selectors on separate clean installs; retain identity/proof reports.
  - Reboot without login; verify lingering/target ordering starts five roles with valid sockets and identity. Restart each role separately; recreate its sockets. Kill installer mid-effect and resume the same generation. Inject edited Quadlets, damaged state and mismatched executable/image identities; report incomplete/degraded, never healthy.
  - Re-run confinement, TCP/UDP stale-reply/withdrawal, source identity, private pulls and network isolation against generated units. Prove ingress failure interrupts TCP/loss of UDP sessions and does not cause a healthy edge to restart.
  - Extend `.github/workflows/ci.yml` so Go, installer, image, generated-unit fixtures and nested proof changes trigger their relevant checks. VM tests need an explicit capable/authorized runner; hosted unit tests are not the reference-host gate. Keep full lint/test/race checks.
  - Update installation/CLI docs and examples to distinguish implemented Alpha 1 from future apps. Delete remaining v2 reachable paths and direct dependency baggage; do not promise a working public registry or workload command.
  - Done: C1–C10 as applicable, reviewed proof reports, no unresolved authority/recovery finding. Alpha 2 remains blocked until this complete installed system passes.

## Rollout and recovery

Only fresh authorized reference hosts or same-generation incomplete installations are allowed. Preserve unknown unit edits and resources. An interruption resumes its journal after observation; it never blindly deletes/recreates the installation. Do not test a new stage by replacing an existing generation: use a fresh VM until the separate update lifecycle is accepted and implemented.

All five roles share one intended identity. Mixed identities, readiness errors, unresolved ownership or failed confinement are release blockers, not warning-only successes.

## Related

- [Plan index and shared checks](README.md)
- [Previous: Alpha 1A](alpha-1a-foundation-proofs.md)
- [Next: Alpha 2](alpha-2-web-app.md)
- [Accepted design](../design.md)
