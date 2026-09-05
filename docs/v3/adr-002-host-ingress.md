# ADR-002: Use a confined, transport-only host ingress for TCP and UDP

- Status: Accepted direction, amended to replace TCP descriptor handoff; implementation and public use remain gated below
- Date: 2026-09-05
- Amends: [ADR-001](adr-001-v3-foundation.md), for ingress ownership, transport and component lifecycle
- Related: [V3 design](design.md)

## Context

Rootless Podman remains responsible for four isolated containers and their
private networks. On the tested named-bridge stack, ordinary port publication
rewrites the client address before edge. Direct pasta networking preserves it
but cannot be combined with those bridges through the tested configuration.
Startup socket activation does not provide live listener changes. These are
limitations of the tested paths, not general claims about rootless Podman.

The initial direction transferred host TCP listeners to edge and retained UDP
sockets in ingress. Security validation invalidated the TCP assumption: a
validated listening socket could be repurposed with `connect(AF_UNSPEC)` and
used to reach host loopback from edge, despite network isolation, zero effective
capabilities, no-new-privileges and the tested seccomp filter. A retained duplicate
also kept the port bound after a false withdrawal acknowledgement. Previously,
transferred UDP sockets had demonstrated unwanted host-network send authority.

The amendment therefore removes host-network descriptor transfer entirely.
Ingress transports traffic; edge retains application intelligence. UDP deployment
uses recreate with interruption, not live game-session migration. This decision
does not change manifests, apply/deploy or introduce a second app-port catalogue.

## Decision

### Keep four containers and one transport-only host role

Retain independent rootless `control`, `runtime`, `edge` and `registry` containers.
Ingress runs as a confined, non-root host process from the same installed Gordon
executable. It is not another binary or independently versioned artifact.

The host installer owns four Quadlets and an ordinary `systemd --user` ingress
service under the current lifecycle baseline. Systemd supervises all five roles.
Distribution identity, installation locking, journaling, generated-file checksums,
readiness, lingering and same-generation recovery cover all five. Updates and
component rollback remain deferred under ADR-001.

The exact OS confinement mechanism remains undecided and must be proven before
public use. This amendment does not select a dedicated host account, change the
service-manager ownership or authorize privileged ingress. If confinement needs
such an installation change, decide and record it before implementing that change.

```text
control -- authorize listener operations --> ingress
                                              |
client -- TCP/UDP --> ingress -- private Unix transport --> edge --> backend
                     owns host sockets                    TLS and routing
```

| Role | Responsibility |
| --- | --- |
| control | Validate/reserve routes; authorize listener operations for a journaled operation and generation |
| ingress | Own host sockets; relay TCP bytes and UDP datagrams; attach kernel-observed identities; enforce transport resource bounds |
| edge | Application TLS, HTTP/TCP/UDP routing, backend selection, CIDR policy and backend connections |
| runtime | Workloads and app-ingress networks; no new host-network or component-management authority |
| registry | OCI operations and independent registry TLS identity, unchanged |

Ingress does not interpret HTTP, SNI, domains, certificates, app manifests or game
protocols. It neither selects backends nor applies `trusted_cidrs`. It cannot read
secrets, invoke Podman/systemd management, launch components or mutate desired
state. It receives only the minimal authorized listener/transport generation
projection. Transport limits are not application routing policy.

### Leave the public firewall to the administrator

The administrator owns firewall policy and privileged-port redirects, for example
public TCP 443 to local 8443. Gordon binds the configured local destination; it
never changes firewall rules or host sysctls, or silently elevates privileges.
An unauthorized bind fails clearly.

App routes remain the only workload exposure declarations. Public-to-local
translation and reservation checks must distinguish the public and local address
without duplicating app ports in installation configuration. The exact mapping
syntax remains an implementation prerequisite.

### Separate administration from the transport capability

Control alone authorizes journaled listener operations: creation, activation,
invalidation and removal, including rollback and recovery, using the operation's
captured authorized release/applied state and generation. Administration uses a
dedicated private capability; apply remains mutation-free. Recovery reconciles
previously authorized applied state, not a new authorization from edge. Edge's
data channel cannot authorize binds, choose arbitrary host-network destinations
or invoke ingress administration.

