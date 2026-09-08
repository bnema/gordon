# Alpha 1A: foundation contracts and proofs

Status: A1A.0/A1A.1 complete with negative results; four-container topology from ADR-004 and required pasta/Pesto publication from [ADR-006](../adr-006-pasta-pesto-publication.md). A1A.2–6 remain unaccepted until their contracts and evidence pass review. This stage is not a production-ready installation.

## Context and scope

Use the [shared baseline, constraints, checks and executor rules](README.md). ADR-004 selects four rootless containers, merged edge and runtime-owned publication. ADR-006 requires Podman 6.1.1, pasta/Pesto 2026_07_28.f8df3f1, Netavark/aardvark-dns 2.1.0 or a later explicitly validated combination. No host ingress, relay IPC, rootlessport fallback or host-process confinement gate.

Outcome: accepted, test-backed contracts for Alpha 1B. No app control plane, public production deployment, firewall management, cluster API, component update or UDP session persistence.

Existing tooling: `dev/v3/cmd/sandbox/`, `dev/v3/cmd/l4probe/`, `dev/v3/cmd/foundationproof/`, `dev/v3/README.md` and two fixture modules. Extend the existing test-only harness and store sanitized reports in `dev/v3/proofs/`. Do not turn the sandbox into an installer or component supervisor.

## Tasks

- [x] **A1A.0 — Native publication checkpoint** — negative result recorded in [the proof](../../../dev/v3/proofs/a1a0-native-pasta.md).
  - Packaged bridge publication NAT-rewrites TCP/UDP source; the pasta port-forwarder/Pesto mechanism is unavailable on the reference stack.
  - This is historical Podman 5.7 evidence. The [Podman 6 experiment](../../../dev/v3/proofs/podman6-pasta-pesto.md) passes basic IPv4/IPv6 source preservation and dynamic Pesto forwarding; ADR-006 selects that stack. Neither experiment closes F1/F2 recovery and isolation gates.

- [x] **A1A.1 — Host-process confinement checkpoint** — negative results recorded in [the proof](../../../dev/v3/proofs/a1a1-confinement.md).
  - The tested systemd user-unit filesystem sandbox was silently unapplied; the tested Landlock rule installation failed.
  - ADR-004 removes the host process. Keep the harness and historical reports; test container/capability denial instead of attempting another host relay.

- [ ] **A1A.2 — Capability socket and role filesystem contract** — depends on ADR-004/006.
  - Extend proposed ADR-005 for runtime-only Pesto control: exact directory mount, socket recreation, peer permissions and authority over the shared rootless engine. Prove access from the runtime container and denial from every other role/app; no arbitrary Pesto command proxy.
  - Write a focused socket/identity ADR with a producer/consumer matrix for control administration, edge projection, registry events and runtime control. Specify host/container directory layout, mounts, owners, UID/GID mappings, modes, peer checks and startup/recreation behavior.
  - Separate role-private storage from capability directories. Only runtime receives Podman; no role receives another role's store or runtime's secret key. Mount directories rather than socket inodes.
  - Specify strict HTTP/JSON version, envelope, errors, body limits and streaming cancellation. No internal bearer tokens, generic RPC or data-relay framing. A caller-supplied role/path is not identity proof.
  - Test socket deletion/recreation, wrong UID, swapped endpoint, unauthorized role, excessive body, unknown fields and malformed requests. Verify both authorized access and forbidden access from actual container contexts.
  - Keep seccomp, applicable LSM policies, user namespaces, dropped capabilities, no-new-privileges and least-authority mounts intact. Apply ADR-006's accepted Ubuntu rootless AppArmor exception: host AppArmor stays enabled, but a per-container profile is not an acceptance prerequisite. Prove the remaining protections with effective container denial tests; report the missing layer and limits of evidence on other distributions.
  - Done: accepted matrix and automated positive/negative tests. The installer must not guess socket paths or UID policy later.

