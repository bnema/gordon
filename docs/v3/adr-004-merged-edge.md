# ADR-004: Merged edge container with runtime-owned publication

- Status: Accepted as amended by [ADR-006](adr-006-pasta-pesto-publication.md)
- Date: 2026-09-08
- Decision owner: Gordon maintainer
- Amends: [ADR-002](adr-002-host-ingress.md), [ADR-003](adr-003-alpha-scope-and-trust.md) §5, and the five-role topology in [design](design.md)
- Related: [A1A.0](../../dev/v3/proofs/a1a0-native-pasta.md) and [A1A.1](../../dev/v3/proofs/a1a1-confinement.md) proof records, [implementation plans](plans/README.md)

## Publication amendment

[ADR-006](adr-006-pasta-pesto-publication.md) requires Podman 6 and pasta/Pesto, with runtime-controlled dynamic forwarding. It supersedes the native-path abandonment, NAT baseline and edge-recreation assumption below. Four containers and no Gordon host ingress remain the topology. The original mechanism rationale below is historical.

## Context

Gordon is a small single-host reverse-proxy deployer for self-hosted environments, not a hostile multi-tenant hosting service (ADR-003 context). Its security bar is ordinary self-hosting practice: an internet-facing proxy with no lateral movement into the engine, secrets, or administration — not containment against an attacker already present on the host account.

Two bounded proofs closed the previous topology options on the reference host (Ubuntu 26.04, Podman 5.7.0):

- A1A.0: bridge `-p` publication NAT-rewrites client source (TCP+UDP); no pasta port-forwarder or Pesto is packaged. The maintainer abandoned the native/passt path entirely, including source-built retests.
- A1A.1: no same-account host-process confinement mechanism works. Unit mount sandboxing is silently unapplied (H1); Landlock rule installation is rejected by the kernel (H2). There is no host ingress process left to confine.

The maintainer further directed: keep V2's proven traffic behavior and move it into a container; publish registry via edge with edge-terminated TLS (option 1); scope security pragmatically instead of military-grade.

## Decision

Gordon v3 has **four containers and no host ingress process**: `control`, `runtime`, `edge`, `registry`. Edge is a merged traffic-plane container: it owns container-network listeners published by ordinary rootless Podman `-p` and performs V2's routing, TLS, and TCP/UDP proxying inside one rootless container.

| Role | Responsibility |
| --- | --- |
| control | Authorize and journal exact listener mappings from reserved route state; single allocator across desired, active, and in-flight references, including rollback |
| runtime | Execute publication (`-p`) and workloads; sole Podman socket holder; narrowly reconciles edge publication and app-ingress attachments |
| edge | Merged traffic plane: published listeners, application and explicitly public registry TLS/routing, backend connections, trusted-proxy client policy |
| registry | OCI storage, authentication, private runtime pulls, bounded push-event outbox; private by default, explicitly publishable via edge |

Control authorizes, runtime publishes, edge serves. Edge receives resulting configuration, never authority to request arbitrary publication. Binding another container-local port must not publish it. No host-network descriptors exist anywhere in this topology; no Unix relay IPC between ingress and edge is built (A1A.3/A1A.4 relay machinery is dropped).

### V2 behavior reuse (not wholesale copy)

Edge reimplements V2's proven traffic behavior against control's route projection instead of V2's database and image labels:

- hot-applied traffic snapshot (entrypoints/routers/services) fed by control's sanitized route projection;
- per-host HTTP routing with header trust restricted to authenticated/upstream-proxied sources only (V2 `GetClientIP` pattern), concurrency and body limits, h2c;
- smart TCP dispatch (HTTP/h2c/TLS/PROXY-protocol-v2 sniffing) with SNI routing;
- per-client UDP sessions with idle eviction, extended with epoch invalidation on recreate;
- ACME issuance and the registry-domain branch for explicitly published registry.

V3 rejects V2's image-label configuration, route-owned containers, implicit deploy, mutable tags, and Docker. Existing code is not evidence; reimplement only fitting invariants in the smallest design.

### Registry via edge (option 1)

Registry stays private by default and is published only by explicit system-domain configuration creating a reserved system route. When published, edge terminates registry TLS and forwards to the registry container like other HTTP services, with short-lived control-minted push tokens. Runtime pulls stay on the private network and never transit edge. Edge-observed push credentials are an accepted alpha risk of the same class as terminated application traffic; SNI passthrough without termination remains the post-alpha hardening, not an alpha gate.

### Pragmatic alpha scope

Non-negotiable: no Podman socket outside runtime, no host network/PID/IPC, no privileged setup or dedicated account, no firewall/sysctl mutation, control-journaled reservations, write-only service secrets, digest-pinned images, per-app private networks.

Documented limits (not failures): NAT-rewritten peer addresses with no kernel source identity — no `trusted_cidrs` on raw transports in alpha, HTTP identity only from restricted upstream conveyance; edge-wide interruption when the published-port set changes; no UDP session migration or restoration.

Abandoned as military-grade for this product: host-process sandboxing (no host process remains), end-to-end kernel source preservation, independent relay-vs-edge revocation, registry confidentiality against a compromised edge terminator.

## Consequences

- ADR-002's transport-only host ingress, relay/IPC contracts, and confinement gates are superseded, not merely unmet. Its descriptor-handoff rejection and reservation/withdrawal reasoning remain valid history.
- ADR-003 §5's native-vs-ingress checkpoint is resolved: native abandoned, host ingress replaced by merged edge. The rest of ADR-003 (trust boundaries minus the fifth role, storage, shutdown, rootless) stands.
- Design five-role text, the ingress service, and relay/IPC tasks are replaced by four-container topology with runtime-owned publication. Per-app ingress networks and runtime attachment reconciliation are unchanged.
- New proofs required before public use: runtime publication lifecycle (readiness, independent withdrawal, stale-bind cleanup, crash/reboot reconciliation, reservations/rollback, stopped-intent); container/capability denial and unauthorized publication; HTTP metadata spoof/bypass rejection; private pulls with edge down; bounded draining and UDP epoch rejection. Confinement proofs of a host process are moot.
- Reversal: only an explicit maintainer decision adopting different installation/topology invariants through a new ADR, or a supported mechanism restoring kernel source identity on the reference host (which would reopen, not invalidate, this topology).

## Related

- [Design](design.md)
- [Alpha scope and trust](adr-003-alpha-scope-and-trust.md)
- [Host-ingress record](adr-002-host-ingress.md)
- [Plan index](plans/README.md)
