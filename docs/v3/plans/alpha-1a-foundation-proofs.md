# Alpha 1A: foundation decisions and proofs

Status: planned; N0 native-network proof first, then applicable F1–F3 contracts/proofs. This plan produces evidence, not a production-ready installation.

[ADR-003](../adr-003-alpha-scope-and-trust.md) takes precedence. A1A.1/3/4 are ingress-fallback work only; if A1A.0 succeeds, remove them and retain/refine common socket, network, ownership and recovery contracts. Do not interpret the numbered fallback dependencies as requiring the role being eliminated.

## Context and scope

Use the [shared baseline, constraints, checks and executor rules](README.md). ADR-002 has rejected host descriptor handoff, not proven the replacement ingress secure. Tested user-service profiles failed either isolation or startup. Do not reuse them as a working fallback or conclude that confinement is impossible.

Outcome: accepted, test-backed contracts for Alpha 1B. No app control plane, production deployment, firewall management, cluster API, component update or UDP session persistence.

Existing anchors: `dev/v3/cmd/sandbox/main.go` (`run`, `sandbox.dispatch`, `sandbox.sync`), `dev/v3/cmd/l4probe/main.go`, `dev/v3/README.md`, and the two fixture modules. Proposed new work areas: `dev/v3/proofs/` for sanitized reports and `dev/v3/cmd/foundationproof/` for an explicitly test-only harness. Do not turn the sandbox into an installer or component supervisor.

## Tasks

- [ ] **A1A.0 — Retest native pasta publication before implementing ingress** — depends on: authorized disposable host; no ingress implementation prerequisite.
  - Maintainer direction (2026-09-06): retest pasta first. If native publication meets the required behavior and isolation, **omit the Gordon host ingress role entirely**, rather than keeping it as an optional fallback or implementing two paths.
  - Recover earlier experiment commands and package versions first. Direct `--network=pasta` was already tested; do not present it as new. Separately verify availability and support of bridge publication via `rootless_port_forwarder = "pasta"` / Pesto on the reference stack. Upstream documentation is not proof of packaged availability or functionality; do not silently upgrade packages or change an active engine's configuration.
  - In an empty rootless engine, publish TCP and UDP through Podman to an edge fixture attached to named private ingress networks. Test two distinct clients, kernel source identity at edge, IPv4/IPv6, bidirectional UDP with multiple replies, binary fidelity, and administrator-managed port redirections. Record backend observations separately: ordinary edge proxying still exposes edge's address to the backend.
  - Test adding/removing published ports, edge restart/recreation, host reboot, port reuse while another container stays running, and absence of stale binds. Establish whether publication changes need edge recreation; installation-wide interruption of unrelated routes is an explicit maintainer trade-off, not a passing result hidden behind "functional".
  - Prove app-network isolation and absence of Podman/admin/Pesto capabilities or private data in edge/apps. No host networking, host descriptor handoff, required privileged Gordon operation, dedicated system account or system service. The administrator owns firewall/forwarding changes.
  - Done: sanitized commands/versions/results plus critical review. On success, amend ADR-002/design/agent guidance and this plan set to the proven four-role topology before production implementation; remove ingress-specific lifecycle, confinement, relay and IPC tasks, while retaining applicable network, reservation and recovery tests. On failure or incomplete evidence, record the exact blocker and return to the ingress decision; no claim that the native path works.
  - Until this task is resolved, **do not start ingress-specific implementation in A1A.1–4 or Alpha 1B**. No native-network test was executed when recording this task.

- [ ] **A1A.1 — Reproducible proof harness and confinement decision** — depends on: A1A.0 resolved without a successful native replacement; authorized disposable host.
  - Record Ubuntu/kernel/Podman/systemd/network-helper/LSM versions, account and UID mapping, test revision, expected allowed resources and forbidden canaries. Preserve the README's aardvark experiment caveat; do not silently replace guest packages.
  - Add bounded harness scenarios and machine-readable pass/fail results. Reuse sandbox VM lifecycle/SSH rather than introducing another provisioning framework. Require positive controls proving canaries exist and are readable by the trusted account before testing denial from ingress.
  - Compare only mechanisms compatible with the approved same-account rootless/user-service model. No dedicated system account, system service or privileged Gordon setup. If no candidate meets direct private-state/Podman isolation, stop and report the blocker; do not silently weaken the boundary.
  - From the ingress process context, test denial of application secret files, Podman socket/storage, control-private files, arbitrary host writes, `/proc`/same-account process access and filesystem escape paths. Test symlink/path traversal and unauthorized socket access. In the same sandbox verify legitimate host binds and private IPC remain usable.
  - Test confinement again after service restart and reboot. Keep AppArmor/SELinux, seccomp, user namespaces and no-new-privileges intact. Do not grant host-network access to edge to bypass a failed bind.
  - Done: reproducible allow/deny report plus accepted confinement ADR and critical review. Failure blocks public use and dependent installer composition; an experiment that cannot start is not a passing sandbox.

