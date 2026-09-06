# Alpha 3: multi-service security and persistent data

Status: planned; entry requires Alpha 2 complete and S1 record/key/recovery contracts. ADR-003 already selects encrypted runtime bbolt with a separate read-only key mount; storage-engine selection is not open. Ingress-specific effects below apply only if the fallback survives N0.

## Context and scope

Use the [shared baseline and checks](README.md). Extend the existing AppSpec/release/journal flow, not a second deployment engine. Deliver multiple named services, public app-wide environment, write-only service secrets, service-owned named volumes, named private networks, service-targeted deploy and safe multi-service failure.

No shared secret references, service-local public environment, host binds/shared service volumes, dependency attachment model, backups, data migration runner, public OCI push or concurrent replacement.

Existing anchors to inspect at execution: Alpha 2 apps/deployment/domain/persistence packages, Alpha 1 Podman/role ports, `internal/adapters/in/cli/{secrets,networks,volumes,deploy}.go` if still present, and `dev/v3/fixtures/app-web-test/`. Legacy secret and volume services are only helper/test candidates. Proposed new v3 files belong in those established owning packages; secret storage stays in runtime adapters and secret metadata in control domain/persistence.

## Tasks

- [ ] **A3.1 — Accept service/network/secret/volume contracts** — depends on: A2.6.
  - Specify exact named shared-network declaration/validation syntax and membership lifecycle without provider/consumer abstractions. Preserve app-private isolation: all services share their app network, unrelated apps communicate only through explicit named-network membership.
  - Specify write-only secret request/response types, size/name validation and runtime-private bbolt buckets/atomic operations. Encrypt values before persistence with standard AEAD; define envelope/version, per-key nonce uniqueness, record identity binding, authentication failure and compatible-format rules. No plaintext pages, v2 pass/sops/env-file backend or SQL/migration framework.
  - Define cross-role secret mutation crash behavior, idempotency and metadata reconciliation without value readback. Control and runtime bbolt transactions are separate; runtime is the value authority. Serialize app operations without retaining values in control.
  - Specify unprivileged initial key/store provisioning and interruption recovery. Key directory 0700, key 0600, mapped owner, separate runtime-only read-only mount. Never regenerate/overwrite on missing or wrong key for an existing store. Test incomplete initialization, swapped ciphertext/identity, tampering, nonce misuse protection and matching key/database backup recovery. No key in logs/environment/image; no automatic rekey tooling.
  - Specify volume canonical identity, safe target/name validation, Podman volume options and ownership verification. Resolve behavior for image-declared volumes so Podman cannot create unmanaged anonymous storage implicitly. No host binds, shared service volumes or adoption by matching an image label/name alone.
  - Extend W1 runtime definitions/effective-config identity and validation for multi-service composition. Define safe ordered recreate failure handling, including evidence that replacement may have written a volume and the resulting prohibition on automatic old-image restoration.
  - Specify external error/status behavior for missing secrets, secret/public-env collision, forbidden referenced-secret removal, network/volume conflict, stopped app and desired/effective divergence. Record future tombstone ownership needs without implementing purge here.
  - Done: S1 ADR/contracts and critical review accepted; tests cover both normal operation and interrupted cross-role effects. No unselected backend/syntax is silently chosen downstream.

- [ ] **A3.2 — Extend manifest normalization and preflight** — depends on: S1 accepted.
  - Add multiple explicit services, app `[env]`, service secret names, service-owned volumes and accepted optional named-network declarations to the existing normalized model. Preserve compact/expanded equivalence and full-manifest apply semantics.
  - Reject service-specific public environment, public-env/secret name collisions, cross-service secret references, host path binds, shared volumes, duplicate names, invalid targets and reserved Gordon resources. Reject `env_file` and all image-label defaults/overrides.
  - Normalize public values once and inject them into every service at deployment. Required secret names define membership but do not contain values. Apply does not access stored secret values or create runtime resources.
  - Validate all required secrets before any deployment runtime mutation; missing values fail the whole preflight. Keep apply/secret mutation/deploy capture serialized so references and inputs cannot drift mid-operation.
  - Test collision cases in compact/expanded forms, invalid service/route references, secret absence, public values inherited by all services and no implicit service-local fallback.
  - Done: C2–C6/C10; apply effect-count regressions still pass with multi-service manifests.

