# Alpha 4: public OCI registry, push events and rollback

Status: planned; entry requires Alpha 3 complete and R1 TLS/auth/event contracts. ADR-003 removes the edge-impersonation/confidentiality gate: edge is trusted for public registry traffic. Ingress-specific readiness applies only to the conditional fallback after N0.

## Context and scope

Use the [shared baseline and checks](README.md). Extend existing digest-pinned releases to Gordon-hosted images, authenticated public OCI operations, durable bounded push events and full/service rollback. Do not introduce releases for the first time here.

No public control API, component bearer-token plumbing, implicit deploy-on-push, label autoroute, image prune/GC, backup restoration, old secret versions or distribution update. Standard OCI client authentication is distinct from forbidden internal role tokens.

Existing anchors: Alpha 1 registry role/private-pull endpoint, Alpha 2/3 release and deployment ports, `internal/usecase/{registry,registrystate,images}/`, `internal/adapters/out/filesystem/`, `internal/adapters/in/cli/{push,rollback,images}.go` if retained, CLI `remote/`, `.github/actions/` and deployment docs. Inspect legacy OCI helpers individually; do not revive v2 route lookup, eventbus deployment or shared credentials.

Proposed work belongs in the established registry/control/edge compositions, consumer-owned image/event/release ports, bounded filesystem outbox adapter, and CLI commands. Exact authentication, certificate and event schemas are R1 outputs, not assumptions in this plan.

## Tasks

- [ ] **A4.1 — Accept registry trust, authentication and event contracts** — depends on: A3.6.
  - Consolidate W1 TLS modes for explicit public registry exposure: certificate ownership/provisioning/renewal, client trust and proxy-to-origin transport. V2 CA, supplied certificates and upstream TLS are candidates, not selected implementations. No mandatory SNI passthrough or proof against trusted-edge impersonation. Require correct endpoint validation, not insecure TLS fallback.
  - Specify public-domain opt-in (private default), reserved system-route lifecycle, OCI authentication/provisioning/revocation and repository-scoped authorization. No public control API or stored-token printing. Runtime stays pull-only on the private endpoint; control/edge/ingress receive no credential-store mounts. Public credentials transit trusted edge; their exposure on compromise is accepted.
  - Specify registry protocol support, digest/media-type/platform validation, bounded uploads, interrupted-upload storage and request/resource limits. Reuse existing OCI libraries where applicable; no new dependency without approval.
  - Write the bounded outbox contract: repository/tag/digest/time/event ID, durable acceptance/ack boundary, capacity/backpressure, dedup retention, ordering, stale-event policy, retry timing, poison events and restart recovery. Define what a successful push guarantees if event persistence or queue capacity fails.
  - Specify exact control eligibility/error rules for push target lookup, unique auto-deploy mapping, stopped intent, pending configuration, synthetic-release divergence and ambiguous explicit push targets. Events cannot supply route/secret/runtime instructions.
  - Define rollback request validation, historical selection, full versus synthetic composition, route reacquisition and operator acknowledgement for volume compatibility. No mechanism may imply that Gordon can prove arbitrary database compatibility.
  - Specify the rootless Podman CLI build/push contract before implementing --build; avoid inherited Docker fallback. Done: R1 contracts and critical review accepted, supported TLS/origin trust and negative authentication tests defined. No registry confidentiality-against-edge proof remains.

- [ ] **A4.2 — Explicit public OCI storage and TLS routing** — depends on: R1 accepted.
  - Implement bounded OCI handlers/storage and authentication in registry's isolated role. Registry owns blobs/manifests/tags and authentication storage; TLS/key ownership follows the accepted endpoint contract. Untrusted content/repository strings cannot escape storage.
  - Keep registry private by default. Explicit system-domain configuration enables edge TLS termination and HTTP forwarding to registry under R1. Reserve the domain; app route collisions and app stop/remove/purge cannot seize or delete the registry system component. Test disabled exposure produces no public route.
  - Keep the F2 private runtime pull endpoint independent of public edge. Test runtime pulling while edge is unavailable, push denial for pull-only credentials and no credential exposure to control.
  - Test digest mismatch, truncated/cancelled/oversized uploads, invalid auth, wrong repository permission, storage exhaustion and restart. Return actionable protocol errors; do not publish an incomplete manifest as valid content.
  - Test certificate provisioning/renewal failure, wrong endpoint identity and rotation under R1 with real client/upstream trust validation. Test spoofed forwarded identity and origin bypass. Never use insecure TLS flags to make acceptance pass; edge plaintext visibility is expected, not a failing confidentiality test.
  - Done: C2–C6/C10, C8 standard authenticated OCI push/pull and trust/storage/authority failures. Registry compromise still has the accepted bounded auto-deploy content risk, not immunity from it.