- [ ] **A1A.3 — Edge traffic plane and runtime publication lifecycle** — depends on A1A.2.
  - Resolve the proposed direct forwarding of dedicated workload TCP/UDP before accepting F2. If selected, record the reduced edge role and assign client policy, readiness, destination authority, withdrawal and session cleanup in a focused amendment; update A1A.4 and A5.1–3 before implementation. Otherwise retain the accepted edge path. Do not implement both or defer this foundation ownership question to Alpha 5.
  - Specify exact publication authorization, operation IDs/generations and reserve/create/activate/withdraw states. Control owns authorization; runtime executes Pesto forwarding rules; edge owns container listeners and cannot request arbitrary host publication.
  - Specify minimal durable authorized/applied mapping records, atomic boundaries and observe-before-resume recovery, including uncertain outcomes. Do not introduce the full app journal in proof tooling.
  - Specify single mapping ownership and pasta bootstrap with no public rules. Runtime changes rules through Pesto without recreating edge; dynamic rules must not also appear in static Quadlet PublishPort entries. Prove coexistence with Podman setup/teardown, pasta/socket recreation and reboot. Systemd supervises; no host lifecycle executor or runtime systemd API.
  - Bind every rule to authorized destination identity and current address; prevent stale rules exposing an unrelated container that reuses an address. Test lost replies, concurrent mutations and crash boundaries. Measure established-flow behavior on add/delete rather than assuming uninterrupted traffic.
  - Run binary-transparent bidirectional TCP with half-close, slow readers/writers, abrupt death and saturation. Set bounds for connections, buffers, timeouts, backpressure and cleanup using measured behavior.
  - Shared-route withdrawal is acknowledged by edge with bounded route-stream cleanup. Dedicated/final-listener withdrawal additionally requires independently verified runtime mapping removal. Missing/false edge acknowledgement must not free an uncertain reservation; runtime must be able to remove publication despite an uncooperative edge.
  - Kill each test role before/after persisted effects and resume without manual replay. Restore only authorized applied mappings, exclude withdrawn mappings, remove stale binds safely, and demonstrate interruption rather than transparent edge recovery.
  - Binding an additional container-local port must not publish it. Prove edge cannot reach Podman/admin capabilities or obtain publication authority.
  - Done: accepted publication/traffic contract, repeatable recovery/authority scenarios and measured limits.

- [ ] **A1A.4 — Edge UDP sessions, epochs and disruptive recovery** — depends on A1A.2–3.
  - Define edge-owned associations keyed by listener/epoch, observed client and local destination. Direct source preservation follows F2; upstream NAT/proxies may already rewrite identity. Define non-reused listener epochs across process/host restart; do not persist sessions.
  - Select/test datagram size, queued-byte, association-count, idle-time, per-client work and response-amplification limits. Define overload behavior precisely.
  - Test datagram boundaries, binary fidelity, multiple/unsolicited backend replies, multiple local addresses and IPv4/IPv6. Replies use the observed local destination and can target only the original client of a live association.
  - Exercise edge admission/forwarding closure → epoch/association invalidation → acknowledgement → simulated backend replacement → publication/route readiness → fresh epoch/admission. A global snapshot change must not invalidate unrelated listeners. A rule change must not require edge restart. Prove effects on existing pasta and edge associations; rule deletion alone is not session cleanup. Edge restart loses its sessions.
  - Inject late backend replies after invalidation, restart and reboot; reject old epochs and expired/guessed associations. Recover only authorized listeners/routes with empty sessions. Delayed client datagrams may create new sessions; no application-aware replay detection.
  - Done: accepted UDP contract and repeatable bidirectional/restart evidence. Echo-only probes and manually replayed configuration do not close this task.