- [ ] **A3.3 — Runtime-owned write-only secrets** — depends on: A3.2.
  - Implement `secrets set/list/remove` with explicit app/service targeting and secure non-echo input. Set/update may carry values transiently through control, but control stores metadata only; responses, logs and diagnostic errors never echo values.
  - Runtime encrypts values in its own bbolt store and injects plaintext same-name environment variables only into the owning service. Expose set/replace/delete/existence/injection through narrow ports, no get/export/read endpoint. Never put values in control/release definitions. Protect the key mount separately and fail closed on wrong/missing key or invalid ciphertext.
  - Removing a secret referenced by desired or active effective AppSpec fails. Changing a value has no effect on a running container until its next deploy/restart. Future rollback must use current values, not historical copies.
  - Test atomic replacement failure, lost response/retry, runtime/control crash between value persistence and metadata acknowledgement, unauthorized service access, listing/log/error leakage and snapshot/mount isolation. Confirm services receive only their own values.
  - Verify storage/file/process/key-mount exposure on C8. Encryption protects the database alone, not injected environments, Podman state, process memory, runtime/host compromise or backups containing the key. Traffic/supply-chain compromise can expose a service's own values; do not claim end-to-end secrecy or anti-rollback protection. Document separate secure key backup and loss consequences.
  - Done: C2–C6/C10 and reference-host denial/injection tests. No stored-value readback is introduced to make recovery easy.

- [ ] **A3.4 — Service volumes and explicit network membership** — depends on: A3.2; A3.3 for the DB fixture.
  - Create/verify stable named Podman volumes keyed by app/service/volume and the accepted ownership record. Reuse them across releases; refuse unknown/foreign resources. Prevent image-declared anonymous-volume or forged-label paths from bypassing ownership.
  - Reconcile automatic app-private and per-app ingress networks, plus explicit named private memberships. All app services join the private network with service-name aliases; only routed services join ingress alongside edge.
  - Permit selected services of separate apps to join an accepted named network; no cluster-wide or implicit shared network. Edge must not join database/private networks merely because a routed frontend uses them.
  - Keep desired network declarations in effective release configuration, never derive them from observed Podman attachments. Restore authorized attachments after runtime/edge restart; shared network teardown cannot disconnect another app.
  - Test volume content across deploy/reboot, path/ownership collisions, forbidden sharing, network creation/attach failure, unrelated-resource exclusion and negative edge-to-database/cross-app connectivity.
  - Done: C2–C6/C10 plus C8 DB/cache/web fixture isolation and persistent counters, without direct host publication of DB/cache.

- [ ] **A3.5 — Multi-service operations and safe failure reporting** — depends on: A3.3–4.
  - Extend full deploy to capture all service definitions/digests, validate before effects, and recreate in deterministic service-name order. Reuse the W1 journal and coordinated route activation; do not publish routes for a falsely complete service set.
  - Implement `deploy <app> --service <name>` only when running intent, desired/active source revisions and normalized desired/active effective configuration match. Create a composed release changing only the selected digest. No pending AppSpec activation and no unrelated service restart.
  - On later-service failure, retain the former route generation and perform bounded best-effort restoration only where safe. If replacement may have written a persistent volume, do not automatically run its old image. Report exact mixed/degraded resources and required operator recovery.
  - Extend stop, logs `--service`, status and read-only `networks list`/`volumes list` with ownership and per-service results. Stop retains all volumes/secrets/revisions, persists intent first, and resumes cleanup on interruption.
  - Test a worker/DB/web app, failure of the second/third service, volume writes followed by failure, pending desired change, stopped intent, unknown service and concurrent secret mutations. Verify bounded restoration and untouched unrelated services/apps.
  - Done: C2–C6/C10 and C8 full/service deploy plus data-safe failure recovery. A successful API call must not mask mixed actual state.

- [ ] **A3.6 — Multi-service security acceptance gate** — depends on: A3.1–5.
  - Update the existing web/PostgreSQL/Valkey fixture to the implemented manifest and secret workflow; keep fixture values synthetic and never production defaults. Add a second app to prove named-network opt-in and default isolation.
  - On a clean install test apply/no effects, missing-secret preflight, key/store provision/deploy, secret value replacement without immediate restart, service deploy, stop/reboot and edge attachment recovery. Check missing/wrong key, ciphertext tampering, interrupted initial provisioning and failed metadata/value transactions without resetting the store.
  - Kill control/runtime around secret mutations and journaled multi-service effects. Verify current values, persisted intent, immutable effective releases and volume data survive without leaked values or silent old-image restart.
  - Audit no host binds/anonymous unmanaged volumes, no image-label authority, no edge-to-unrouted-service access, no forbidden Podman mount and no implicit secret sharing.
  - Update CLI/docs with the service-owned non-confidential-value workflow and explicit data-compatibility warning. Run all earlier installation/workload gates on the new clean generation.
  - Done: C1–C10 as applicable and reviewed clean-host/negative/data-safety reports. Public registry remains disabled.

## Rollout and recovery

Fresh installation only for the new distribution. App operations retain stable volume identities; no automatic data rollback, backup restoration or schema migration is promised. When replacement writes are possible, stop automatic old-code restoration and surface the journal/resources for operator-led recovery. A timeout is not evidence that a volume is safe for old code.

## Related

- [Plan index and shared checks](README.md)
- [Previous: Alpha 2](alpha-2-web-app.md)
- [Next: Alpha 4](alpha-4-registry-rollback.md)
- [Design: app model](../design.md#app-model)
