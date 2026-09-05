# ADR-002: Add a confined host ingress role for TCP handoff and UDP relay

- Status: Accepted direction; implementation and public use remain gated below
- Date: 2026-09-05
- Amends: [ADR-001](adr-001-v3-foundation.md), for ingress ownership, transport and component lifecycle
- Related: [V3 design](design.md)

## Context

The app manifest already declares TCP/UDP listen addresses. Apply persists
configuration only; full deploy activates the configured release and routes.
This decision does not change that contract or introduce a second app-port list
in installation configuration.

Rootless Podman remains responsible for containers and their private networks.
On the tested named-bridge stack, ordinary port publication rewrites the client
address before edge. Direct pasta networking preserves it but cannot be combined
with those bridges through the tested Podman configuration. Native systemd
socket activation delivers listeners at startup, not new listeners to an
already-running edge. These are limitations of the tested paths, not a claim
that rootless Podman is unsuitable or that future versions cannot improve them.

A host process can instead transfer TCP listeners to edge over Unix IPC. Passing
host UDP sockets also works, but gives edge additional host-network authority:
a prototype used a transferred UDP socket to reach host loopback, inaccessible
through edge's ordinary container socket. Host UDP descriptors therefore must
not be transferred to edge.

## Decision

### Keep four containers; add one narrowly scoped host role

Retain the independent rootless `control`, `runtime`, `edge` and `registry`
containers. Add a logical **ingress** role running from the installed Gordon
executable as a non-root host process. Its exact CLI spelling remains an
implementation detail; it is not another binary or independently versioned
artifact.

The host installer generates four Quadlets and one ordinary `systemd --user`
service for ingress. Systemd supervises all five roles. Ingress neither launches
nor supervises components and cannot invoke arbitrary commands, Podman, systemd
management operations, secret storage or desired-state mutation APIs.

All five roles must match the distribution identity. Installation locking,
journaling, generated-file checksums, readiness, dependency checks, lingering
and same-generation recovery cover the ingress service as well as the Quadlets.
Updates and component rollback remain deferred under ADR-001.

### Leave the public firewall to the administrator

The administrator installs/configures firewalld or an equivalent firewall,
authorizes public traffic, and provisions any privileged-port redirections.
For example, public TCP 443 may be redirected to a host listener on 8443. Gordon
binds the configured local destination; it does not manage firewall rules,
change host sysctls or silently elevate privileges. An unauthorized local bind
fails with an actionable error.

App routes remain the sole workload exposure declarations. Installation settings
cover shared system listeners and host-level policy, not a duplicate catalogue
of app ports. The precise representation of public-to-local address translation
and its participation in listener-conflict validation must be settled before
implementation; do not assume public 443 and local 8443 are the same reservation.
That representation must derive or express the translation without introducing
an installation-level catalogue of ports for each app.

### Separate listener acquisition from application routing

| Role | Responsibility |
| --- | --- |
| control | Validate and reserve routes; authorize listener changes for a journaled operation and generation |
| ingress | Bind authorized host listeners; hand off TCP; relay bounded UDP traffic |
| edge | TLS, HTTP/TCP/UDP routing, backend selection, CIDR enforcement and connection/session lifecycle |
| runtime | Workload and app-ingress networks; no new host-network or component-management authority |
| registry | OCI operations and independent TLS identity, unchanged |

Control alone authorizes listener creation/removal through a dedicated private
capability. Edge's data channel grants no permission to bind host addresses,
change destinations arbitrarily or invoke ingress administration. Ingress
receives a minimal listener projection, not complete manifests or app secrets.

Ordinary control APIs remain strict HTTP/JSON over Unix sockets. This ADR adds
a narrowly scoped Unix IPC exception for descriptor transfer (`SCM_RIGHTS`) and
framed UDP traffic. It does not introduce gRPC, internal TCP RPC, a generic
message bus or a general-purpose forwarding API. Capability paths, peer checks,
UID mappings, frame formats and versioning require the implementation proof.

