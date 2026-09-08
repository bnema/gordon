# A1A.1 — Proof harness and confinement decision

Status: complete — no confinement candidate meets the boundary (see H1, H2); public use stays blocked
Date: 2026-09-07
Task: [alpha-1a-foundation-proofs.md](../../../docs/v3/plans/alpha-1a-foundation-proofs.md) A1A.1
Gate: proven confinement unblocks ingress transport work (A1A.2–4); failure blocks public use.

This directory holds sanitized reference-host evidence only.
No credentials, private keys, or bulk OCI payloads.

## Current interpretation

[ADR-004](../../../docs/v3/adr-004-merged-edge.md) removes the host ingress process. The failed candidates and blocked fallback below are historical evidence, not current implementation gates. Container/capability denial and runtime publication recovery require separate merged-edge proofs; this report does not establish them.

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

## Execution record (2026-09-07)

Guest: Ubuntu 26.04 LTS, kernel `7.0.0-30-generic`, podman `5.7.0`,
systemd `259`, uid/gid `1000`, LSM
`lockdown,capability,landlock,yama,apparmor,ima,evm`.
`apparmor_restrict_unprivileged_userns = 1`.

Harness (`dev/v3/cmd/foundationproof`, committed) ran in the guest:

- `versions` reports the above as JSON.
- `setup-canaries /tmp/foundationproof-a1a1` + `positive-controls`: pass.
- `run-scenario --bind 198.18.77.2 --expect open`: **11/11 pass** — binds
  (TCP+UDP), IPC read/write, and all canary reads allowed, as expected
  without confinement.
- `probe-read` subcommand added for real-path checks: open context reads
  `/home/gordon/.bashrc` (allowed, 3771 bytes); the Podman socket path does
  not exist while no engine service runs (informational).

### Profile H1: unit filesystem sandboxing silently absent (FAIL)

`systemd-run --user` with `NoNewPrivileges=yes ProtectSystem=strict
ProtectHome=yes` (transient and persistent unit): the unit starts, journal
shows no error, `NoNewPrivs=1` holds — but `/home` has no overmount,
`ls /home/gordon` works, and `touch /etc/should-fail` succeeds under
`ProtectSystem=strict`. Settings are accepted (`systemctl show` confirms)
but mount namespacing never happens.

Root cause (kernel audit, sanitized): `systemd-executor` creates a userns
and transitions to the `unprivileged_userns` AppArmor profile, where
`capable sys_admin` is **DENIED** — so mount setup cannot proceed, yet the
unit runs unconfined without any error. `PrivateUsers=yes` is skipped the
same way (`uid_map` stays identity `1000 1000 1`). Pure unit filesystem
confinement therefore **cannot meet F1–F7 on the reference guest**.

### Seccomp and NNP are enforced

- `NoNewPrivileges=yes`: holds (`NoNewPrivs: 1` in `/proc/PID/status`).
- `SystemCallFilter=@system-service` + `~socket`: the scenario process dies
  with `status=31/SYS` (SIGSYS) on its first bind — seccomp applies to user
  units. Seccomp restricts calls, not file paths, so it cannot cover F1–F7
  alone.

### Profile H2: unprivileged Landlock rule installation rejected (FAIL)

Spike `foundationproof landlock-demo` (committed test-only tooling, no new
dependency beyond the existing `golang.org/x/sys`) creates a ruleset
handling read/write/readdir/execute, allows traversal on `/`, read/write on
the IPC dir and read on `/proc`, then restricts itself. It is fail-closed:
any enforcement error aborts before probing.

On the reference guest (kernel `7.0.0-30-generic`) it aborts with
`add rule /: invalid argument`. The same failure reproduces on the dev host
(kernel `7.1.8`) and in a textbook C program using raw syscalls, so this is
kernel behavior, not a Go calling-convention bug:

- `landlock_create_ruleset` succeeds (returns a ruleset fd);
- `landlock_add_rule` fails `EINVAL` for **every** rule type (1, 2, 99),
  every attr size tried (16/24/32), and with `flags=1`;
- even the documented `LANDLOCK_CREATE_RULESET_VERSION` query fails
  `EINVAL`, although the installed UAPI header documents it;
- lockdown is off (`[none]`), no `landlock=` cmdline restriction, no
  landlock sysctl exists.

Exact kernel-side reason unknown (would need kernel source/bisect — out of
scope). Effect: no Landlock rule can be installed, so unprivileged Landlock
cannot meet F1–F7 on the reference guest either. Both same-account
candidates are now exhausted: unit mount namespacing is silently unapplied
(H1) and Landlock rule installation is rejected (H2).

## Remaining procedure

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
| versions/account/UID mapping recorded | PASS | Ubuntu 26.04, kernel 7.0.0-30, podman 5.7.0, systemd 259, uid 1000, LSM incl. apparmor+landlock |
| canaries + positive controls | PASS | setup-canaries + positive-controls pass in guest |
| allowed binds/IPC usable (open) | PASS | 11/11 scenario checks pass unconstrained (TCP+UDP bind on 198.18.77.2, IPC rw) |
| unit filesystem sandboxing (H1) | FAIL | ProtectHome/ProtectSystem/PrivateUsers accepted but silently unapplied; AppArmor denies sys_admin to systemd-executor userns |
| NNP + seccomp enforcement | PASS | NoNewPrivs=1 holds; ~socket filter kills with SIGSYS |
| forbidden denial under confinement | FAIL | no installable mechanism: H1 silently unapplies, H2 rejects rule installation |
| traversal/unauthorized-socket denial | FAIL | same — unenforceable without a working mechanism |
| restart + reboot confinement retest | NOT RUN | moot — no passing profile exists |

## Decision

- [x] No candidate meets the boundary → blocker recorded above (H1+H2); public
  use stays blocked. Do not silently weaken the boundary.
- [ ] A candidate meets isolation with usable binds/IPC → (not met) would
  accept it in a focused confinement ADR; A1A.2 may proceed.