- [ ] **A1A.5 — Network, source limits and private registry proof** — depends on ADR-006 and the relevant A1A.2–4 contracts.
  - Specify public-to-local mapping and canonical reservation rules: shared HTTP/HTTPS listeners, dedicated route binds, wildcard/specific overlap, dual-stack conflicts and reserved registry domain. No installation-level app-port catalogue.
  - Test administrator-owned firewall allow/deny/redirect policy from external clients; compare policy before/after to prove Gordon changes no rules/sysctls. Unauthorized privileged binds fail explicitly.
  - Trace client → rootless publication → edge → backend for HTTP/TCP/UDP. Verify original host-observed source at edge with two direct clients for IPv4/IPv6, and record edge-address backend peers separately. Raw-transport `trusted_cidrs` and backend original-source requirements remain unsupported.
  - Specify and test securely restricted/authenticated upstream HTTP identity conveyance, header sanitation, spoof rejection and origin bypass rejection. Test direct Internet/private-tailnet access without an upstream and reject client-provided identity headers in direct mode. Cloudflare/NetBird proxy modes require their own restricted trust path; upstream NAT cannot be reversed by Pesto.
  - Prove per-app private versus generated ingress-network isolation, edge restart attachment restoration and narrow runtime reconciliation. No generic Gordon component mutation through workload APIs.
  - Prove the rootless engine can pull a digest through an authenticated private registry endpoint with edge stopped. Verify endpoint trust, pull-only credentials and push denial. Control/edge receive no credential-store mount. Use a private fixture; this does not enable public registry.
  - Done: accepted address/network/private-pull contract and reference-host report. An accepting port proves neither isolation nor identity.

- [ ] **A1A.6 — Installation/distribution contract and execution handoff** — depends on F1/F2 accepted.
  - Specify host installer and role command syntax, installation configuration/defaults/validation, owned paths, four Quadlets and target, identity encoding and persistent-format versions. Keep public registry disabled by default and all setup unprivileged.
  - Specify the minimal Podman API and Pesto control subset, runtime helper availability and rootless verification. Validate the ADR-006 stack and actual selected forwarder; missing/incompatible helpers or rootlessport fail closed. Administrator-owned prerequisite provisioning must not upgrade the engine or install privileged packages through Gordon. No Docker fallback. Storage engines are fixed; initial secret-key provisioning must respect the later secret record contract rather than inventing an incompatible store.
  - Specify installation locking, intended/staged/running/failure records, fsync/atomic replacement, same-generation resume and refusal of foreign/different-generation installations. Preserve unknown generated-unit edits through explicit recovery with backup.
  - Define mutually exclusive branch/commit/local/version inputs, clean/dirty source attribution, exact host/image executable equality and digest recording without circular self-hashing. Validate identity against installation state, not inherited image labels.
  - Define readiness for all four roles: process, own role, dependencies, publication and expected distribution. Specify startup ordering, lingering validation and late/unavailable sockets without always-healthy placeholders.
  - Signed version bootstrap uses a pinned release key; source inputs are explicitly unauthenticated. Incomplete signed publication fails closed; no update/rollback lifecycle is selected.
  - Review contracts and high-impact findings, attach schemas and crash test vectors, and update Alpha 1B task anchors before execution.
  - Done: F3 accepted, F1/F2 evidence linked and applicable harness/fixture checks pass. Alpha 1B has no unresolved mechanism assumption for its selected slice.

## Acceptance and recovery

C8 evidence covers usability and forbidden authority on an explicitly authorized disposable reference host with synthetic data. C2–C6 apply to production/shared Go changes; C7/C9/C10 apply to tooling and fixtures. Fakes are not reference-host evidence.

Inspect ownership before cleanup. Never delete unknown volumes/networks, reset a nonempty engine or modify a production firewall. Sandbox teardown is not product recovery. Preserve failed reports and resume at the failing task; revise and review candidate contracts when the evidence contradicts them.

## Related

- [Plan index and shared checks](README.md)
- [Next: Alpha 1B](alpha-1b-installation.md)
- [Merged edge ADR](../adr-004-merged-edge.md)
- [Development host guide](../../../dev/v3/README.md)
