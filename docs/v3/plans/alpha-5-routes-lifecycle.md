# Alpha 5: multi-protocol routes and complete app lifecycle

Status: planned; entry requires Alpha 4 complete and L1 ordering/deadline/recovery contracts. ADR-003 already selects automatic HTTP-only/no-volume overlap without a concurrency declaration or safety classifier. Ingress-specific transport tasks are conditional on N0; native success removes dedicated relay IPC, not the applicable isolation/recovery objectives.

## Context and scope

Use the [shared baseline and checks](README.md). Deliver HTTP/TCP/UDP app routes, restart/remove/purge, stop/recovery for all protocols and the accepted automatic web-overlap algorithm after transport/recovery tests. Applications own concurrency safety; Gordon does not prove it.

No generic deployment strategy/readiness selector, live UDP session migration, transparent ingress restart, cluster replicas, resource GC, backups, component updates or rollback of data. App `restart` is not permission for runtime to supervise Gordon's components.

Existing anchors at execution: Alpha 1 ingress IPC/listener journal, Alpha 2 control reservation/edge snapshot/deployment state, Alpha 3 service/volume/secret contracts, Alpha 4 composition/rollback/event policy, `dev/v3/fixtures/app-game-test/`, `dev/v3/cmd/l4probe/`, and existing CLI output/JSON patterns. Extend these owning boundaries rather than creating a route-owned container engine.

Proposed work locations: established app/deployment/edge/ingress packages, domain tombstone/lifecycle records, runtime volume ownership adapter and CLI `restart.go`, `stop.go`, `remove.go`, `purge.go`, `routes.go`. Exact new DTOs, confirmation syntax and state transitions must be accepted at A5.1 before coding.

## Tasks

- [ ] **A5.1 — Accept rollout and lifecycle extensions** — depends on: A4.6.
  - Refine ADR-003's fixed structural policy: HTTP/HTTPS-only routes and no persistent volumes get overlap; others recreate. No opt-in, image label, application-safety classifier or strategy selector. State the accepted risk of duplicated jobs/migrations in a web process; the app owns graceful shutdown and schema compatibility.
  - Define multi-entrypoint TCP checks, acknowledged route switch, old-container stop signal, its effective stop_timeout (30s default), early exit/SIGKILL, and crash-safe target/deadline handling. Specify existing keep-alive/HTTP2/WebSocket cleanup within bounded shutdown rather than claiming a signal proves drain or adding a second unexplained full grace period. No application readiness or OCI HEALTHCHECK interpretation.
  - Specify route DTO/schema extensions and protocol validation, shared SNI passthrough, public/local bind mapping under F2, CIDR/source metadata and exact withdrawal evidence. F2 transport mechanisms remain authoritative.
  - Extend W1/S1 persistence contracts for restart/remove/purge, tombstone contents/name reservation, cross-role deletion order, operator confirmation containing the app name, and interruption recovery. Registry images retain an independent lifecycle.
  - Define UDP stop/recreate/rollback integration: per-listener admission/forwarding closure and epoch invalidation before backend effects, both-role acknowledgement, readiness and fresh reopen. Never invalidate unrelated listeners because a global snapshot generation changes.
  - Specify errors/status for incompatible rollback, uncertain withdrawal, absent/current secrets, stopped intent, incomplete cleanup and destructive confirmation failure. Re-review authorization and data-loss risks with a critical senior/red-team pass.
  - Done: L1 and lifecycle extensions accepted with precise state/effect/error tables. Overlap stays disabled until ordering/shutdown/recovery tests pass, not pending an application-concurrency proof.

