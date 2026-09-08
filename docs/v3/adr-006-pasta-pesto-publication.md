# ADR-006: Require Podman 6 and runtime-controlled Pesto publication

- Status: Accepted product and architecture direction; production contracts and acceptance proofs outstanding
- Date: 2026-09-08
- Decision owner: Gordon maintainer
- Amends: [ADR-004](adr-004-merged-edge.md), publication mechanism and source-address baseline
- Evidence: [bounded reference-VM experiment](../../dev/v3/proofs/podman6-pasta-pesto.md)

## Context

The packaged Podman 5.7 stack lacked the pasta/Pesto bridge forwarding mechanism. ADR-004 consequently selected ordinary rootless publication with NAT-rewritten peers. Creation-time `-p` mappings also left an unresolved ownership problem: changing them requires container recreation, while runtime owns publication and host-owned Quadlet/systemd owns edge creation and supervision.

A maintainer-authorized experiment with Podman 6.1.1 and current pasta/Pesto demonstrates source-preserving bridge publication for TCP/UDP over IPv4/IPv6, and direct Pesto addition/removal of forwarding rules without restarting the destination container. Absence of a port-update operation in `podman update` does not imply absence of dynamic forwarding through Pesto.

The maintainer selects this stack as a hard dependency. Distribution defaults do not justify retaining a second networking architecture.

## Decision

### Required stack, no fallback

The development/reference baseline is:

| Component | Validated experiment version |
| --- | --- |
| Podman | 6.1.1 |
| pasta and Pesto | 2026_07_28.f8df3f1 |
| Netavark | 2.1.0 |
| aardvark-dns | 2.1.0 |

Require this baseline or a later explicitly validated combination, not merely a numeric version comparison. Validate actual helper selection and behavior. Configure `rootless_port_forwarder = "pasta"`. Missing Pesto, incompatible helpers or silently selected rootlessport fail prerequisites; do not downgrade.

Ubuntu 26.04 remains the reference operating system, but its default Podman 5.7 packages are insufficient. The administrator provisions supported prerequisites; Gordon neither performs privileged package installation nor silently replaces an existing engine. The temporary source-build procedure is development evidence, not an approved production distribution channel.

### Ownership

Keep four independent rootless containers: control, runtime, edge and registry. Control reserves and journals exact authorized mappings. Runtime executes/reconciles Pesto forwarding to observed edge container addresses. Edge serves container-network listeners and cannot authorize public mappings.

The host executable owns generated Quadlets; systemd supervises the four roles. Do not add a Gordon host traffic process, host lifecycle executor for port changes, generic systemd API or parallel rootlessport path. Routine mapping changes use Pesto rather than recreating edge or rewriting `PublishPort` entries.

Direct Pesto forwarding to application TCP/UDP endpoints is a maintainer suggestion awaiting a focused contract, neither accepted nor rejected here. It could reduce edge's transport role, but must assign client policy, readiness, withdrawal and transport cleanup currently owned by edge. This ADR accepts the required stack and dynamic publication mechanism, not a silent bypass of edge.

The publication contract must establish a single mapping owner. Dynamic application mappings must not be duplicated in static Quadlet `PublishPort` declarations that could recreate withdrawn binds. Minimal bootstrap of the rootless network/pasta instance and any system listeners must be specified, including startup ordering when no public mapping exists.

Pesto's socket is a host forwarding capability. Only runtime may receive the Gordon-side control capability; edge, registry, control and workloads must not receive it. Podman's trusted host processes retain their native access. Exact socket directory mounts, peer permissions, command/protocol subset, engine-wide reach and compatibility with Podman's own setup/teardown are proof gates, not assumed implementation details. Do not expose an arbitrary Pesto command proxy.

### Source identity and upstream proxies

For direct traffic, pasta/Pesto preserves the source observed at the host to the edge container on the tested paths. A preceding proxy, subnet router or NAT may already replace the original source; no downstream mechanism reconstructs it.

Gordon works without an upstream reverse proxy, including on private tailnets. HTTP identity comes from the socket in direct mode, or sanitized headers on an explicitly restricted/authenticated upstream path. Never trust arbitrary client headers. Cloudflare and NetBird remain optional.