- [ ] **A4.3 — Durable outbox and control-owned event consumption** — depends on: A4.2.
  - Emit minimal durable push events under the R1 success/ack contract, deliver through the dedicated Unix capability, and retain/retry until acknowledged. No generic control or runtime operation is available to registry.
  - Control validates, deduplicates and maps events to configured services; it owns policy and release selection. Persist consumption/operation linkage so restart/lost ACK cannot trigger duplicate untracked deployments.
  - Implement exact per-service opt-in repository-and-tag matching; reject ambiguous auto-deploy selectors installation-wide during apply. Disabled auto-deploy never deploys. Tag events cannot activate a pending AppSpec or revive a stopped app.
  - Enforce desired/active source revision and normalized effective-config equality before service composition, including after synthetic rollback. Treat forged/malformed/stale events according to R1, never silently select a newer tag during replay.
  - Test duplicate/reordered events, full queue, unavailable control, registry restart, control crash before/after ACK, removed/changed selector, stopped intent and pending desired state. Bound queue/retry work and report capacity/deployment failures separately from stored-image state.
  - Done: C2–C6/C10 plus C8 durable outage/recovery with no unauthorized mutation or unbounded backlog.

- [ ] **A4.4 — Gordon image selection and push CLI** — depends on: A4.2–3.
  - Extend the existing resolver so references without a registry hostname target Gordon only; external refs still require complete hostnames. Reject literal `latest`, pin every runtime ref and ignore all image labels for configuration/display.
  - Implement `push <image> [--build] [--deploy]` using the approved rootless Podman build/push path and standard OCI authentication, not inherited Docker fallback. Source build inputs remain explicit; an image push alone does not deploy by default.
  - For `--deploy`, use the same eligibility rules as service-targeted deploy. Zero/multiple active targets require explicit `--app` and `--service`; target selection cannot bypass policy. A successful image push followed by refused deployment reports both outcomes clearly.
  - Implement `images list` and JSON inspection with verified image/digest data, not OCI label metadata. Do not add prune, retention deletion or an implicit tag rollback path.
  - Test explicit/ambiguous targets, auth failure, canceled build/upload, successful push with deploy refusal, stopped apps, external/Gordon reference canonicalization and pending desired/effective divergence.
  - Update CI push examples to call the actual v3 command/API; remove old route-based workflow guidance for v3. No credentials in logs or command output.
  - Done: C2–C6/C10, C8 real standard-client and CLI push/deploy, plus C1 for CLI/CI docs.

- [ ] **A4.5 — Immutable full and service rollback** — depends on: A4.1, A4.3–4; Alpha 3 composition/data safety.
  - Implement `rollback <app> [--to <release>] [--service <name>]`. A full rollback creates a new release from a historical immutable release; it never rewrites desired/history and records the selected historical source revision.
  - A service rollback creates a synthetic release from active effective configuration, replacing only the selected former service definition/digest. Retain active app-wide environment, entrypoints, routes, network declarations and all other services. Record base/donor release IDs and base source revision as provenance.
  - Validate compatibility of the donor definition with retained fields, required current secret names, volume identity and route reservations before runtime mutation. Reconcile networks from release declarations, not live Podman attachments. Historical releases do not reserve hosts indefinitely.
  - Reacquire reservations atomically, use the existing journal, and activate only with runtime/edge/ingress evidence. Secret injection uses current values. Stopped apps refuse rollback; unrelated services remain untouched by a service rollback.
  - After composition, report actual effective config/divergence. Revision equality alone must not allow service deploy/push/auto-deploy to undo synthetic config; only a full deploy activates the desired AppSpec again.
  - Test donor incompatibility, deleted/current missing secret, conflicting reclaimed host, missing digest, unavailable runtime/edge/ingress, partial recreate failure and volume writes. Explicit volume rollback requires operator-established compatibility; no automatic old-image restoration after possible writes.
  - Done: C2–C6/C10 and C8 full/service rollback, immutable provenance and post-rollback eligibility guards across restart.

- [ ] **A4.6 — Registry and rollback acceptance gate** — depends on: A4.1–5.
  - On a clean install exercise standard OCI client push/pull, CLI push-only/push-deploy, opt-in auto-deploy, offline control/outbox replay and runtime private pulls with public edge unavailable.
  - Combine pending manifest changes, stopped intent, synthetic rollback and duplicate/old events; assert push succeeds where appropriate but forbidden deployment never occurs. Crash every persistence/ACK boundary with repeatable outcomes.
  - Re-run endpoint/origin TLS and authentication tests on renewal/restart, private-store isolation, resource bounds, image-label non-authority and volume-safe failed rollback. Verify explicit public exposure and app-lifecycle exclusion. Logs/status distinguish image persistence, event delivery, operation and active release state without printing credentials.
  - Update registry/CLI/release docs and v3 CI examples; retain deferred GC/backups and distribution-update gates. Run all earlier clean-host/security/recovery checks on this generation.
  - Done: C1–C10 as applicable and reviewed endpoint trust/auth/outbox/rollback reports. Document accepted traffic credential theft/poisoned-image risk, including later explicit deploys, without weakening direct Podman/admin isolation.

## Rollout and recovery

Fresh distribution installation only. Registry outbox recovery obeys R1; it cannot blindly replay deployment requests. App rollback is a new journaled release, not distribution rollback or data restoration. If a former image cannot safely use current volumes, leave explicit degraded state for the operator rather than attempting automatic repair.

## Related

- [Plan index and shared checks](README.md)
- [Previous: Alpha 3](alpha-3-multi-service.md)
- [Next: Alpha 5](alpha-5-routes-lifecycle.md)
- [Design: image policy](../design.md#image-policy)
- [Design: rollback](../design.md#rollback)
