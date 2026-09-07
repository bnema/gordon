# A1A.0 — Native pasta/Pesto bridge publication proof

Status: complete — native path FAILS on the reference stack (see Results)
Date: 2026-09-07 (executed 2026-09-07)
Task: [alpha-1a-foundation-proofs.md](../plans/alpha-1a-foundation-proofs.md) A1A.0
Gate: N0 — success removes the host-ingress role and all dedicated relay/IPC tasks.

This directory holds sanitized reference-host evidence only.
No credentials, private keys, or bulk OCI payloads.

## Execution record (2026-09-07)

Host tooling was installed per `dev/v3/README.md` and `sandbox up`
succeeded; the VM (`gordon-v3-sandbox`, `qemu:///system`) ran the procedure
below. Reference guest versions:

- Ubuntu 26.04 LTS, kernel `7.0.0-30-generic`
- podman `5.7.0`, netavark `1.16.1`, aardvark-dns `1.16.0-3`
- pasta `0.0~git20260120.386b5f5-1` at `/usr/bin/pasta` (packaged, available)
- no `pesto` binary or package; no user `containers.conf` by default
- host route `198.18.77.0/24 dev gv3test0 src 198.18.77.1` present

## Procedure (executed 2026-09-07; kept as record)

All guest commands run as `gordon` unless noted. Record exact versions first.

### 0. Baseline versions (guest, as gordon + root where noted)

```sh
./dev/v3/sandbox sync
./dev/v3/sandbox gordon -- sh -c 'podman --version; podman info --format "{{.Host.Rootless}} {{.Store.GraphDriverName}}"'
./dev/v3/sandbox gordon -- sh -c 'cat /etc/os-release | head -3; uname -r'
./dev/v3/sandbox exec -- sh -c 'apt-cache policy aardvark-dns podman systemd; ls /usr/libexec/podman/* 2>/dev/null || true'
./dev/v3/sandbox gordon -- sh -c 'podman network ls; ls /usr/libexec/podman/pasta* 2>/dev/null || true; which pasta passt 2>/dev/null || true'
```

Distinguish from the already-tested direct `--network=pasta` path: this proof
covers bridge publication via `rootless_port_forwarder = "pasta"` / Pesto on
named rootless bridges. Upstream docs are not evidence; record packaged
availability on Ubuntu 26.04.

### 1. Empty-engine TCP/UDP publication to a named ingress network

```sh
./dev/v3/sandbox gordon -- sh -c 'podman system reset --force 2>/dev/null || podman rm -af; podman network prune -f; podman volume prune -f'
# build fixtures
./dev/v3/sandbox gordon -- sh -c 'cd ~/src/gordon/dev/v3/fixtures/app-game-test && CGO_ENABLED=0 go build -trimpath -o app-game-test . && podman build -t localhost/app-game-test:dev .'
# named networks: one app-private, one ingress (edge + routed service only)
./dev/v3/sandbox gordon -- sh -c 'podman network create app-priv; podman network create app-ingress'
./dev/v3/sandbox gordon -- sh -c 'podman run -d --name edge-fixture --network app-ingress -p 127.0.0.1::27015/tcp -p 127.0.0.1::27015/udp localhost/app-game-test:dev'
```

Then exercise with two distinct clients (host + guest netns per
`dev/v3/README.md`), checking the fixture's `source=` reflects each kernel
client address, not a rewritten loopback. Cover IPv4 first; IPv6 only where
the guest stack offers it — record either way, do not assume it.

```sh
go run ./dev/v3/cmd/l4probe tcp 198.18.77.2:<port> hello 198.18.77.1
go run ./dev/v3/cmd/l4probe udp 198.18.77.2:<port> hello 198.18.77.1
# second client inside guest netns (see README), expecting source=198.18.77.10
```

Observed (2026-09-07, `edge-fixture` on `app-ingress` with
`-p 198.18.77.2:27015:27015/tcp+udp`). Baseline with the default forwarder
reproduced the ADR-002 limitation for TCP and UDP
(`source=10.89.1.2:...`). After writing `~/.config/containers/containers.conf`
with `[network] rootless_port_forwarder = "pasta"` and recreating the
container, host probes still failed source identity:

