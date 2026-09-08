# Pesto authority and lifecycle proof

Status: bounded VM experiment; direct mount/lifecycle candidate fails acceptance.

## Setup

Ubuntu 26.04 with the installed [development bundle](../../../docs/v3/ubuntu-toolchain.md), rootless only. Synthetic containers use a dedicated `10.91.0.0/24` bridge and test host ports 18080/18081. No production resources or firewall rules are changed.

Fixtures are pinned to Alpine digest `sha256:1beb0dc0a51de7ff38e3b5274078a2e0b81113ba5c7535e1a03d5913a5edbda3` and Ubuntu digest `sha256:2260313b31c8c011cd2eebe728008efac1b3982be73eb71348ea2648d2c0e09b`. The Ubuntu fixture executes the bundle's glibc-linked Pesto client; Alpine cannot execute that binary. Both run read-only with dropped capabilities and NNP. Consumer fixtures use no network and keep-id UID/GID 1000. These are capability fixtures, not implemented Gordon roles.

## Results

| Test | Observation |
| --- | --- |
| Start bridge container without published ports | Native pasta starts with empty HOST/SPLICE tables and a control socket; no dummy public rule needed |
| Native capability directory inventory | Socket shares a directory with namespace handle, PID, reference count, resolver/configuration files and a nested run directory; it is not a socket-only directory |
| Runtime-shaped fixture, read-only native directory and toolchain mounts | Pesto show and add succeed without networking or Linux capabilities |
| Unlink socket through read-only mount | Rejected with read-only filesystem error; this does not prevent forwarding mutations |
| Edge/control/registry/app-shaped fixtures without capability mount | Pesto connect fails with missing endpoint; same binary/image provides positive comparison with runtime-shaped fixture |
| Wrong UID with capability mount | Connect fails with permission denied |
| Direct rule and destination deletion while another bridge container remains | Rule remains in Pesto after Podman removes destination |
| Unrelated container reuses destination IP | External client reaches replacement response through the old publication, without a new Pesto command |
| Last bridge container removed while network-none consumer retains directory mount | Native socket/directory disappear; new bridge container creates an empty working endpoint, but existing consumer mount cannot reach it |
| Delete rule during established TCP echo connection | Existing connection still sends/receives after successful rule deletion |

Native socket observed mode is 0755 inside an account-owned 0700 directory. Filesystem permission checks are demonstrated; native protocol peer credential checks have not been audited. The mount exposes more than the desired control socket and is not approved for production.

## Reproduced unsafe destination reuse

1. Start backend A at `10.91.0.10:8080` and an independent bridge anchor.
2. Add `-t 198.18.77.2/18080:10.91.0.10/8080` using Pesto.
3. External client receives `backend-A`.
4. Remove backend A; `pesto --show` still lists its direct rule.
5. Start unrelated B at the same address, without publication flags.
6. External client receives `unrelated-B` through port 18080 without changing the rule.

No Gordon runtime reconciler participates. This models its absence, not a full runtime-crash journal test. Address-only rules plus eventual reconciliation do not prevent unauthorized exposure. A preventative ownership/lifecycle contract is required before accepting direct publication.

## Dedicated edge-address candidate

A follow-up used separate, non-overlapping synthetic edge (`10.92.0.0/24`) and app (`10.93.0.0/24`) bridges. Only edge fixtures received the fixed publication destination `10.92.0.10`. Both used the same pinned Alpine image and restrictions above.

1. A Pesto TCP rule to edge A returned `edge-A` to the external client.
2. Edge A was removed while the app fixture remained running on its separate bridge. The rule remained, but the external probe timed out rather than reaching the app.
3. Edge B was created at the fixed edge address. Without changing the rule, the external client received `edge-B`.
4. The rule was explicitly deleted before removing edge B. Creating another edge fixture at the same address did not restore the rule: Pesto tables stayed empty and a new external connection was refused (errno 111).

This supports fixed edge destination feasibility under the manually enforced network assignment. It does not prove a durable allocator or prevent a Podman-authorized caller from placing an unrelated container on the edge network. Gordon must enforce exclusive edge network/address ownership, reject overlapping allocations and validate recovery before opening listeners. The surviving rule automatically reaches a replacement as soon as it listens; application readiness/authorization is not provided by Pesto. Established-flow cleanup and UDP remain separate gates. Both networks and all follow-up containers were removed by exact names.

## Network-attached runtime and restart experiment

Two restricted Ubuntu fixtures represented edge and runtime on a dedicated bridge. Runtime used keep-id UID/GID 1000 and the same read-only capability/toolchain mounts; it was attached to the bridge rather than network-none.

- Removing edge while runtime remained attached preserved working Pesto access. Runtime's own network attachment kept the native context alive; no extra keeper container was added.
- Killing the exact native pasta PID caused runtime's Pesto connection to fail with connection refused. Creating a new edge fixture made Podman report the dead helper and restart it. The existing runtime mount then reached the new socket, with empty forwarding tables. This is recovery triggered by a Podman operation, not demonstrated autonomous supervision.
- Stopping both fixtures, starting edge first and then runtime restored access through runtime's remounted directory.
- After an actual VM reboot, the first attempted Podman start failed with `failed to reexec: Permission denied`. A subsequent diagnostic invocation and ordered edge/runtime start succeeded without configuration or protection changes. The cause of the initial failure remains unresolved. Bundle binaries were reused without compilation; Pesto was reachable with empty tables after manual startup.

These results support context retention by a legitimate network-attached runtime and ordered remounting, but do not validate an automatic Quadlet recovery path. No forwarding rules were present in this lifecycle experiment; authorized-rule replay was not tested. Fixtures and the dedicated bridge were removed after the test. The native-directory inventory concern remains unchanged.

## Consequences and remaining work

Runtime remains the sole intended Pesto capability holder. The naive native-directory mount fails both least-mount and stable-recreation assumptions. Rule deletion is not established-flow termination. These failures block accepting this candidate, not the selected Podman/pasta stack.

Unverified: UDP established associations/late replies, native command authorization internals, alternate host addresses and wildcard conflicts, independent socket replacement, autonomous pasta-death/reboot recovery and durable mapping recovery. No UDP or general recovery guarantee follows from these tests. Resolve the lifecycle and destination-identity contract before production integration; do not add broad mounts, host executors or disable protections to pass.

## Cleanup

All experiment containers and the dedicated bridge were removed by their exact owned names. The isolated engine has no remaining containers; its native Pesto endpoint disappeared after final teardown. Cached fixture images remain. Initial HTTP fixture attempts exited because Alpine lacks the HTTP applet; TCP fixtures used its available netcat instead. No engine reset or unrelated cleanup was performed.