### TCP: transfer the listener, not the traffic

Ingress binds an authorized TCP listener and transfers it to the running edge.
Once ownership is acknowledged, ingress closes its duplicate. Edge accepts the
connections directly and performs TLS and routing. This avoids an additional TCP
proxy hop and preserves the peer address supplied by the host network; it does
not undo source rewriting already performed by external NAT/proxies.

Track every descriptor through transfer, acknowledgement, rejection and timeout.
The production protocol must handle lost acknowledgements, duplicate operations
and either process crashing mid-transfer without leaking listeners or declaring
an uninstalled generation active. Native startup activation is not a substitute
for this live handoff protocol.

Before accepting a descriptor, validate that it is a TCP stream socket in
`LISTEN` state, with the authorized address family and local bind address/port
for that operation and generation. Reject other socket types, connected sockets,
wrong families/addresses and unexpected descriptors. Successful handoff is not
proof that a host TCP descriptor is safe: negative tests must characterize and
constrain the host-network operations a compromised edge can perform through
it, including attempts to repurpose it for outbound connections. If the retained
authority violates containment, the handoff design remains blocked.

### UDP: retain the host socket and constrain replies

Ingress retains each UDP socket. It records the kernel-observed client and local
destination addresses for each datagram, including address family and IPv6 scope
or receiving interface where needed. It forwards payload and this identity to
edge via the private data channel. Edge uses that authenticated
metadata for `trusted_cidrs`, then routes to the backend. This is not a claim
that edge observes the original client through a local `ReadFrom` call.

Ingress may send responses only to the original client associated with a live,
ingress-owned session. Edge must not choose an arbitrary host-network destination
or create a session without an incoming datagram. Session identifiers must be
bound to the listener generation, peer, observed local destination and lifetime;
stale or unknown identifiers must fail closed. Ingress selects the reply's local
source address/interface from that kernel-derived tuple, not from edge input.
Wildcard binds must retain the actual destination address rather than treating
all local addresses as interchangeable. Test multi-address IPv4/IPv6 and scoped
interfaces. Sender-controlled payload fields are never peer or local identity.

Production UDP requires binary-transparent, bidirectional sessions, including
multiple and unsolicited backend responses within a valid session. Bound frame
sizes, queued bytes, sessions, idle lifetimes, response amplification and work
per client. Define backpressure and malformed-peer behavior before implementation.
The successful one-request/one-response prototype is not a game-server proxy.

### Preserve apply/deploy and reservation semantics

Apply does not bind sockets or change ingress. Deploy activates only its captured,
validated release configuration; it does not invent routes. Listener handoff and
UDP relay readiness participate in the operation journal and activation checks.

Control retains reservations across desired, active and in-flight state. Route
removal requires edge withdrawal acknowledgement and ingress confirmation that
host ownership/session state has been released as appropriate. Historical
releases do not retain bindings; rollback reacquires and validates them. No
restart or recovery may bind a merely pending AppSpec or revive a stopped app.

TCP withdrawal acknowledgements describe cooperative operation only. Once ingress
has closed its copy, it cannot revoke edge's descriptor or certify that edge
closed every copy merely by receiving an ACK. Timeout, refusal or uncertain
closure fails the operation and retains the reservation. Non-cooperative recovery
requires stopping the descriptor-holding processes through the existing,
operator-controlled installation lifecycle and verifying release before
reassignment; an edge restart must not restore the withdrawn listener. This does
not authorize runtime or ingress to restart edge during workload reconciliation.
Tests must cover retained/duplicated descriptors and failed withdrawal.

Recovery must reconcile authorized applied state with observed ingress and edge
state, not trust edge to request arbitrary new binds. Minimal persisted listener
metadata and crash reconciliation require a focused protocol/persistence design;
the prototype's manual replay is not automatic recovery.

