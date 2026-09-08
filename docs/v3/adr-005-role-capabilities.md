# ADR-005: Role filesystem and Unix capability contract

- Status: Proposed; not accepted or implemented; F1 container proofs outstanding
- Date: 2026-09-08
- Scope: A1A.2 under [ADR-004](adr-004-merged-edge.md)
- Decision owner: Gordon maintainer

## Boundaries

Four independent rootless containers communicate through separate Unix capability directories. Mount possession authorizes the fixed handler set, not a caller-supplied role. No common API directory is mounted into a consumer. Host-account/runtime compromise remains outside containment.

This proposal does not select the runtime/Quadlet publication mechanism, install journal, app persistence, public TLS or secret ciphertext format. Those contracts remain separate gates.

## Candidate filesystem layout

Define `D = $XDG_DATA_HOME/gordon` (default `~/.local/share/gordon`) and `R = $XDG_RUNTIME_DIR/gordon`. Require absolute canonical paths, sufficient Unix socket path length, owner matching the installation account and no foreign/symlink path components. Installation owns the directories; creation/refusal and atomic recovery are completed by F3.

| Host directory | Container destination | Writable owner | Other mounts |
| --- | --- | --- | --- |
| `D/control` | `/var/lib/gordon/control` | control | none |
| `D/runtime` | `/var/lib/gordon/runtime` | runtime | none |
| `D/edge` | `/var/lib/gordon/edge` | edge | none |
| `D/registry` | `/var/lib/gordon/registry` | registry | none |
| `D/runtime-key` | `/run/gordon/key` | host initializer only | runtime read-only |
| `R/control-admin` | `/run/gordon/control-admin` | control | host CLI |
| `R/control-edge` | `/run/gordon/control-edge` | control | edge read-only directory mount |
| `R/control-registry` | `/run/gordon/control-registry` | control | registry read-only directory mount |
| `R/runtime-control` | `/run/gordon/runtime-control` | runtime | control read-only directory mount |
| rootless Podman socket directory | `/run/gordon/podman` | host Podman service | runtime only |

Every capability directory contains exactly `api.sock`; the Podman directory retains its native socket name. Read-only consumer mounts prevent socket replacement/unlink, not connecting and sending authorized requests. Socket connect permission is checked separately by Linux; prove it with real mounts.