Ordinary control APIs remain strict HTTP/JSON over role-specific Unix sockets.
The data path uses dedicated Unix IPC for TCP streams and framed UDP datagrams,
not internal TCP APIs, gRPC, generic RPC or a general forwarding service.
**No host-network descriptors, listening or connected, are passed to edge.**
The framing, capability paths, peer validation, UID mappings and versioning still
require a focused protocol design and tests; no particular multiplexing scheme
is selected here.

Both transports carry the kernel-observed client and local destination identity,
with family/interface/scope where required, bound to their listener and transport
generation. Edge trusts this identity only from the authenticated ingress
capability, never from a client-supplied payload/header. Authentication here means
validated capability ownership and peer identity, not new bearer-token plumbing.
Edge uses that identity for `trusted_cidrs`. Neither transport undoes prior NAT;
ordinary edge-to-backend proxying still exposes edge's address at the backend.
Backend original-source requirements remain a separate gate.

### TCP: relay opaque streams, retain all host sockets

Ingress owns each authorized listener and each accepted host connection. It relays
bytes bidirectionally between that connection and edge's private Unix transport.
Replies go only to that existing client connection; edge cannot request outbound
host connections. TLS remains opaque to ingress, and edge retains TLS termination
or registry SNI passthrough as appropriate.

The relay must preserve byte order and half-close semantics, bound buffers and
connections, apply backpressure, and clean up on cancellation or peer failure.
Ingress can close its listener without relying on edge to relinquish a descriptor.
Stopping acceptance is distinct from draining or terminating existing connections;
withdrawal must account for both with bounded cleanup. This is transport plumbing,
not an HTTP proxy or a second routing engine.

### UDP: bounded associations, disruptive lifecycle

Ingress retains each UDP socket. A datagram from a client can create a temporary
association of listener, generation, kernel-observed peer and local destination.
Ingress forwards payload and identity to edge; edge chooses the backend.
Edge may return datagrams only for a live ingress-owned association. It cannot
invent sessions or select arbitrary host destinations. Replies use that
association's original peer and observed local source address/interface.

Preserve datagram boundaries and binary payloads. Support multiple and unsolicited
backend replies within a live association, not just one request/one response.
Bound datagram/frame sizes, queued bytes, sessions, idle lifetimes, response
amplification and work per client; define overload and malformed-peer handling.
These limits remain necessary even when deployments may interrupt players.

UDP services use recreate. Each affected listener has a non-reused association
epoch, independent of unrelated listeners and the global edge route generation.
The journaled operation first stops new association admission and forwarding for
that listener, then invalidates its old epoch and associations in ingress and
edge before replacing/stopping the backend. Admission stays closed during the
replacement. Only after the replacement backend and route are ready may control
authorize admission under a fresh epoch. Late backend/IPC responses from old epochs
remain rejected, including across restart; the protocol must prevent epoch reuse.
Selecting the restart-safe epoch allocation mechanism is a prerequisite of the
focused IPC/persistence design, not an invitation to persist UDP sessions. Its
tests must restart processes and inject an old response to verify non-reuse and
rejection. This amendment does not select a storage format or allocation algorithm.
Unrelated apps' associations are not invalidated by that deployment.

There is no session migration, transparent cutover, persisted UDP session state
or session restoration after ingress/edge restart or host reboot. Clients recover
according to their own protocol. A fresh client datagram may have been delayed
in the network; ingress does not parse game protocols to determine its age.
Listener/route recovery remains required, but always starts with empty sessions.

### Preserve apply/deploy, reservations and execution intent

Apply persists desired configuration only. Deploy activates only its captured,
validated release after runtime, edge route generation and ingress relay readiness
are confirmed. Control journals operations and retains reservations across desired,
active and in-flight state; a desired removal alone does not free an active port.

For a route on a shared HTTP/SNI listener, edge acknowledges its route-generation
withdrawal and owns route-level draining/rejection on existing streams. Ingress
cannot identify application routes in opaque TCP and must not close unrelated
streams or the shared listener to certify that route's withdrawal. The host/route
reservation may be released after that acknowledgement and removal of all desired
and in-flight references; the shared listener reservation remains.

