# Architecture red-team log

## 2026-09-04 — Gordon component lifecycle

### Builder decision

One Gordon installation has one distribution identity binding source/version, host-binary SHA-256, component-image OCI digest, and persistent-format versions. A distribution provides one host binary and one digest-pinned component image containing the same Gordon binary. The image runs as four isolated roles: `control`, `runtime`, `edge`, and `registry`.

The installed host binary owns desired installation state, generated Quadlets, installation-level lifecycle commands, and identity/readiness verification. Quadlet translates container declarations into units, systemd supervises the units, and rootless Podman runs the containers. Gordon is not a fifth resident supervisor.

Alpha 1 supports fresh install and same-generation recovery only. Mixed generations may exist during a future update only as an unhealthy transitional state. Component replacement, rollback or roll-forward, persistent-format compatibility, interruption recovery, and generated-Quadlet drift policy require a focused lifecycle ADR before any component update support.

### Evidence

- Updated `docs/v3/design.md` deployment topology, lifecycle ownership, alpha installer flow, Alpha 1 scope, and remaining decisions.
- Updated `docs/v3/adr-001-v3-foundation.md` decision drivers, lifecycle decision, consequences, risks, validation requirements, and follow-up decisions.
- `git diff --check` passes.
- Markdown fences are balanced.

### Critic pass

Senior review `568a43bf-aa10-49e3-8d0b-30caf08fbd5a` found one blocking and four high-impact objections:

- update states and recovery were underspecified;
- version text did not cryptographically identify both artifacts;
- shell bootstrap and Gordon lifecycle ownership overlapped;
- rollback ignored persistent-format compatibility;
- workload reconciliation did not normatively exclude Gordon-owned resources.

### Resolutions

- Limited Alpha 1 to fresh install and same-generation recovery; component update remains blocked on a lifecycle ADR.
- Added explicit transitional mixed-generation semantics and readiness states.
- Bound distribution identity to source/version, binary hash, image digest, and persistent-format versions; tagged manifests are signed and Quadlets pin the digest.
- Reduced the shell to obtaining the binary and invoking Gordon's locked, journaled, idempotent installer.
- Added preconditions for schema compatibility, backup, rollback or roll-forward, and interruption recovery.
- Reserved and labelled Gordon-owned resources, excluded them from workload reconciliation/GC, and narrowed the edge-network exception.
- Added user-systemd lingering and generated-Quadlet drift requirements.

### Accepted risks

- Branch, commit, and local source installations are unauthenticated alpha inputs.
- Initial `curl | sh` delivery trusts HTTPS unless the installer is obtained and verified through a separately trusted channel.
- Alpha 1 offers no component update or rollback.
- Runtime retains full-engine authority and one narrow edge-network attachment exception, both requiring the documented Alpha 1 proof.

### Final verdict

After two correction rounds, senior reviewer `568a43bf-aa10-49e3-8d0b-30caf08fbd5a` returned **VALIDATED** with no remaining blocking or high-impact objection.

## 2026-09-04 — Workload design clarification

### Builder decision

Clarify route reservations across desired/active/in-flight state, effective configuration identity after partial rollback, durable execution intent, volume-safe restoration, and the minimum workload invariants required in Alpha 2. Require full-path ingress proofs, registry certificate/client-trust analysis, and a concurrency/draining ADR before the corresponding features are enabled.

Public environment remains app-wide only. All service-specific values remain write-only secrets, including non-confidential values. Their historical values are not reproduced by manifests or release rollback. No new per-service public environment, rollout eligibility field, registry trust mechanism, or component update support is introduced.

### Evidence

- Updated design, foundation ADR, and agent guidance only; sandbox and runtime code are untouched.
- `git diff --check` passes.
- Markdown fences and relative file links pass validation; all 15 design TOML examples parse with Python `tomllib`.
- No Go tests or VM execution are claimed for this documentation-only change.
- CodeRabbit CLI is unavailable in this environment.

### Critic pass

Senior reviewer `63230960-eaa2-41af-8fbe-c8ceb49d680a` reviewed the changes against `origin/v3-alpha` and raised four high-impact and one medium-impact clarification, plus two wording improvements.

### Resolutions

- Unified source-address acceptance around full-path observation, original-client CIDR enforcement, and an explicit gate for workloads requiring preservation at the backend. Observation alone does not waive a workload's preservation requirement.
- Defined service rollback provenance as the base active AppSpec revision plus base/donor release identities and the composed effective AppSpec; full rollback retains the selected historical revision.
- Replaced mutable observed network attachments with declarations from the active release's effective AppSpec.
- Kept stopped intent until successful full-deploy activation; interrupted operations use their journal and failed attempts clean up rather than resurrecting prior releases.
- Distinguished certificate-lifecycle decisions from their implementation proof and clarified that web concurrency eligibility remains unselected.

### Remaining gates

The public-env model is unchanged. Detailed persistence/recovery, registry trust, ingress implementation, and rollout eligibility remain subject to their stated ADRs and runtime proofs.

### Final verdict

The same senior reviewer verified the corrections in commit `139ebd4b` and returned no remaining must-fix semantic contradictions within that scope. This closes the documentation critic pass, not the future implementation proofs. The final commit additionally records this review outcome.
