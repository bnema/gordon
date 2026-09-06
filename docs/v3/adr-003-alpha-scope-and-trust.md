# ADR-003: Keep alpha rootless and proportionate, with explicit trust and storage contracts

- Status: Accepted product decisions; implementation and listed proofs outstanding
- Date: 2026-09-06
- Decision owner: Gordon maintainer
- Amends: [ADR-001](adr-001-v3-foundation.md) and [ADR-002](adr-002-host-ingress.md)
- Related: [Consolidated design](design.md), [implementation plans](plans/README.md)

## Context and precedence

Gordon is a small single-host deployment tool for an administrator who chooses the hosted applications, not a hostile multi-tenant hosting service. Applications may nevertheless be compromised. Their lack of direct access to Gordon's administration, engine and private storage remains essential.

The earlier baseline combined this boundary with stronger promises: registry confidentiality even after edge compromise, a separate proof of application concurrency safety, and a mandatory fifth host role. The maintainer selected the narrower contracts below after reviewing their operational cost. These decisions replace conflicting requirements in ADR-001/002; the original text remains historical evidence, not an additional gate.

This ADR records decisions, not completed implementations. It does not select untested native networking, an ingress sandbox, exact TLS modes, database schemas or a secret ciphertext format.

## Decisions

### 1. Trusted but fallible edge; explicit app containment

Control, runtime and the host account are trusted. Edge is trusted to terminate and proxy application **and registry** traffic. An external TLS terminator is likewise trusted for traffic it decrypts.

A compromised edge can read/alter terminated traffic and steal passwords, sessions or OCI credentials. Stolen push credentials combined with an enabled auto-deploy selector may execute malicious code in that service and expose its injected secrets. This indirect compromise risk is accepted; do not promise that edge compromise cannot expose any application secret.

Keep the direct authority boundary: edge and hosted apps have no Podman socket, control/runtime administration capability or direct access to Gordon-private data or runtime's secret store/key. Edge receives a sanitized projection, not full AppSpecs. It may necessarily reach routed service ports; network adjacency is not a grant of administration authority. Do not claim that sharing a network restricts traffic to only the declared port.

An app has its private network and no implicit cross-app access; explicitly named networks remain the opt-in exception. A compromised service can attack reachable peers in its own app, and use its own injected secrets. It cannot directly mount another service's volumes or read that service's secret store. Rootless namespaces, LSM/seccomp, capability removal and least-authority mounts remain required. A shared-kernel exploit or runtime/host-account compromise can defeat these boundaries; no VM-strength isolation is claimed.

### 2. Registry is a private-by-default, explicitly publishable system service

Registry remains one of the four Gordon containers and owns OCI storage, authentication and its bounded event outbox. It is not an AppSpec, and app stop/remove/purge cannot manage it.

The private runtime pull endpoint remains available independently of public edge. Public exposure requires explicit installation configuration with a domain such as `registry.example.com`; it creates a reserved system route. No implicit exposure from an image label or image push. Public OCI operations require authentication and repository-scoped permissions; runtime credentials are pull-only and auto-deploy stays opt-in.

Edge may terminate registry TLS and forward requests to registry like other HTTP services. Registry confidentiality against edge, mandatory public SNI passthrough, and the proof that compromised edge cannot obtain a client-accepted registry certificate are **removed requirements**. Do not confuse credentials transiting edge with permission to mount registry's credential store.

TLS correctness still matters: validate endpoint identity and define certificate provisioning/renewal and origin trust before public traffic. The exact TLS configuration remains open. V2's internal CA and administrator-provided certificates are candidates, not automatically restored features. Cloudflare Full (strict) does not automatically trust a Gordon private CA. Trusted-proxy headers require an authenticated or otherwise securely restricted upstream path; an address rewritten by a rootless forwarder is not proof that a request came from Cloudflare/NetBird. Do not silently choose insecure TLS or unrestricted header trust.

### 3. bbolt for control; separately encrypted runtime secrets

Control persists transactional state in its own embedded **bbolt** database: normalized AppSpecs/revisions, immutable releases, durable execution intent, operation journals, reservations, secret metadata and removal tombstones. No SQL, sqlc, goose or external database. Atomic database transactions do not make Podman/network effects atomic; retain the observe-before-resume operation journal.

Runtime owns a **different, private bbolt database** for secret values. Encrypt values before writing them with a standard authenticated-encryption construction; AES-GCM is the proposed standard-library implementation, not a bespoke cipher. The focused secret contract must specify the envelope/version, nonce uniqueness, identity binding, key validation and bounded inputs before implementation. Values must never be persisted in plaintext in bbolt, control, releases or logs.

Store the random master key in a separate private directory, mounted read-only only into runtime. Use directory `0700` and key `0600` with ownership matching the proven rootless UID mapping. Installation initializes the key safely; runtime does not need a writable key mount. No key in the component image, TOML, environment variables or logs. Exact host/container paths are part of the socket/storage contract, not fixed by examples.

An existing secret database with a missing/wrong key or invalid ciphertext fails explicitly; never regenerate a key, silently empty the store or overwrite encrypted data. Initial creation and crash recovery must distinguish a truly new store from a damaged existing installation. Back up the key separately and securely; losing it makes recovery impossible. Automatic key rotation/rekey tooling is not introduced by this decision.

The protection is against disclosure of the database **alone**. It does not protect against runtime, the host account, a backup containing both database and key, or plaintext available to the intended application at runtime. bbolt metadata is not promised confidential. A copied encrypted database can still be stale: AEAD is not anti-rollback protection.

