# Multi-host evolution: design review

Status: planning note; not a cluster specification or an amendment to accepted ADRs

Date: 2026-09-05

## Purpose and scope

Keep the initial v3 implementation single-host while avoiding unnecessary coupling
that would make a future cluster require rewriting its core. This review concerns
the accepted design, not implementation readiness: the inherited v2 code is not
evidence that these boundaries already exist in v3.

A possible first cluster use case is one control plane deploying the same web
service release to two hosts, with a load balancer above their edges. The web
instances use one logical application database, either external to Gordon or
managed on a specific host. This improves web capacity; it does not by itself
provide database, load-balancer, or control-plane high availability.

Cluster operation remains outside the initial v3 scope. This note adds no manifest
fields, remote APIs, scheduler, network overlay, replication mechanism, or delivery
milestone. Accepted apply/deploy, rollback, reservation and containment semantics
remain unchanged. Consequential cluster decisions require focused ADRs and a
critical review before implementation.

## Existing boundaries worth preserving

| Design area | Existing baseline | Evolution constraint |
| --- | --- | --- |
| Service identity | A service is a logical workload; a container is not its stable identity. | Preserve this distinction. Multiple instances must not require inventing duplicate services or manifests. |
| Releases and operations | Releases capture immutable effective configuration and digests; operations journal effects and observed results. | Keep release identity separate from container identity and operation outcome. A future shared release could have different deployment results on different hosts. |
| Control and runtime | Control owns desired state; runtime owns Podman and observed local resources. | Keep Podman IDs, local paths and transport mechanics at their owning boundary, not in logical app identity. |
| Routing | Routes target stable entrypoints; edge consumes a sanitized projection. | Keep logical routing separate from resolving live backend addresses. Do not make a container IP the stable route identity. |
| Host ingress | Ingress owns host sockets and relays opaque TCP/UDP traffic to its local edge over Unix IPC. | Keep this transport local; an upstream load balancer does not require ingress-to-edge IPC to become a cross-host API. |
| Persistent data | A volume belongs to a service; releases reuse it and rollback does not roll back its contents. | Do not infer volume mobility or safe replication from a reusable service definition. |

These are implementation review criteria, not a request to introduce generic
cluster interfaces, node fields or transport factories before a concrete need.

## Mono-host assumptions that need future decisions

The current design is coherent for one host. The following assumptions must be
revisited explicitly if cluster work begins, rather than silently generalized.

| Current assumption | Why extending it is consequential |
| --- | --- |
| One active instance per service, except eligible web replacement | Replicas require explicit concurrency eligibility, membership, routing eligibility and draining. A volume-free HTTP service may still run migrations or background jobs. |
| One active app release and a locally coordinated deployment journal | A network partition can leave hosts on different releases. Cluster status must represent partial rollout and unknown observations, not claim atomic success or treat an unreachable instance as stopped. |
| Installation-wide reservations and edge publication | A cluster needs defined public reservation authority and host-local bind ownership. Preserve desired/active/in-flight protection; do not merely weaken uniqueness to allow replicas. |
| Unix-socket possession grants local capabilities | A remote runtime needs authenticated node identity, scoped authorization, replay protection and failure semantics. Existing SSH administration is not a cluster protocol. |
| Local runtime stores secret values | Remote placement needs explicit secret delivery, ownership and revocation. Do not solve it by adding secret reads or placing values in edge snapshots. |
| Stable local Podman volumes and service-name DNS | Neither makes data or service discovery portable between hosts. Placement and connectivity need explicit treatment. |
| One private runtime-to-registry path and matching installation generation | Remote pulls and component version compatibility need their own reachability, trust and rollout decisions. |
| One systemd-managed installation | Each host can retain local supervision; cluster workload reconciliation must not become an implicit generic component supervisor. |

A future load balancer could run above the host-local edges. Its location, TLS
ownership, trusted client-address propagation, health checks, TCP/UDP behavior and
configuration authority are not selected. Static TOML might describe topology,
but an orchestrator must not depend on a second manually maintained list of live
replicas. SSH forwarding is an option to evaluate, not a commitment; ordinary SSH
port forwarding neither transports UDP nor preserves original client addresses.

## Application database and other state

The first candidate multi-host workload is a web service whose instances can
safely run concurrently and do not depend on instance-local persistent state.
They share one logical application database:

- An external database delegates its operation and availability to its operator.
- A Gordon-managed database attached to one host remains a single failure point.
  Losing that host must not silently create an empty replacement database or
  start another writer while the old one may still be running.
- Multiple database containers are not database replication. High availability
  requires engine-specific replication, promotion, fencing, backup and restore
  procedures; it is not implied by a replica count.

Gordon's own no-external-database policy concerns Gordon's internal persistence,
not whether hosted applications can use an external database.

Sessions, uploads, caches, scheduled jobs and database migrations also need
explicit concurrency treatment. A shared database alone does not make an app
safe to replicate. Future rolling deployment must account for schema
compatibility across old and new instances; this note does not select a migration
runner or promise exactly-once execution.

## Network isolation: app boundary

The current product model is `app -> services -> runtime containers`. An app can
contain a web service and its database service. The accepted design gives all
services in an app one private network, permits explicit named networks across
apps, and gives edge access only to routed services through an ingress network.

The discussed cluster direction must not introduce a cluster-wide application
network granting unrelated workloads implicit access. Transport between cluster
components must not itself grant application network membership or access to
database ports.

The maintainer confirmed that the intended isolation boundary is the app, not
an individual service: web and database services within the same app may
communicate, while unrelated apps receive no implicit access. Future multi-host
connectivity should preserve that boundary across hosts. This does not select a
cross-host networking mechanism or remove the accepted, explicit named-network
opt-in for communication between apps.

## Critical checks and limits

- **Premature abstraction:** preserve existing logical boundaries; add no unused
  cluster machinery to Alpha 1.
- **False availability claims:** two web replicas do not remove shared database,
  load-balancer or control-plane failure points.
- **Unsafe failover:** a timeout is not proof that a remote process has stopped.
  Placement and recovery must address stale ownership before automated failover.
- **Boundary erosion:** remote orchestration must not expose Podman, secrets or
  control-private state to the load balancer, edge or ingress.
- **Compatibility promises:** preserving these boundaries reduces coupling; it
  does not guarantee unchanged manifests, APIs or persistent formats in a future
  cluster version. Those migrations remain separate decisions.

Before cluster implementation, obtain a focused ADR and independent critical
review for the chosen topology, authority, partition behavior and data policy.
No new cluster behavior or security proof is approved by this planning note.

## Related

- [Accepted v3 design](design.md)
- [ADR-001: v3 foundation](adr-001-v3-foundation.md)
- [ADR-002: host ingress](adr-002-host-ingress.md)
