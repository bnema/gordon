# Manual validation — 2026-09-04

Reference guest: pinned Ubuntu 26.04 amd64 cloud image, Go 1.27.1, rootless Podman. Validated with KVM and system libvirt.

The final KISS wrapper was exercised against a real VM, as an unprivileged host user with previously authorized system libvirt. No host sudo command, DNS setting, trust-store mutation, or authorization-policy change was needed.

## Passed

- `up`: new disk and private network, pinned-key SSH, cloud-init completed without errors.
- Guest `gordon`: Podman reports `rootless=true`; systemd user manager reports `running`; lingering survives reboot.
- `sync`: copied the worktree; both example modules built inside the VM.
- SSH configuration: root key-only, passwords and agent forwarding disabled; `127.0.0.1:2222` listener only; port 22 unreachable through the test network.
- README's PostgreSQL 18 / Valkey 9 / web commands: both counters increment through HTTP at `198.18.77.2:8080`.
- TCP 27015 and 27016, UDP 27015: payload round trips and source address `198.18.77.1` from the host.
- README's guest client namespace: TCP/UDP source address `198.18.77.10` preserved through direct rootless Podman publication.
- Private TCP 27017, 5432 and 6379: connection refused from the host; RCON self-probe succeeds inside the game container.
- `stop` / `up`: graceful shutdown and restart; manually restarted fixture containers retain PostgreSQL data and game event logs.
- Browser-style access: guest-only hosts entry and SSH SOCKS5 tunnel, tested with `curl --socks5-hostname`; tunnel removed afterward.
- `destroy --yes`: VM, NVRAM, seed, disk, pool and private network removed. No sandbox listener remains on 2222 or 1080. Existing default libvirt network left unchanged.

## Limits

- Fixtures are manually run Podman containers, not Gordon-managed applications. Their manifests are prospective examples, not validated by a v3 parser.
- No claim is made about Gordon's future L4 proxy, TLS edge, privileged ports 80/443, workload security policy, or installer. Those still require their planned ADRs and implementation.
- HTTPS/CA and wildcard DNS are not automated in this reduced wrapper. No browser trust store was changed or browser UI tested.
- Guest apt package versions are not pinned. The base-image checksum does not make the complete installation reproducible.
- Failure injection, arbitrary host firewall/VPN combinations, ARM64 and multiple simultaneous sandboxes are outside this initial validation.

Next work is implementation planning for Gordon v3, not starting its implementation in this PR.
