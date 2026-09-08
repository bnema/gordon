# A1A.2 reference toolchain checkpoint

Status: partial evidence; F1 is not accepted.

## Environment

Ubuntu 26.04 amd64, isolated Podman 6.1.1 and Quadlet, pasta/Pesto `2026_07_28.f8df3f1`, Netavark/aardvark-dns 2.1.0. The cached development bundle SHA-256 is `e96b572e3ece918b1ef115ddc47b6d897fd09448a229bde11d2fa45a8929d8a0`. Two successive builds on the same VM produced this identical archive. It is cached outside the repository and installed under `/opt/gordon-v3-toolchain`; packaged Podman is unchanged.

The installed engine reports rootless mode, isolated account-owned graph storage and Netavark. The wrapper supplies separate storage configuration because the distribution's explicit storage paths override Podman 6's rootless defaults.

## Observed checks

A digest-pinned Alpine fixture (`docker.io/library/alpine@sha256:1beb0dc0a51de7ff38e3b5274078a2e0b81113ba5c7535e1a03d5913a5edbda3`) ran with:

- `--network=none`, `--userns=keep-id:uid=1000,gid=1000`, `--user=1000:1000`;
- read-only root, dropped capabilities, no-new-privileges;
- PID limit 32 and memory limit 64 MiB requested.

The process reported UID/GID 1000, zero effective capabilities, `NoNewPrivs=1` and `Seccomp=2`. Separate root-write testing failed with a read-only-filesystem error. No Podman socket, runtime store or secret-key directory existed at the tested container paths. No host capability or private-store mount was supplied.

These absence checks establish only the empty fixture's mount boundary. They are not a producer/consumer role matrix and lack positive canary controls. Resource limits were requested, not stress-tested. The container reports `crun (unconfined)`; the accepted AppArmor exception applies.

## Open acceptance work

- F1's exact runtime-only Pesto directory, bootstrap/recreation and peer-authority contract is still proposed/incomplete.
- Actual four-role socket connectivity, positive private-store canaries, denied role access, socket recreation and malicious peer/request tests are not covered by this checkpoint.
- Strict HTTP/JSON handlers and bounded streaming are not implemented by these fixtures.
- Quadlet execution, publication from this installed bundle and fresh-VM/reboot reuse remain unverified.

Do not mark A1A.2 complete or infer product recovery from these checks. Runtime's engine-wide Podman/Pesto authority remains an accepted risk requiring explicit contracts and negative tests.
