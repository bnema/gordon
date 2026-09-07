# Alpha 2: first web app with durable recovery

Status: planned; entry requires complete Alpha 1 and W1 contracts before workload implementation.

ADR-003 selects bbolt, default 30s per-service shutdown and the trusted-edge model. Ingress references below apply only to the fallback; if N0 selects native networking, use its accepted publication/withdrawal readiness contract instead, without implementing relay IPC.

## Context and scope

Use the [shared baseline and checks](README.md). Deliver one explicitly named service from an external registry, a private app network, separate ingress network, HTTP entrypoint/route, configuration-only apply, immutable digest-pinned deploy, minimal stop and interruption/reboot recovery. The release and recovery model starts here, not in Alpha 4.

Do not add multi-service secrets/volumes, public Gordon registry, rollback commands, concurrent web replacement, TCP/UDP app routes, migrations or generic readiness configuration. Unsupported manifest features fail before persistence rather than being silently ignored.

Existing patterns: Cobra configured output/JSON helpers, strict role APIs from Alpha 1, `internal/domain/`, `internal/boundaries/{in,out}/`, storage adapters under `internal/adapters/out/filesystem/`, fixture `dev/v3/fixtures/app-web-test/`. The inherited config/container/proxy use cases are not v3 service implementations.

Proposed new work locations: `internal/domain/{app_spec,release,operation,reservation}.go`, `internal/usecase/{apps,deployment}/`, `internal/adapters/in/cli/apps.go`, control handlers/DTOs, persistence adapters, and edge/runtime packages established by Alpha 1. Concrete schemas and method signatures must come from W1; no external database or speculative repository framework.

## Tasks

- [ ] **A2.1 — Accept persistence, coordination and first-web contracts** — depends on: A1B.6.
  - Produce focused bbolt persistence, reservation/edge-snapshot and workload-recovery contracts. The storage engine is already selected: specify buckets, transaction boundaries, durable AppSpec revisions, normalized identity, immutable releases, desired/intent/observed distinctions, operation journals, format versions and corruption/disk-full behavior. No SQL/migration framework; reject incompatible formats without mutation.
  - Define per-app serialization of apply/deploy/stop and future mutation categories, installation-wide reservation/publication coordination, lock ordering, bounded admission, operation IDs, retry/idempotency and observe-before-resume semantics. No stale in-memory mutex alone can establish crash atomicity.
  - Define exact apply/deploy/status DTOs and errors: malformed manifest, unsupported feature, name/route conflict, unavailable dependency, missing image, stale operation and incomplete recovery. Map domain failures to stable HTTP statuses and CLI nonzero exits; return former/resulting revision for apply.
  - Specify image-selector canonicalization, control-side digest resolution, platform/index selection and runtime verification of pinned content. Define resolver authority without Podman or OCI push credentials in control. No labels, implicit exposed-port selection or registry searching.
  - Specify supported HTTP/TLS modes, certificate provisioning/renewal, canonical hosts and secure upstream-header trust for the selected network path. Modes/signer/origin trust are still open; do not silently restore v2 PKI. Keep public registry disabled pending explicit publication/R1, not because edge is forbidden to terminate its TLS.
  - Define exact snapshot generation/publication/ACK durability, last-valid snapshot bootstrap, global merge of concurrent app updates, backend validation after reboot and listener recovery integration with F2. Define which actor starts workloads and when; component lingering alone is insufficient.
  - Specify pre-Alpha-5 recreate routing/withdrawal and fixed backend availability checks, without configurable probes or early concurrent replacement. Define shutdown target/deadline persistence: stop_timeout defaults to 30s, finite positive service overrides, early exit or SIGKILL at deadline, no indefinite reset after retries. Keep reservation withdrawal evidence separate from process exit.
  - Done: W1 accepted with state/effect tables, crash test vectors and critical review resolved. Update downstream anchors before coding; bbolt is fixed, wire/schema/transaction details must be explicit.

- [ ] **A2.2 — Normalize and persist desired AppSpecs without effects** — depends on: W1 accepted.
  - Implement complete single-app TOML parsing, globally unique names, explicit named services, compact/expanded normalization and duplicate-form rejection. Validate one-service alpha scope and reference integrity.
  - Validate external registry-qualified image references and reject literal latest. Normalize per-service stop_timeout (30s default, finite positive duration); reject invalid/zero/negative/overflow values, record it in releases and do not allow an app-env override. Parse entrypoints/routes under W1; canonicalize DNS, reject duplicates/wildcards and reserve configured system domains/listeners.
  - Apply compares normalized state and atomically persists revisions/reservations under the W1 contract, with last-successful-write-wins and former/resulting revision/actor reporting. A new app starts stopped; repeated/changed apply preserves intent. No runtime pull, network, bind, container or edge call is permitted.
  - Reserve desired, active and in-flight identities. Removal from desired cannot release an active route. Test two apps racing for one host, failed persistence, simultaneous apply/deploy capture and internally conflicting candidates.
  - Test malformed TOML, missing/implicit service, unsupported env/secrets/volumes/multi-service/TCP/UDP fields at this stage, duplicate forms and unresolved references. No partial desired state on failure.
  - Done: C2–C6/C10; fake runtime/edge/ingress/resolver call counts prove zero apply effects, including negative paths.