Ordinary edge-to-backend proxying still creates a new connection. HTTP can convey verified identity in headers; original client source in backend TCP/UDP sockets is not promised. Source-preserving publication alone does not introduce raw-transport `trusted_cidrs` into the alpha manifest: any such feature still needs a focused policy contract and negative tests.

### Recovery and withdrawal

Runtime reconciles control-authorized applied generations with current edge identity, addresses, networks and observed Pesto state. Container addresses may change after stop/start; never treat a historical container IP as durable destination authority. Prevent existing forwarding rules from reaching an address reassigned outside the authorized destination identity, rather than relying only on eventual reconciliation. The enforcement mechanism remains part of the publication contract.

Define durable operation IDs, generations, unknown outcomes, ordering and observe-before-resume behavior. After edge/pasta/runtime restart or reboot, restore only authorized applied mappings; never pending AppSpecs, withdrawn routes or stopped apps. Do not persist UDP sessions.

Rule deletion, closing admission and terminating existing traffic are separate observations. Reservations remain held until mapping withdrawal and bounded TCP/UDP cleanup are established. Pesto removing a rule is not evidence that accepted connections or UDP flows are gone.

Port-set changes no longer inherently require edge-wide interruption. The experiment does not establish uninterrupted long-lived streams or production recovery. Edge failure still interrupts its TCP connections and loses its UDP sessions; disruptive recovery remains reportable, never transparent by promise.

## Superseded and retained requirements

This ADR supersedes ADR-004's prohibition on the pasta/Pesto path, rootlessport/NAT baseline and assumption that published-port changes require edge recreation. Historical A1A.0/A1A.1 negative results remain valid for their tested stacks/candidates, not universal impossibility proofs.

All other boundaries remain: rootless only, no shared pod, no host network/PID/IPC, no firewall/sysctl mutation, no privileged Gordon setup, Podman only in runtime, private stores, write-only secrets, digest-pinned images, app-private networks and no component update/migration scope. The socket proposal in ADR-005 must be extended and reviewed for Pesto before acceptance.

## Reference-host AppArmor boundary

The maintainer accepts proceeding on Ubuntu 26.04 without a per-container AppArmor profile on the selected rootless stack. Podman 6.1.1's vendored `common/pkg/apparmor/internal/supported` explicitly rejects rootless mode; its profile selection also rejects a requested profile in rootless mode. Compiling with the `apparmor` build tag does not remove this limitation. See [the upstream discussion](https://github.com/containers/common/issues/958).

Keep host AppArmor enabled and preserve applicable host policies. Do not patch out Podman's checks, switch to rootful execution, or disable an LSM to pass a proof. Container acceptance instead requires observed user-namespace isolation, dropped capabilities, no-new-privileges, seccomp, read-only roots, least-authority mounts, network separation and resource bounds, including negative authority tests. These layers are not equivalent to a restrictive AppArmor profile; the missing container policy is an accepted defense-in-depth limitation, not a passed LSM proof.

This exception supersedes requirements for active per-container AppArmor on the Ubuntu reference stack in ADR-005 and the foundation plans. It does not relax SELinux on other supported hosts or close any other F1/F2 gate. Reconsider per-container AppArmor when upstream supports the required rootless path, with fresh positive and negative enforcement tests; no compatibility scaffolding is required now.

## Proof gates and critical questions

- Prove runtime-container access to the narrowly mounted Pesto capability and denial from edge/apps; quantify its authority over the shared rootless engine.
- Establish ownership coexistence with Podman setup/teardown and restoration after pasta recreation; direct host CLI commands are not the production boundary.
- Test stale destination IPs, address reuse by unrelated workloads, overlapping/dual-stack conflicts, failed mutations, lost responses, restart/reboot and stopped-intent recovery.
- Test actual established TCP and UDP behavior on deletion, bounded cleanup, UDP epochs, binary fidelity, multiple/unsolicited replies, saturation and backpressure.
- Verify HTTP header spoof/bypass rejection separately from direct source preservation.
- Verify the reference-host protections and negative authority tests described above; report host AppArmor state separately from the accepted absence of a per-container AppArmor profile.
- Measure performance and private registry pulls; run the full four-role and installer acceptance suite before declaring Alpha 1 ready.

The stack selection is accepted. These outstanding contracts/proofs govern safe implementation; they do not restore an abandoned fallback or authorize declaring the experimental stack production-ready.