Withdrawal of a dedicated listener or the final authorization for a shared
listener additionally requires ingress confirmation of listener closure and
bounded cleanup of its accepted connections/UDP associations. A false ACK from
edge cannot retain a host descriptor, but cannot prove ingress cleanup either.
Timeout, refusal or uncertain cleanup fails withdrawal and retains the applicable
reservation. Reassignment requires verified release; uncertain ownership may
require operator-controlled installation recovery. This grants neither runtime
nor ingress new component-restart authority.

Recovery reconciles authorized applied state with observed ingress/edge state,
not arbitrary bind requests from edge. It must not activate a pending AppSpec or
revive a stopped app. Historical releases do not reserve listeners indefinitely;
rollback reacquires and validates reservations before mutation. Minimal persisted
listener metadata and interrupted-operation reconciliation remain protocol and
persistence requirements, separate from disposable connection/session state.

## Security and availability consequences

Ingress is now on the TCP and UDP data paths. The extra TCP relay adds I/O, CPU
and buffering and must be benchmarked; no negligible-overhead claim is made.
Its failure interrupts relayed TCP connections, including registry passthrough,
and loses UDP sessions. This availability trade-off is accepted instead of giving
edge host-network descriptors. No zero-downtime ingress restart is promised.

Ingress remains a public attack surface even without application parsing. Non-root
and `NoNewPrivileges` alone do not isolate it from the Podman account's files and
processes. Tested user-service filesystem properties left forbidden canary access
possible; stronger tested profiles failed before execution with capability errors
and AppArmor denials. These profiles are not an acceptable fallback, and their
failure does not prove that no OS confinement solution exists.

Before public use, prove denial of secrets, Podman sockets/storage, control-private
state, unauthorized writes and process/filesystem escape paths, while required
binds and private transport remain usable. Do not weaken AppArmor/SELinux, seccomp
or container isolation to obtain a working prototype. The relay removes the
observed descriptor capability from edge; it does not establish ingress confinement.

| Failure | Required behavior |
| --- | --- |
| control unavailable | Already active TCP/UDP traffic continues; mutations fail clearly |
| ingress unavailable | Relayed TCP connections terminate; UDP sessions are lost; public traffic and new binds are unavailable |
| edge unavailable | Public traffic fails closed; affected TCP relays close and UDP associations are invalidated |
| ingress restart | Restore only authorized listeners and relay readiness, with new connections/empty UDP sessions; do not restart healthy edge |
| edge restart/reboot | Recover authorized listeners/routes with new connections/empty UDP sessions; never activate pending configuration or stopped apps |

Registry TLS stays independent of edge and ingress. The certificate-impersonation
proof gate is unchanged. No zero-downtime UDP deployment or game compatibility
claim follows from this transport design.

## Evidence and remaining gates

The descriptor experiments justify rejecting TCP/UDP host-socket handoff, not a
claim that the replacement relay is implemented or secure. Earlier UDP
request/response and manual replay tests do not establish general bidirectional
sessions or automatic recovery. Product reservation persistence/recovery has not
been verified by those experiments.

Proceed with a narrow transport implementation and focused checks, not workload
features built on an assumed boundary. Alpha 1 completion and public use still
require host confinement, capability permissions, TCP relay correctness and
resource bounds, authenticated source identity, dynamic listener withdrawal,
non-cooperative peer handling, interrupted-operation and restart/reboot recovery,
IPv4/IPv6/address conflicts, firewall coexistence, full-path CIDR allow/deny and
private runtime-to-registry access. UDP exposure additionally requires binary
fidelity, bidirectional associations, stale-generation rejection, disruptive
recreate/restart tests and measured limits/performance. Live UDP session migration
and restoration are explicitly not gates or planned alpha features.

## Alternatives not selected

- Ordinary rootless bridge publication alone: failed the tested source-address requirement.
- Startup socket activation alone: does not provide live listener changes.
- Host TCP/UDP descriptor transfer: grants unwanted host-network authority; receipt validation is insufficient.
- Application-aware host proxy: duplicates edge's TLS/routing responsibility; ingress is opaque transport only.
- Live UDP session migration or durable session recovery: unnecessary for the accepted recreate lifecycle.
- Privileged veth/DNAT integration: privileged lifecycle ownership remains unresolved.
- Edge with host networking: violates the retained container isolation contract.

## Related

- [Foundation ADR](adr-001-v3-foundation.md)
- [V3 design](design.md)
- [Development environment](../../dev/v3/README.md)