```text
$ go run ./dev/v3/cmd/l4probe tcp 198.18.77.2:27015 hello 198.18.77.1
unexpected response: protocol=tcp port=27015 source=10.89.1.3:59406 payload=hello
$ go run ./dev/v3/cmd/l4probe udp 198.18.77.2:27015 hello 198.18.77.1
unexpected response: protocol=udp port=27015 source=10.89.1.3:52825 payload=hello
```

The forwarder processes remained `rootlessport` / `rootlessport-child`; the
config key is absent from the podman `5.7.0` and netavark `1.16.1` binaries
and from the shipped `containers.conf` defaults, so the setting was silently
ignored (`podman info` reports no warning). No second client was attempted:
with the mechanism absent, further source-identity probes cannot pass N0.

### 2. Bidirectional UDP, binary fidelity, backend observation

Current `l4probe` + game fixture prove one text request/one text reply only.
For this proof, additionally record:

- multiple + unsolicited backend replies within one association (fixture gap —
  extend only if baseline passes);
- binary payload round-trip (current probe is LF-delimited text; note as gap);
- backend observation: ordinary edge proxying exposes edge's address at the
  backend — record backend-side `source=` separately, never claim backend
  source preservation from edge metadata.

### 3. Lifecycle: ports, restart, reboot, reuse, stale binds

```sh
# add/remove a published port; edge restart; edge recreate; host reboot;
# port reuse while another container stays running; absence of stale binds
```

Record whether publication changes require edge recreation. Edge-wide
interruption of unrelated routes needs explicit maintainer acceptance — it is
not a passing result.

### 4. Isolation: networks, capabilities, private data

Prove: app-private vs ingress-network isolation; edge/apps contain no
Podman/admin/Pesto capabilities or private data; no host networking, host
descriptor handoff, privileged Gordon operation, dedicated system account, or
system service. Administrator owns firewall/forwarding changes.

## Results

| Check | Result | Evidence |
| --- | --- | --- |
| packaged pasta/Pesto availability | PARTIAL (pasta yes, Pesto absent) | `/usr/bin/pasta`, passt `0.0~git20260120.386b5f5-1`; no `pesto` binary or package |
| pasta port-forwarder mechanism | ABSENT | `rootless_port_forwarder` in neither the podman `5.7.0` nor netavark `1.16.1` binaries nor the shipped `containers.conf` defaults; forwarder processes stay `rootlessport` |
| user-defined-network port handler | DOCUMENTED NAT | shipped defaults: the rootlesskit handler rewrites source IP and "is also used for rootless containers when connected to user-defined networks"; the slirp4netns handler preserves IP but "cannot be used for user-defined networks" |
| TCP publication + source identity | FAIL | host probe from `198.18.77.1` observed as `source=10.89.1.3:59406` (NAT rewrite; default-forwarder baseline showed `10.89.1.2`) |
| UDP publication + source identity | FAIL | host probe from `198.18.77.1` observed as `source=10.89.1.3:52825` (NAT rewrite) |
| port add/remove, restart/recreate, reboot, reuse | NOT RUN | moot — gate check failed; lifecycle of a NAT-rewriting path cannot pass N0 |
| network/capability isolation, private pulls | NOT RUN | moot — same reason; revisited under ingress proofs where applicable |

Observed cleanup note: `podman rm -f edge-fixture` intermittently reports
`rootless netns: kill network process: permission denied` yet still removes
the container (`podman ps -a` empty, no forwarder processes remain). The
fixture container was removed and the ineffective `containers.conf` deleted;
`app-priv` / `app-ingress` networks were left for follow-up procedures.

## Decision

- [x] Native path fails → exact blocker recorded above; no claim that the
  native path works. Returned to the ingress decision: A1A.1–A1B proceed
  under ADR-002/ADR-003, subject to proving same-account rootless
  confinement; otherwise public use remains blocked.
- [ ] Native path meets behavior + isolation → (not met) would amend
  ADR-002/design/plans to the proven four-role topology and remove ingress
  lifecycle, confinement, relay, and IPC tasks.
