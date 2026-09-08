# Ubuntu 26.04 development toolchain

This procedure provisions an authorized disposable Ubuntu 26.04 amd64 host for Gordon v3 foundation tests. It is administrator-managed development tooling, not the Gordon installer. Gordon and test containers run rootless. Keep host AppArmor enabled; per-container AppArmor is subject to the accepted [reference-host exception](adr-006-pasta-pesto-publication.md#reference-host-apparmor-boundary).

## Install prerequisites

On a fresh Ubuntu 26.04 installation, the administrator installs the distribution-provided runtime dependencies:

```sh
sudo apt-get update
sudo apt-get install ca-certificates python3 uidmap dbus-user-session \
  podman crun conmon catatonit libseccomp2
```

Use a normal account with subordinate UID/GID ranges in `/etc/subuid` and `/etc/subgid`. Log into that account through a real user session; verify `XDG_RUNTIME_DIR` and `systemctl --user status`. The administrator enables lingering if user services must survive logout. Do not disable AppArmor, seccomp or user-namespace restrictions as a repair.

## Install a cached bundle

The development bundle contains Podman 6.1.1, Quadlet, pasta/Pesto `2026_07_28.f8df3f1`, Netavark/aardvark-dns 2.1.0 and seccomp configuration. It depends on the Ubuntu packages above; it is not a standalone static distribution.

Obtain the archive and its SHA-256 from a trusted build operator. There is no public Gordon toolchain release or signed download endpoint. An archive accompanied by an attacker-supplied checksum is not authenticated.

From a checkout, as administrator:

```sh
sudo python3 dev/v3/toolchain/install-bundle.py /path/to/bundle.tar.gz TRUSTED_SHA256
```

The installer verifies the archive and file inventory, rejects unsafe members and refuses an existing prefix. It installs under `/opt/gordon-v3-toolchain` without replacing packaged Podman or changing its configuration. It does not create users, enable services, modify firewall rules or load LSM policies.

As the normal account:

```sh
sh dev/v3/toolchain/podman-v3 info
sh dev/v3/toolchain/podman-v3 version
/opt/gordon-v3-toolchain/bin/pasta --version
/opt/gordon-v3-toolchain/bin/pesto --help
```

The wrapper requires a non-root user and `XDG_RUNTIME_DIR`, selects the bundle helpers and uses separate data/config directories under the account's home. Its storage configuration leaves rootless path selection to Podman rather than inheriting distribution storage paths. Do not mix the wrapper and packaged Podman for the same resources.

## Build once, reuse

The build script requires Go 1.27.1, Git, Make, a C compiler, pkg-config, libseccomp development headers and systemd development headers. Build as the normal account from clean source checkouts at the exact commits enforced by the script:

```sh
python3 dev/v3/toolchain/build-bundle.py \
  --podman-source /path/to/podman-source \
  --passt-source /path/to/passt-source \
  --output /path/to/cache/toolchain.tar.gz
```

Netavark and aardvark are downloaded from upstream releases with pinned archive hashes. Source commits are checked, but source signatures are not independently authenticated by this script. The manifest records build package versions, source identities, build flags and file hashes. Keep archives and build inputs outside the repository.

Two successive builds on the development VM produced byte-identical archives. This establishes repeatability in that environment, not an independently reproduced clean-machine build: build packages are recorded, not installed from a frozen package snapshot. Reusing the verified archive avoids compiling for each new VM.

## Validation status

The isolated installed engine reports rootless mode and Netavark and successfully pulls/runs a container. A read-only test container reports zero effective capabilities, no-new-privileges and seccomp filtering; writes to its root are rejected. It reports `crun (unconfined)`, not an AppArmor-confined container.

Bundle-specific Quadlet execution, publication lifecycle, fresh-VM/reboot reuse and the A1A.2 role authority matrix remain acceptance checks. These basic checks do not establish Alpha 1 readiness. See [Alpha 1A](plans/alpha-1a-foundation-proofs.md).
