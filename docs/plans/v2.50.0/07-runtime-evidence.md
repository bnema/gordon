# 07 — Runtime Feasibility Evidence (P0.2)

Status: evidence record, 2026-09-08. Bounded fixtures backing D1–D3.
No production containers, firewalls, or sysctls touched. Fixture
containers used `busybox:1.36.1` / `alpine:3.21` only; all removed
after each probe (trap-cleanup scripts, since removed).

## 1. Environment

| Item | Value |
|---|---|
| Docker server | 29.7.2, driver overlayfs, cgroup v2 (systemd), ROOTLESS (`/run/user/1000/docker.sock`, API v1.55) |
| Docker IPv6 | NOT enabled on default/user bridges (`EnableIPv6: false`, single `/16` v4 IPAM) |
| IPv6 custom network | `docker network create --ipv6` FAILED in this environment (daemon refused setup) |
| Podman | ABSENT (`podman: command not found`) — Podman evidence PENDING, see §6 gap |
| Go toolchain | go1.27.0; `pelletier/go-toml/v2 v2.4.3` `DisallowUnknownFields` verified in module cache |
| Registry push chunking | `pkg/registrypush` `DefaultChunkSize = 50*1024*1024`, server `max_blob_chunk_size` default 95 MiB (code-verified, unchanged) |

## 2. Private network + aliases (backs D3 §4, manifest §6)

- User-defined bridge: short DNS names AND multiple
  `--network-alias` values resolve intra-network (`nslookup web.` →
  `172.21.0.2`; `wget http://web-alias:8080/` → served body). NOTE:
  bare `nslookup web` without trailing dot is hijacked by the host
  `search netbird.cloud home` + `ndots:0` (host resolv.conf
  inheritance) — diagnostics/tooling note, not a reachability
  failure; HTTP via short name works.
- Cross-network isolation: a container on network B cannot resolve
  or ping names on network A (verified `ping` failure + NXDOMAIN).
- AT-CREATE aliases are recorded (`DNSNames` includes name + both
  aliases, API-verified via `/containers/{id}/json`).

## 3. Post-creation alias connect does NOT propagate (contract consequence)

`docker network connect --alias db-alias <netB> <ctr>` (after
`disconnect` from netA): endpoint inspection shows `Aliases: None`,
`nslookup db-alias` → NXDOMAIN. FROZEN CONSEQUENCE (04-network.md
§4): aliases are set ONLY at container creation; membership changes
needing new aliases re-attach the endpoint with the complete alias
set (disconnect + connect with full aliases was NOT re-probed with
aliases on the second connect — P3 fixtures must cover that variant
explicitly, plus the alias-at-create path the engine will use).

## 4. Dual-stack binds (backs reservation §3.5)

- IPv6 unavailable (rootless, §1): `publish = "port"` (no IP) claims
  IPv4 only here; the reservation records `dual: false`. No silent
  IPv6 promise. Platforms WITH IPv6 record `dual: true` — P3 fixtures
  on an IPv6-capable host must prove the dual entry before claiming it.
- Wildcard-vs-specific overlap default = conflict (no platform proof
  of disjoint binds was attempted; P3.2 proves per-platform or keeps
  the conservative default).

## 5. Bind conflicts & UDP shutdown (backs deploy §6–§7)

- Second `docker run -p 127.0.0.1:18081:80` while bound → daemon
  error `Bind for 127.0.0.1:18081 failed: port is already allocated`.
  Real bind failure is observable and mappable to `bind-failed`.
- UDP (`nc -l -u -p 9000 -e cat`): datagrams flow intra-network
  (`echo hello-udp | nc -u` received). `docker stop -t 5` on the
  UDP listener waited the FULL timeout (5.1s) — SIGTERM ignored, no
  graceful drain exists. FROZEN: UDP/game replacement is DECLARED
  interrupting; deploy output states interruption, never
  zero-downtime; UDP readiness restricted to `none`/`log`.
- Observed source IP behind proxy (TCP): NOT probed end-to-end (needs
  the Gordon proxy in path). RECORDED OPEN: peer-identity and
  UDP-forwarding proofs are required before any route-CIDR control
  (04-network.md §5). P3 fixtures must demonstrate per-runtime
  source-IP behavior through the actual proxy, not direct dials.

## 6. Gaps (explicit, blocking where noted)

1. **Podman evidence MISSING** (no binary on this machine). Required
   before P3: rootless-Podman host→container-IP dial, alias-at-create
   behavior, pasta-network notes, security-profile generated-config
   parity. Recommendation frozen in 04-network.md D3.5: proceed P1–P2
   (runtime-independent), GATE P3 on Podman evidence.
2. Alias-change re-attach variant (`disconnect` + `connect --alias
   ...` WITH aliases on the second connect) not probed — P3 fixture.
3. UDP source-identity through proxy not probed — P3 fixture.
4. `connect --alias` behavior was rootless-Docker-specific; P3 must
   re-prove on daemon Docker AND Podman before generalizing the
   alias-at-create rule beyond "at least this platform needs it".

## 7. Fixture record (reproducibility)

Probes ran as `/tmp/p02_*.sh` (trap-cleanup, unique `p02*` prefixes,
ephemeral bridge networks, no volumes, no published host ports except
one `127.0.0.1:18081` conflict probe, since removed). Images pulled:
`alpine:3.21` (already-cached `busybox:1.36.1`, `alpine:3.24` reused).
No `iptables`/`sysctl`/firewall changes. `docker network ls` and
`docker ps -a` verified clean after each probe.