## Security and availability consequences

Ingress is Internet-facing through UDP and is therefore part of the public
attack surface. It must not become an unconfined process under the trusted
Podman account: that would recreate the v2 secret/socket exposure risk. Non-root
execution and `NoNewPrivileges` alone do not prevent same-account file access.

Before public use, prove an OS-enforced service sandbox denies application
secrets, control-private state, Podman sockets/storage, unauthorized filesystem
writes and escapes through `/proc`, other processes or inherited descriptors.
Keep allowed capability directories separate. Exact systemd/LSM isolation must
be tested on the reference host; if insufficient, stop and revisit the execution
boundary rather than accepting this risk implicitly. Do not disable AppArmor,
SELinux or seccomp to make the service work.

| Failure | Required behavior |
| --- | --- |
| control unavailable | Already active TCP/UDP traffic continues; listener mutations fail clearly |
| ingress unavailable | Handed-off TCP listeners/connections can continue; UDP is unavailable; new binds fail |
| edge unavailable | Application traffic is unavailable; fail closed; recover only authorized applied state |
| ingress restart | Reconcile TCP ownership and restore authorized UDP listeners without restarting healthy edge |
| edge restart/reboot | Reconcile listeners, sessions and routes without activating pending configuration or stopped apps |

UDP now depends on ingress throughput and availability as well as edge. No
zero-downtime UDP promise is added. Backend original-source preservation remains
unproven: ordinary proxying exposes edge's address to the backend. Registry TLS
passthrough and the separate certificate-impersonation gate remain unchanged.

## Evidence and remaining gates

Two bounded disposable-VM prototypes supplied different evidence:

- The initial TCP-and-UDP descriptor-transfer prototype demonstrated live changes,
  TCP/UDP source delivery, traffic continuity after ingress termination, listener
  release after edge termination and explicit replay. Its UDP handoff was rejected
  because it granted edge host-loopback send authority.
- The selected-direction prototype kept UDP on the host and transferred only TCP.
  It demonstrated live changes, UDP peer metadata, TCP continuity but UDP loss
  after ingress termination, and manual UDP resumption. A forged UDP reply
  destination was ignored while the real client received the response. Edge
  termination/recovery with this revised UDP relay was not tested.

Neither prototype demonstrated automatic durable recovery or complete TCP
capability confinement. The UDP tests were textual request/response exchanges,
not general bidirectional sessions.

These results support the direction, not production readiness. Alpha 1 remains
blocked on confined host-role execution, capability permissions, TCP descriptor
validation and negative authority tests, non-cooperative withdrawal, UDP local
address/session identity, interrupted-transfer recovery,
automatic restart/reboot behavior, IPv4/IPv6 and address conflicts, firewall
coexistence, full-path CIDR allow/deny, and private runtime-to-registry access.
General UDP sessions, binary fidelity, limits and performance must be proven
before UDP exposure is enabled. The observed Podman/pasta AppArmor cleanup
failure is a separate platform issue, not solved by this design.

**Recommendation:** keep the implementation narrow: TCP descriptor handoff plus
UDP session relay, no host firewall management, no generic supervisor, and no
routing/TLS logic duplicated outside edge. Build and verify these boundaries
before implementing workload features on top of them.

## Alternatives not selected

- Ordinary rootless bridge publication alone: failed the tested source-address requirement.
- Startup socket activation alone: does not provide live listener changes.
- Transfer host UDP sockets to edge: grants unwanted host-network send authority.
- General TCP/UDP host proxy: adds a TCP data hop and source-metadata protocol unnecessarily.
- Privileged veth/DNAT integration: promising transport proof, but privileged lifecycle ownership is unresolved.
- Move edge to host networking: violates the retained container isolation contract.

## Related

- [Foundation ADR](adr-001-v3-foundation.md)
- [V3 design](design.md)
- [Development environment](../../dev/v3/README.md)