Only the exact directories above are mounted, never their common parents, the host home, host root, user systemd bus or unit directory. All roles use read-only roots, dropped capabilities, no-new-privileges and default seccomp. Preserve applicable LSM policies; [ADR-006](adr-006-pasta-pesto-publication.md#reference-host-apparmor-boundary) accepts the absence of per-container AppArmor on the Ubuntu rootless reference stack, not the disabling of host AppArmor. Writable temporary files use bounded tmpfs. Network attachment and resource bounds belong to F2/F3; this matrix does not imply broad network access is safe.

## Candidate UID and permissions

Propose a separate `keep-id` user namespace per component, mapping the installation account to container UID/GID `1000:1000`, and explicitly run that user rather than trusting image `USER`. This is one trusted host principal, not four independent host accounts. Mapped IDs, not a hard-coded host UID, own host files.

Use private directories `0700`, ordinary private files `0600`, socket directories `0700` and sockets `0600`. Runtime key directory is `0700`, key `0600`, mounted read-only. Do not recursively chown arbitrary host paths or apply `:U` as a repair. Refuse unexpected owners/modes and preserve unknown contents for operator recovery.

For Linux Unix peers, validate expected mapped UID with `SO_PEERCRED` on both accepted and connected sockets. Do not treat PID, GID alone, JSON role or HTTP headers as role identity. Components may map to the same host UID: role separation is the independent mount namespace plus separate capability handler set. A same-UID process in the trusted host account can impersonate a role; this is an accepted boundary, not a failed token scheme.

SELinux private data uses private labeling; capability directories need deliberate shared labeling between their producer and consumers. Do not disable labeling or seccomp. Ubuntu AppArmor results do not establish SELinux correctness. If the candidate UID/mount policy fails on the reference stack, revise it before acceptance rather than widening permissions.

## HTTP contract proposal

- HTTP/1.1 over Unix sockets, paths under `/v1/`; no TCP administration, proxy environment fallback, redirects, generic RPC or internal bearer tokens.
- Each capability exposes only its role handlers and a read-only identity/readiness response. Readiness distinguishes own state, dependencies and distribution; it is not public route readiness by itself.
- JSON requests require `application/json`, UTF-8, a single top-level object, known fields, no duplicate keys, bounded nesting and exactly one JSON value. No compressed request bodies. Validate domain names/IDs separately; never use user input as a filesystem path.
- Initial ordinary request/response body bound: 1 MiB; headers: 16 KiB; header read timeout: 5 seconds; ordinary request deadline: 30 seconds. These are proposed protocol limits, subject to proof and later bounded endpoint-specific contracts. Secret/OCI payload limits are not selected here.
- Use HTTP statuses 400 (malformed/invalid), 403 (forbidden), 404 (unknown resource), 405 (method), 409 (state/ownership conflict), 413 (too large), 415 (media type), 429 (capacity), 503 (dependency unavailable), and 500 (internal failure). Responses contain a stable `code` and sanitized `message`; do not echo full payloads, credentials or private paths.
- Long operations acknowledge a durable operation ID rather than hold an ordinary request open. A lost response does not prove no effects. Exact idempotency and journals belong to their operation contracts; do not auto-retry mutations at the transport layer.
- Projection/log streams use bounded NDJSON only when required by the owning contract. Client cancellation closes the request and stream resources. Streaming handlers need separate idle/write deadlines and capacity limits; they cannot inherit an unlimited ordinary handler.
- Reject unexpected methods, paths, query fields and upgrade requests. The socket handler set is fixed at composition; no caller-selected role routing.

## Startup and recreation

The host initializer creates volatile capability directories before systemd starts their producers. Reboot removes `R`; F3 must recreate it without adopting foreign entries. A producer owns only its socket path and takes an exclusive process lock in its private state directory before listening.

On restart, inspect the expected path without following symlinks. Refuse non-sockets, foreign ownership and an already live server. Remove an owned stale socket only after establishing exclusive producer ownership; binding failure is not permission to delete arbitrary paths. Set restrictive umask before bind, then verify modes. Consumer directory mounts survive socket inode replacement; clients reconnect with bounded backoff and verify peer/role/distribution again.

A consumer may start before its producer and report dependency-unavailable without terminating unrelated roles. Control must not require edge readiness merely to expose its projection; runtime must not require an active workload to expose its control socket. Actual target ordering and dependency cycles are verified in F3.

## Required evidence before acceptance

| Scenario | Required result |
| --- | --- |
| Each authorized producer/consumer pair | Connect, identify intended peer/distribution and use only its fixed handlers |
| Edge/registry against admin/runtime control/Podman | Paths absent and direct connection impossible |
| Edge/app against each private store and runtime key | Mount absent; known canary exists for the trusted positive control |
| Consumer attempts socket replacement | Read-only mount rejects unlink, bind replacement and symlink substitution |
| Wrong UID or socket owner/mode | Refusal without chmod/chown repair |
| Same-UID endpoint swap | Role/distribution mismatch refused; same-host-principal impersonation explicitly not contained |
| Producer restart and socket recreation | Existing mount reaches new inode; stale connections fail and reconnect safely |
| Reboot with absent runtime directories | Owned directories recreated before bind; no broad host mount |
| Malformed/duplicate/unknown/oversized JSON, slow headers | Bounded rejection without effects or leaked payload |
| Cancelled/disconnected stream and saturation | Bounded goroutines/memory and prompt resource cleanup |
| Userns, dropped capabilities, seccomp, NNP, read-only root and applicable LSM policies | Protections observed in running containers, not inferred from flags; report the accepted Ubuntu per-container AppArmor exception separately |

Candidate validation must run on the authorized Ubuntu reference VM and use synthetic data. No production APIs or installer paths are implemented until the corresponding decision is accepted and proof evidence is reviewed.