- [ ] **A5.2 — TCP/UDP route validation and public activation** — depends on: A5.1.
  - Extend compact/expanded entrypoints and app routes to dedicated TCP/UDP listeners and accepted SNI passthrough. HTTP routes target HTTP; TCP/SNI targets TCP; UDP targets UDP. Entrypoints alone never expose traffic.
  - Validate protocol/port/address/CIDR syntax and all desired/active/in-flight reservation conflicts, including wildcard/specific binds, IPv4/IPv6 dual-stack overlap, shared installation listeners and reserved registry SNI. No app-port catalogue in installation config.
  - Reuse control's atomic reservation and snapshot publication paths. Apply remains effect-free; deploy/rollback authorize only captured release operations. Changing a route does not independently create/delete/stop a service.
  - Edge routes opaque TCP/UDP using trusted ingress client/local metadata and enforces CIDRs; ingress remains transport-only. Test untrusted headers/payloads cannot alter policy identity. Backends requiring original kernel client source stay blocked unless F2 separately proved it.
  - Test allowed/denied RCON peers, binary TCP traffic/half-close, shared HTTP/SNI routing, dedicated listener failure and route removal with unrelated shared traffic. Unknown/stale route snapshots fail closed.
  - Done: C2–C6/C10 and C8 full client→ingress→edge→backend TCP/UDP paths. No direct Podman publication may substitute for routed tests.

- [ ] **A5.3 — UDP recreate and withdrawal across app operations** — depends on: A5.2; F2 production transport.
  - For each affected listener, journal and stop admission/forwarding, invalidate old epoch/associations in ingress and edge, then replace/stop the backend. Keep admission closed until runtime/route/relay readiness agrees; reopen only with a fresh non-reused epoch.
  - Apply the same sequence to full/service deploy, rollback, restart and stop. Late backend/IPC responses from the former epoch are discarded after process/host restart too. Delayed client datagrams may establish new associations; no game parsing.
  - Dedicated/final shared-listener withdrawal requires ingress closure plus bounded accepted-stream/association cleanup. Route-only withdrawal on a shared HTTP/SNI listener requires edge's per-route evidence and must not kill other apps' traffic. Uncertain cleanup retains the applicable reservation.
  - Test multiple clients, unsolicited/multiple backend replies, binary fidelity, timeout/overload bounds, changed backends, edge/ingress failure between invalidations and fresh reopen, reboot and stale reply injection. Another app's UDP sessions survive unrelated deployment.
  - On restart recover only authorized applied listeners/routes with empty UDP sessions. Stop intent remains stopped; partial operations resume from observations and cannot reopen a withdrawn listener to simplify cleanup.
  - Done: C2–C6/C10 and C8 game fixture disruption/reconnection and non-cooperative withdrawal. Explicitly report interrupted TCP and lost UDP on ingress failure.

- [ ] **A5.4 — Automatic web overlap and bounded shutdown** — depends on: A5.1 ordering/deadline contract; A5.2–3 route semantics.
  - Apply structural eligibility only: HTTP/HTTPS-only routed service, no persistent volumes. Unrouted workers, pure TCP/UDP, RCON, mixed-protocol and volume-owning services recreate. Hidden background work in a web process is the application's responsibility, not a new classifier.
  - Implement start candidate → Podman running → required HTTP entrypoint TCP acceptance → acknowledged atomic route switch → signal old container → wait up to its stop_timeout → force termination if still running. Only the temporary replacement may add an instance. Do not add replica, strategy or concurrency configuration.
  - Journal each effect and ACK. Candidate failure before switch must not falsely activate it; uncertain switch/drain must be observed before recovery. Preserve unrelated services and follow the accepted volume-safety restriction for any broader app failure.
  - Test structurally eligible HTTP services, including one with background work to prove Gordon adds no classifier; document duplicate-work risk rather than asserting it cannot happen. Test one closed entrypoint, early graceful exit, overridden/default timeout, forced kill, long-lived WebSocket, edge crash around switch and control restart before signal/kill. Never kill the replacement via a stale ID or reset the deadline indefinitely.
  - Verify image labels and healthchecks cannot grant eligibility or change checks. Document transport-level limits: a listening TCP port does not prove application readiness and connections beyond the drain deadline may be interrupted.
  - Done: C2–C6/C10 and C8 structural eligibility/switch/signal/deadline/recovery evidence with critical review. Until this task passes, replacement stays recreate. Passing transport tests does not certify application safety or zero downtime.

