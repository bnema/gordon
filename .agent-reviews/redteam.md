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

## 2026-09-04 — Development VM, reduced scope

- Decision: a development-only libvirt/cloud-init/SSH wrapper; no host sudo, DNS or CA mutations, no application orchestrator. Fixtures remain manual README examples.
- Critic: Go reviewer `4e1d4b73-5d63-4a06-addf-ffe328d26bfb` reviewed the discarded prototype and reported risks around host privilege, SSH trust, ownership, synchronization and cleanup.
- Resolution: the prototype's `dev/v3/internal` implementation was removed after the maintainer requested KISS. The replacement pins SSH keys before boot, checks libvirt UUID ownership, uses a kernel lock and extracts source as the guest's unprivileged user. Host namespace and trust-store operations were removed.
- Evidence: automated checks and real-VM validation of the replacement are recorded in `dev/v3/VALIDATION.md`.
- Limit: the reviewer explicitly did not re-audit the replacement. Its report is not approval of the final wrapper or remaining fixture code. No further review was launched.
- Accepted scope: one fixed trusted development VM, manual fixture lifecycle, no production ingress or installer guarantee. Future Gordon implementation requires separate plans.