- [ ] **A1A.2 — Capability socket and role filesystem contract** — depends on: A1A.1 candidate confinement; accepted mechanism before final proof.
  - Write the focused socket/identity ADR with an explicit producer/consumer matrix for control admin, edge projection, registry events, runtime control, ingress admin and ingress-edge data. Specify each host path, mounted directory, container path, owner, UID/GID mapping, mode, peer validation and startup/recreation behavior.
  - Decide exact strict HTTP/JSON envelope/version/error/body-limit rules for ordinary APIs and streaming cancellation rules. Dedicated data IPC is specified by A1A.3–4, never routed through an all-purpose admin handler.
  - Mount directories, not stale socket inodes. Test deletion/recreation, wrong UID, swapped endpoint, unauthorized role, excessive body, unknown fields and malformed requests. A path or role string supplied by the caller is not identity proof.
  - Verify role-private data is absent from other mounts; only runtime sees Podman; edge cannot reach ingress admin or runtime control. Validate equivalent protection on the reference host's active LSM and document what is not proven on other distributions.
  - Done: accepted matrix and automated negative tests. No production socket path/UID policy may be guessed later by the installer.

- [ ] **A1A.3 — TCP relay and listener lifecycle contract** — depends on: A1A.2 candidate capability layout.
  - Specify the dedicated Unix framing/version/peer contract, listener operation IDs and generations, cancellation, bounded buffers/connections, backpressure, timeouts and cleanup. No listening or connected host descriptor is transferred.
  - Specify the minimal authorized applied-listener journal, atomic persistence and observe-before-resume recovery rules. Control authorizes binds; ingress never accepts bind or outbound-connect authority from edge. Distinguish reserve/create/activate/withdraw and uncertain outcomes without claiming the app journal already exists.
  - Add test-only trusted-control and edge peers. Run binary-transparent bidirectional traffic with half-closes, slow readers/writers, malformed frames, abrupt peer death and resource saturation. Prove edge cannot invoke arbitrary host connections or repurpose a host descriptor.
  - Bind metadata to authenticated ingress identity: kernel client/local destination, address family/interface/scope where required. Spoofed client headers/payloads must not replace it.
  - Test dedicated/final-listener close plus bounded accepted-stream cleanup. A false or missing edge ACK must not leave an untracked host bind or free an uncertain reservation. Shared-route attribution stays at edge; ingress must not parse HTTP/SNI to release a route.
  - Kill each test role before/after persisted listener effects; restart without manual replay. Restore only authorized listeners, do not restart a healthy edge, and show old TCP connections fail rather than claiming seamless ingress restart.
  - Measure throughput, CPU, memory, connection limits and slow-peer behavior against a direct baseline. Report results; select limits from evidence, not an unmeasured negligible-overhead assumption.
  - Done: accepted IPC/listener recovery contract, passing repeatable scenarios and explicit remaining limitations.

- [ ] **A1A.4 — Bidirectional UDP associations and disruptive recovery** — depends on: A1A.2–3 transport/lifecycle contract.
  - Extend that contract for framed datagrams, listener-local non-reused epochs, kernel-observed client/local destination, reply-source/interface selection and bounded in-memory associations. Decide epoch allocation across process/host restart before coding it; do not persist sessions.
  - Select/test limits for datagram/frame sizes, queued bytes, association count, idle lifetime, work per client and response amplification. State overload and malformed-peer behavior precisely.
  - Test datagram boundaries and binary fidelity, multiple and unsolicited backend responses within a live association, wildcard/multiple local addresses, IPv4/IPv6, invalid frame lengths and unauthorized peers.
  - Prove edge replies can target only a live ingress-owned association's original client, never an arbitrary destination. Test guessed/expired associations and late backend/IPC replies after epoch invalidation.
  - Exercise close admission and forwarding → invalidate listener epoch/associations at ingress and edge → simulated backend replacement → readiness → reopen with fresh epoch. Restart processes and inject a captured old response to prove epochs cannot be reused. Another listener's sessions must survive unrelated replacement.
  - Test ingress/edge crash and reboot: recover authorized listeners only with empty sessions. Delayed client datagrams can count as new traffic; do not invent application-aware replay detection or migration.
  - Done: accepted UDP contract and full bidirectional/restart evidence. A request/response echo or manual replay alone cannot close this task.