- [ ] **A5.5 — Restart, remove and purge with durable ownership** — depends on: A5.2–4 and accepted lifecycle contracts.
  - Implement `restart <app> [--service]` using active effective runtime definitions/pinned digests and current secret values. Never resolve tags or use pending desired config. Stopped apps refuse restart, rollback, service deploy and push-triggered deployment/auto-deploy; a standalone image push can still succeed without activation. Only full deploy requests running intent again.
  - Extend stop to all protocols with persisted intent first, reservation/withdrawal evidence and bounded cleanup. Preserve volumes, secrets, manifests and releases. Interrupted stop resumes; it cannot resurrect workloads at reboot.
  - Implement remove: stop/withdraw safely and persist the minimal tombstone before deleting app metadata. Retain stable volume identities, verifiable app/service ownership evidence and per-volume cleanup state until deletion is observed; specify their exact encoding in the lifecycle contract. Then delete app manifests/releases/secrets/owned runtime networks/containers, preserving volumes and the reserved app name. Do not remove another app's shared network membership or registry images.
  - Implement purge: use the durable tombstone ownership record, not labels or the app name alone, to perform remove and delete only verified app-owned volumes plus tombstone after explicit confirmation containing the app name. Purge must work after prior remove. Keep enough durable ownership until deletion is observed; wrong/missing confirmation causes no destructive effects.
  - Reject implicit app-name reuse/volume adoption while a tombstone exists. Test foreign/forged ownership labels, missing/already-deleted resources, disk errors, denied deletion, duplicate requests, active-operation races and crashes between every cleanup effect.
  - Complete CLI list/show JSON coverage, logs/service selection, route inspection and desired/effective/intent/observed lifecycle status. Delete leftover v2 aliases and update help/completion/docs; no ad hoc route mutation or token-printing command.
  - Done: C2–C6/C10 and C8 real restart/stop/remove/purge with persistent test counters and tombstone recovery. Failed cleanup reports exactly what remains, never silently releases uncertain ownership.

- [ ] **A5.6 — Full alpha acceptance and documentation gate** — depends on: A5.1–5.
  - On a clean generation exercise web+DB/cache and TCP/UDP/RCON fixtures through actual Gordon routes, plus a second app for isolation/shared-listener/epoch tests. Confirm unsupported backend-original-source requirements are clearly stated.
  - Combine apply/deploy/secret mutation/push/rollback/stop/restart/remove/purge races under per-app serialization and global reservations. Inject crashes at each journal/ACK boundary and reboot with running, stopped, pending, synthetic and tombstoned state.
  - Re-run selected-topology confinement/identity, TLS endpoint/origin trust, direct app/edge denial of management/private stores, private pull, source-policy, resource-limit, volume-safety and attachment checks. Accept edge visibility into routed credentials; do not restore its removed registry-confidentiality test. Measure network/rollout performance without universal game or zero-downtime claims.
  - Audit CLI against the complete surface and removed/deferred matrix. Document rootless installation, encrypted-secret/key provisioning, deploy/recovery, stop_timeout, current-secret rollback, destructive confirmations and no distribution update path. Native CrowdSec stays post-alpha; tests do not assume upstream protection.
  - Ensure installer/image/unit-only CI changes run relevant checks and nested fixture modules are covered. Retain signed buildable PRs and sanitized proof records; no feature completion based only on mocks.
  - Done: C1–C10 as applicable, every predecessor gate satisfied, remaining limitations documented and no unresolved authority/data-loss/recovery blocker. Only humans decide public release readiness.

## Rollout and recovery

Use fresh reference installations; app release rollback does not authorize replacing Gordon's distribution. TCP/UDP services may be interrupted; UDP sessions are never restored. Restart/cleanup follows persisted intent and observations, not unconditional startup. Preserve reservations/tombstones until required release/deletion is proven. Operator data compatibility, backups and external firewall policy remain outside Gordon's automatic authority.

## Related

- [Plan index and shared checks](README.md)
- [Previous: Alpha 4](alpha-4-registry-rollback.md)
- [Design: deployment strategy](../design.md#deployment-strategy)
- [Design: lifecycle commands](../design.md#lifecycle-commands)
- [Host-ingress ADR](../adr-002-host-ingress.md)