Version persistent formats and reject unsupported versions without mutation. Alpha remains fresh-install/same-generation recovery only; no migration framework or cross-generation upgrade is required. Other stores, including registry outbox and listener state, are not automatically assigned bbolt by this decision.

### 4. Automatic web overlap and bounded container shutdown

There is no concurrency opt-in field, image label, application-safety classifier or configurable strategy selector. For a service routed exclusively over HTTP/HTTPS and without persistent volumes, Gordon starts the new container, waits for Podman running and required HTTP entrypoint TCP acceptance, switches new traffic, then requests shutdown of the old container.

Application authors own graceful shutdown, duplicate/background work and old/new schema compatibility. A web process may also run jobs or migrations; Gordon does not detect that or promise exactly-once execution. Brief overlap and its application risks are accepted. Other services, including unrouted workers, pure TCP/UDP, mixed-protocol and volume-owning services, use stop-then-replace with interruption. Reconnection depends on clients.

Service configuration:

```toml
[services.frontend]
image = "shop/frontend:v1.4.0"
stop_timeout = "30s"
```

`stop_timeout` defaults to `30s`, accepts a finite positive duration and can be extended per service. Reject malformed, non-positive and unrepresentable durations; do not add an infinite-wait mode. It belongs to normalized effective configuration and immutable releases, not application environment. Use it consistently for stop, restart and replacement shutdown.

Request the configured container stop signal, wait up to the timeout, and force termination with SIGKILL if still running. Continue immediately on observed earlier exit. Do not issue a force kill against a replacement container due to stale identity. Journal/capture the shutdown target and bound total waiting across retries/recovery; the exact deadline representation belongs in the operation contract.

Switching new traffic, stopping a process and draining existing streams are different events. The rollout contract must specify existing keep-alive/HTTP2/WebSocket handling and bounded cleanup, without imposing a second unexplained full grace period or claiming that a stop signal proves network drain. Connections can be interrupted by the app or at the deadline. No generic readiness configuration or OCI `HEALTHCHECK` consumption is added; TCP acceptance is not application readiness.

The Alpha 5 rollout gate now concerns ordering, deadlines, network cleanup and recovery tests, **not proof that arbitrary application instances are safe to overlap**. Volume-safe recovery remains unchanged: never automatically restart older code after a replacement may have written its volume.

### 5. Entirely rootless; native networking test before ingress

All Gordon processes remain non-root, with four independent rootless Podman containers supervised through user systemd/Quadlet. Gordon installation must not require creating a system account, installing a system service or invoking privileged setup. The administrator owns host prerequisites (including any required lingering setup), firewall/sysctls and privileged-port forwarding. Gordon validates and reports missing prerequisites; it does not silently elevate.

Before implementing host ingress, run [A1A.0](plans/alpha-1a-foundation-proofs.md). Direct pasta was already tested. Separately verify the native bridge port-forwarder path (`rootless_port_forwarder = "pasta"` / Pesto), actual package/version availability, TCP/UDP source identity at edge, private-network isolation, lifecycle cleanup and capability restrictions. Upstream documentation is not proof on Ubuntu 26.04.

If that native path meets the requirements, **omit host ingress entirely**. Do not build two parallel implementations or retain it as an unused fallback. Record reviewed evidence and update the topology, ownership/port-publication contracts, readiness and plans before production implementation. If dynamic publication needs edge recreation and interrupts unrelated routes, that concession needs explicit maintainer acceptance; it is not pre-approved here.

If native proof fails, ADR-002's host ingress remains a conditional fallback, under the same rootless/user-service constraint. Its direct host-file/process isolation is still unproven. Do not weaken that boundary merely because edge is trusted for traffic: direct access to Podman/host secrets is a different authority. Report the failure and obtain an explicit decision if no viable rootless confinement exists; do not silently proceed unconfined or install a dedicated privileged setup.

Neither path automatically preserves kernel source IP at proxied backends. Test original identity at edge separately from trusted HTTP proxy headers and backend socket identity. Backend requirements not met by a proven mechanism remain unsupported.

### 6. CrowdSec after alpha

Native CrowdSec integration is deferred beyond Alpha 1–5. Administrators may operate upstream protection independently, but acceptance tests and security claims must work without it. Do not add a bouncer, CrowdSec API dependency or firewall mutation to alpha. Future integration needs a defined enforcement point and trusted client identity.

## Validation and consequences

- No proof of native pasta functionality or rootless ingress confinement is supplied by this ADR.
- Test direct app/edge denial of Podman, private state, secret database/key and management capabilities, including forged resource labels. Separately acknowledge traffic-derived credential compromise rather than writing an impossible confidentiality test.
- Test bbolt atomic revisions/reservations, journal interruption, incompatible formats, ciphertext tampering/swapping, nonce/key handling, missing/wrong key and secret non-disclosure through APIs/logs/backups.
- Test public registry opt-in, authentication, repository scope and private pulls with edge down. TLS modes/origin trust remain a bounded contract prerequisite, not an edge-impersonation project.
- Test stop timeout defaults/overrides/validation, early exit, force termination, old/new instance identity, repeated recovery, multi-entrypoint switch and existing streams. Do not infer application concurrency safety from passing transport tests.
- Maintain fresh-install and no-update scope. Plans cover the entire alpha, with proofs and unresolved contracts visibly distinguished from approved product decisions.

## Related

- [Design](design.md)
- [Original foundation ADR](adr-001-v3-foundation.md)
- [Conditional host-ingress ADR](adr-002-host-ingress.md)
- [Plan index](plans/README.md)
