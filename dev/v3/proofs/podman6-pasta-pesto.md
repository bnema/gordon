# Podman 6 / pasta / Pesto reference experiment

Date: 2026-09-08
Status: bounded feasibility experiment passed for the scenarios below; not Alpha 1 acceptance
Decision: [ADR-006](../../../docs/v3/adr-006-pasta-pesto-publication.md)

## Environment and method

Authorized disposable Ubuntu 26.04 amd64 VM. Podman 6.1.1 built from tag commit `8303f2e25b675ea7f82099d615c60969aec15870`; pasta/Pesto built from tag `2026_07_28.f8df3f1` (`f8df3f1b228fe19a74a269334fdfe6cc7d0605ce`). Netavark and aardvark-dns 2.1.0 upstream binaries were checked against their published archive SHA-256 lists. Downloads/source tags were not independently signature-verified.

Temporary binaries, configuration, storage, runroot and network definitions were isolated from the installed Podman 5.7 engine. Configuration selected `rootless_port_forwarder = "pasta"` and the temporary helper directory. Tests used the existing scratch game fixture on named rootless bridges, not `--network=pasta` directly. Containers had read-only roots, no added capabilities, no-new-privileges and bounded writable `/data` tmpfs. No Podman or host capability socket was mounted into fixtures.

One client ran outside the VM on the isolated test network; a second ran in a disposable guest network namespace. IPv6 used private test addresses. The existing `l4probe` verified the source field in the fixture's response. These are text request/response tests, not arbitrary binary or unsolicited-reply tests.

## Results

| Scenario | Observation |
| --- | --- |
| TCP and UDP bridge publication, IPv4 | Both distinct client addresses preserved in fixture responses |
| TCP and UDP bridge publication, IPv6 | Private client IPv6 preserved in fixture responses |
| Stop one published container while another runs | Its rules disappeared from Pesto; TCP listener refused connections; other container still answered TCP/UDP |
| Restart stopped container | Declared TCP/UDP publication restored and source preserved |
| Stop all test containers and start again | TCP/UDP IPv4/IPv6 worked again; not a VM reboot test |
| Direct Pesto rule add/delete | New TCP/UDP port worked without destination-container restart; deletion removed the rule and TCP listener |
| Port reuse after stop | TCP/UDP bind succeeded with `SO_REUSEADDR`; Podman restart reused the ports |
| Effective process restrictions | `CapEff=0`, `NoNewPrivs=1`, seccomp filtering active |
| AppArmor | Not proven: container process reported `crun (unconfined)`; both installed 5.7 and experimental 6.1.1 reported AppArmor unavailable to Podman |

The direct-control experiment used the supported syntax:

```text
pesto --add -t HOST_IP/HOST_PORT:CONTAINER_IP/CONTAINER_PORT -u HOST_IP/HOST_PORT:CONTAINER_IP/CONTAINER_PORT SOCKET
pesto --delete -t HOST_IP/HOST_PORT:CONTAINER_IP/CONTAINER_PORT -u HOST_IP/HOST_PORT:CONTAINER_IP/CONTAINER_PORT SOCKET
pesto --show SOCKET
```

Important failed attempts are retained as findings:

- A direct rule targeting a pre-restart container IP timed out. Inspection showed that Podman had assigned a different IP after restart. Repeating with the observed current IP passed. Recovery must reconcile destinations.
- The IPv6 probe initially rejected a correct response because the expected source string lacked IPv6 brackets. Repeating with the matching bracketed representation passed; no forwarding change was required.
- A plain TCP rebind immediately after stop failed with `EADDRINUSE`. Inspection showed TIME-WAIT entries, no listener. Rebind with `SO_REUSEADDR` passed for TCP and UDP; this is not evidence of a retained listening socket.
- The initial source build omitted the AppArmor build tag. Rebuilding with it did not change Podman's reported availability; the installed baseline reported the same limitation. No LSM policy was disabled to obtain a passing network result.

## Cleanup and limits

All three test containers, both temporary Podman networks and the client namespace/veth were removed. No test listeners or test pasta/aardvark helper processes remained. The veth disappeared with namespace deletion; the subsequent explicit link deletion reported it already absent. The original Podman 5.7 binary, fixture image and existing networks remained intact.

VM build dependencies were installed (32 added packages, no upgrades in that transaction); source checkouts, temporary binaries and isolated image storage remain for inspection. No host firewall/sysctl changes, engine reset, production upgrade, Gordon code change or commit was performed.

Not tested: full Gordon edge/backend path, runtime-container access to Pesto, four-role containment, established-flow cleanup on rule deletion, UDP epochs, binary/multiple/unsolicited replies, saturation, private registry pulls, Quadlet recovery, crash journal or VM reboot. Direct Pesto rules are not recorded by `podman inspect` as declared `-p` mappings. Coexistence with Podman setup/teardown and durable recovery need an explicit contract.