- [ ] **A1A.5 — Network, source identity and private registry proof** — depends on: A1A.1–4 usable candidate transport.
  - Specify public-to-local address mapping and canonical reservations, including shared installation HTTP/HTTPS listeners, dedicated route binds, wildcard/specific conflicts, dual-stack overlap and registry SNI reservation. No installation-level app-port catalogue.
  - Test administrator-managed allow/deny/redirect rules with external clients; snapshot policy before/after to prove Gordon changes no firewall rules/sysctls. Unauthorized privileged binds fail clearly.
  - Trace client → ingress → edge → backend for HTTP/TCP/UDP. Record observed addresses at both edge and backend; test `trusted_cidrs` using authenticated client identity at edge, including denied and spoofed-source cases. Test HTTP forwarding-header sanitation/trust separately from kernel source preservation.
  - Ordinary backend connections expose edge's address. Keep workloads requiring original backend source blocked unless a separately accepted/proven mechanism satisfies that requirement; no generic source-preservation claim follows from edge metadata.
  - Prove app-private versus generated ingress-network isolation using fixtures. Test edge restart and runtime's narrow attachment reconciliation without allowing component create/delete/restart through workload APIs.
  - Prove the rootless Podman engine can pull a digest through an authenticated private registry endpoint independent of edge. Use pull-only runtime credentials and validated endpoint trust; demonstrate push denial and that control/edge/ingress receive no private credential-store mount. Public OCI credentials may later transit trusted edge under ADR-003. This is a controlled private fixture, not public registry enablement.
  - Done: accepted address/network/private-endpoint contract and reference-host report. No direct `podman -p` experiment substitutes for the complete relay path.

- [ ] **A1A.6 — Installation/distribution contract and execution handoff** — depends on: F1/F2 accepted.
  - Specify host installer and, only if retained, ingress command spellings, installation configuration schema/defaults/validation, owned directory/unit paths, identity encoding and persistent format versions. Keep public registry disabled by default and provision only user-owned resources. Specify the minimal native Podman API subset and rootless verification; no Docker fallback. bbolt for control and encrypted runtime secrets are already selected; schemas and key provisioning remain focused contracts.
  - Specify installation lock scope, intended/staged/running/failure records, fsync/atomic replacement boundaries, same-generation resume and refusal of foreign or complete different-generation installations. Define preservation of unknown generated-unit edits and operator recovery with backup.
  - Define source input selection (`GORDON_BRANCH`, `GORDON_COMMIT`, `GORDON_LOCAL`, `GORDON_VERSION` mutually exclusive), clean versus dirty attribution, exact host/image executable equality and digest recording without circular self-hashing. Runtime identity must be validated against installation state, not untrusted image labels.
  - Define readiness observations for all five roles: process, own role, dependencies, expected distribution. Specify installation target/order/lingering and behavior when roles or sockets are late or unavailable. No always-healthy placeholder endpoint.
  - Keep signed version bootstrap rooted in a pinned release key; source inputs explicitly unauthenticated. No update/rollback lifecycle is selected. Record how unfinished signed distribution publication is rejected rather than silently downgraded.
  - Review security/architecture findings and attach exact schemas/test vectors to the accepted ADRs. Update Alpha 1B task anchors to the accepted contracts before execution.
  - Done: F3 accepted, F1/F2 evidence linked, C1/C7 and all added harness/fixture checks pass. Alpha 1B has no unresolved confinement or transport mechanism assumption.

## Acceptance and recovery

C8 proof records must cover both positive usability and forbidden authority; C2–C6 apply to production/shared Go changes, C7/C9/C10 to proof tooling. No VM was run during plan authoring.

Experiments stay on an explicitly authorized disposable host with synthetic data. After an interrupted run, inspect ownership before cleanup; never delete unknown volumes/networks or modify a production firewall. A sandbox teardown is not product recovery evidence. Preserve failed reports and resume at the failing task, revising and re-reviewing its candidate contract if needed.

## Related

- [Plan index and shared checks](README.md)
- [Next: Alpha 1B](alpha-1b-installation.md)
- [Host-ingress ADR](../adr-002-host-ingress.md)
- [Development host guide](../../../dev/v3/README.md)