- [ ] **A2.3 — Prepare immutable releases and constrained runtime mutations** — depends on: A2.2.
  - Resolve the captured selector to a digest before workload mutation; persist the immutable effective AppSpec, source revision, exact digest, resolved runtime definition and intended route projection. Registry unavailable or digest mismatch fails without activating a release.
  - Implement the minimal consumer-owned runtime operations through Alpha 1's Podman adapter. Reject mutable refs, forbidden mounts/network modes/capabilities/devices and resources reserved to Gordon. Never infer configuration or management identity from image labels, including forged component/app/release labels.
  - Create the app-private network and separate generated ingress network. Only routed services and edge join ingress; edge never joins the complete app-private network. Runtime's edge attachment operation is narrow/idempotent and cannot become component lifecycle control.
  - Capture operation ID/input before effects. Persist `prepared`/`mutating` and subsequent phases under W1; record observed resources so retries inspect instead of creating duplicates. Release history is immutable even if activation fails.
  - Test image resolution failure, platform/digest mismatch, pull/create/start/network failures, labels trying to override ports/routes/ownership and crash boundaries. A `HEALTHCHECK` or health label does not add a probe or alter deployment behavior.
  - Done: C2–C6/C10 and C8 creates the intended hardened digest-pinned workload/network set without exposing traffic prematurely.

- [ ] **A2.4 — Activate HTTP routes with coordinated evidence** — depends on: A2.3.
  - Implement edge routing from the sanitized release projection and ingress listener authorization from the captured control operation. No complete manifests, stored secrets or Podman capability reach edge/ingress.
  - Use W1 backend availability checks and recreate behavior. Mark active only after the intended runtime set, edge route generation and ingress listener/relay readiness agree. No label-driven HTTP probe or OCI healthcheck; no two-instance replacement before L1.
  - Publish complete coordinated snapshots across apps; reject invalid/older generations. Persist only last-valid edge state and app certificates. Edge restarts fail closed without a valid snapshot; backend observations must be valid before recovering public readiness.
  - Withdrawal on a shared listener is route-level at edge, including existing-stream rejection/draining according to W1; preserve other apps. Final-listener withdrawal also requires ingress closure/cleanup. Timeout/unknown ACK retains reservations.
  - Test partial runtime success, unavailable/lying/delayed edge ACK, ingress failure, invalid backend, concurrent publications, old snapshots, withdrawn host reuse and unrelated-route continuity. Cover client-header spoofing/CIDR checks over the real relay.
  - Done: C2–C6/C10 and C8 full HTTP path; status cannot claim active for a partially acknowledged operation.

- [ ] **A2.5 — Minimal CLI, stop and observation-driven recovery** — depends on: A2.2–4.
  - Implement `apps apply --file`, `apps list/show`, full `deploy <app>`, `stop <app>`, `status [app]`, `logs <app>` and read-only `routes list/show`. All list/show commands support JSON; local and SSH use the same control API. Keep logs bounded/cancellable and avoid logging full untrusted payloads.
  - Full deploy of a stopped app requests running state but changes durable intent only on activation. If interrupted, its operation can resume; generic reconciliation must not resurrect the previous release. On failure keep stopped intent and clean only known partial resources.
  - Stop persists stopped intent before withdrawal/container removal and retains revisions/releases/ownership. Signal each captured active container, wait at most its effective stop_timeout, force terminate if needed and confirm cleanup before releasing reservations. Persist/reconcile the deadline across interruption; apply/queued events cannot undo stopped intent. Test early exit, force kill and stale-target/retry protection.
  - Recovery observes runtime, edge and ingress before effects, uses active pinned definitions, and never resolves newer tags or activates pending AppSpecs. Restore edge ingress attachments after restart; do not add a generic Gordon component restart API.
  - Status exposes desired revision, effective active release, source revision, execution intent, operation phase and observed/degraded resources, not a single optimistic status bit.
  - Test stop/deploy races, reboot with pending desired configuration, failed deploy while stopped, unavailable control/runtime and logs cancellation. Runtime absence must not terminate existing containers; control absence must not interrupt already active routes.
  - Done: C2–C6/C10 and C8 real local/SSH CLI + reboot recovery for both running and stopped apps.

- [ ] **A2.6 — First-workload acceptance gate** — depends on: A2.1–5.
  - Add a minimal one-service manifest/fixture derived from the existing web fixture without requiring its future DB/cache services. Pin the external test image; fixture source and expected response must be attributable.
  - On a clean installation: apply → assert no runtime effects → deploy → serve HTTP → apply pending change → restart roles/reboot → verify old active release → full deploy → stop → reboot → verify no resurrection.
  - Inject failure before/after every journaled effect and ACK. Compare persisted and actual state; verify uncertain reservations remain held and concurrent app publication does not lose routes. Assert no duplicate managed workloads after recovery.
  - Run negative role/network access tests, ignored/forged image-label tests and the complete Alpha 1 installation gate. Update CLI/docs with supported scope and precise non-readiness/availability promises.
  - Done: C1–C10 as applicable, clean-host and interruption reports reviewed. Alpha 3 cannot begin with persistence, stop or recovery still described as future work.

## Rollout and recovery

Fresh Alpha 2 install only; no migration from Alpha 1 or v2. Workload recovery follows W1 and the captured journal, not reinstall or tag re-resolution. Retain failed operation evidence and report partial actual state. This stage has no persistent app volumes, but still must not delete unknown host resources or lose desired/history records.

## Related

- [Plan index and shared checks](README.md)
- [Previous: Alpha 1B](alpha-1b-installation.md)
- [Next: Alpha 3](alpha-3-multi-service.md)
- [Design: apply, releases and deployment](../design.md#apply-releases-and-deployment)
