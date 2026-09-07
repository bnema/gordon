# A1A.1 — Proof harness and confinement decision

Status: in progress (runbook prepared; no guest execution yet)
Date: 2026-09-07
Task: [alpha-1a-foundation-proofs.md](../plans/alpha-1a-foundation-proofs.md) A1A.1
Gate: proven confinement unblocks ingress transport work (A1A.2–4); failure blocks public use.

This directory holds sanitized reference-host evidence only.
No credentials, private keys, or bulk OCI payloads.

## Entry state (from A1A.0)

Packaged-stack native publication fails kernel source identity (TCP and UDP
NAT rewrite to `10.89.x.x`; no pasta port forwarder or Pesto on the reference
guest — see [a1a0-native-pasta.md](a1a0-native-pasta.md)). The ingress fallback
is therefore active under ADR-002/ADR-003. The OS confinement mechanism is
undecided; previously tested user-service profiles failed either isolation or
startup and are not reused as a working fallback.

## Candidate mechanisms (same-account rootless/user-service only)

Test candidates individually, never combined into an unreviewed bundle:

- `systemd --user` hardening on the ingress unit: `NoNewPrivileges`,
  `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
  `RestrictNamespaces`, `RestrictAddressFamilies` (keeping only
  `AF_UNIX`/`AF_INET`/`AF_INET6`/netlink as the relay requires),
  `SystemCallFilter`, `LockPersonality`, `MemoryDenyWriteExecute` where the
  Go runtime tolerates it.
- Unprivileged Landlock (guest kernel 7.0) for filesystem isolation where
  unit directives prove insufficient. Standard library plus a minimal
  Landlock binding only; no new daemon or privileged helper.
- Explicitly out of scope: dedicated system account, system service,
  privileged installer steps, setuid/capability escalation, AppArmor/SELinux
  weakening, or host-network access for edge to bypass a failed bind.

## Canary matrix

Positive controls first: the trusted `gordon` account must demonstrate each
canary exists and is readable before any denial claim counts.

| # | Forbidden from ingress context | Allowed (must keep working) |
| --- | --- | --- |
| F1 | application secret files | B1: bind test listeners on `198.18.77.2` (TCP+UDP) |
| F2 | Podman socket and storage (`/run/user/$UID/podman`, graphroot) | B2: private IPC directory read/write (ingress-owned Unix sockets) |
| F3 | control-private files | B3: outbound loopback to test backend fixtures only |
| F4 | arbitrary host writes outside owned dirs | — |
| F5 | same-account `/proc` process access and ptrace | — |
| F6 | filesystem escape via symlink/path traversal | — |
| F7 | unauthorized Unix sockets (ingress admin, runtime control) | — |

## Harness plan (`dev/v3/cmd/foundationproof`, explicitly test-only)

Reuse sandbox VM lifecycle/SSH; introduce no new provisioning framework and do
not turn the sandbox into an installer or supervisor:

- subcommands: `versions`, `setup-canaries`, `positive-controls`,
  `run-scenario`, `report` (machine-readable JSON lines plus a human table);
- one scenario per candidate profile, bounded runtime, explicit pass/fail;
- scenarios run a test-only probe binary as the ingress context under the
  candidate unit profile — never a production ingress implementation.

## Procedure

```sh
./dev/v3/sandbox sync
go run ./dev/v3/cmd/foundationproof versions
go run ./dev/v3/cmd/foundationproof setup-canaries
go run ./dev/v3/cmd/foundationproof positive-controls
# per candidate profile:
go run ./dev/v3/cmd/foundationproof run-scenario <profile>
go run ./dev/v3/cmd/foundationproof report
```

Then repeat the passing profile after service restart and host reboot with LSM,
seccomp, user namespaces and no-new-privileges intact. Record exact versions,
unit profile, UID mapping, allowed resources and every allow/deny result below.

## Results

| Check | Result | Evidence |
| --- | --- | --- |
| versions/account/UID mapping recorded | NOT RUN | — |
| canaries + positive controls | NOT RUN | — |
| allowed binds/IPC usable per profile | NOT RUN | — |
| forbidden reads/writes/process/socket denial | NOT RUN | — |
| traversal/unauthorized-socket denial | NOT RUN | — |
| restart + reboot confinement retest | NOT RUN | — |

## Decision

- [ ] A candidate meets isolation with usable binds/IPC → accept it in a
  focused confinement ADR with exact unit profile and critical review; A1A.2
  may proceed.
- [ ] No candidate meets the boundary → stop and report the blocker; public
  use stays blocked. Do not silently weaken the boundary.
