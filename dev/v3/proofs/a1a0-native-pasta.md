# A1A.0 — Native pasta/Pesto bridge publication proof

Status: in progress (runbook prepared; VM execution blocked on host tooling)
Date: 2026-09-07
Task: [alpha-1a-foundation-proofs.md](../plans/alpha-1a-foundation-proofs.md) A1A.0
Gate: N0 — success removes the host-ingress role and all dedicated relay/IPC tasks.

This directory holds sanitized reference-host evidence only.
No credentials, private keys, or bulk OCI payloads.

## Blocker (2026-09-07)

`./dev/v3/sandbox up` fails on this host before VM creation:

```text
sandbox: exec: "cloud-localds": executable file not found in $PATH
```

Missing host tools: `cloud-localds` (`cloud-image-utils`), `passt`.
`virsh`, `qemu-img`, `ssh`, `curl`, `ip`, `dnsmasq` are present.
Administrator action required (once per dev host, per `dev/v3/README.md`):

```sh
sudo pacman -S --needed qemu-full libvirt passt edk2-ovmf cloud-image-utils curl openssh dnsmasq nftables
sudo systemctl enable --now virtqemud.socket virtnetworkd.socket virtstoraged.socket
```

No VM was created; no proof assertions have passed. Do not claim native
functionality until the procedure below is executed on the authorized
disposable host and reviewed.

## Procedure (to run once `sandbox up` works)

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
| packaged pasta/Pesto availability | NOT RUN | — |
| TCP publication + source identity (2 clients) | NOT RUN | — |
| UDP bidirectional + binary fidelity | NOT RUN | — |
| port add/remove, restart/recreate, reboot, reuse | NOT RUN | — |
| network/capability isolation, private pulls | NOT RUN | — |

## Decision

- [ ] Native path meets behavior + isolation → amend ADR-002/design/plans to
  the proven four-role topology; remove ingress lifecycle, confinement, relay,
  and IPC tasks; retain applicable network/reservation/recovery tests.
- [ ] Native path fails/incomplete → record exact blocker; return to the
  ingress decision. No claim that the native path works.
