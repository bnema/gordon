# Gordon v3 development VM

The sandbox wraps libvirt, cloud-init and SSH to run an isolated Ubuntu 26.04 amd64 development VM. It does not install or supervise Gordon. [Alpha 1A](../../docs/v3/plans/alpha-1a-foundation-proofs.md) defines the container, capability, publication and recovery proofs.

## Host prerequisites

On CachyOS/Arch, the administrator installs libvirt, QEMU, passt, cloud-image-utils, curl, OpenSSH, dnsmasq and nftables, enables the libvirt sockets and grants the development account access to `qemu:///system`. Go 1.27, Git, GNU tar and KVM are required. Sandbox commands run as the normal user; they never change host authorization, DNS or trust stores.

```sh
virsh -c qemu:///system uri
./dev/v3/sandbox up
./dev/v3/sandbox sync
```

The VM has four CPUs, 4 GiB RAM and a 40 GiB sparse disk. SSH is key-only on `127.0.0.1:2222`, with a pre-pinned host key and no agent forwarding. The isolated test network uses `198.18.77.0/24`; host and guest addresses are `198.18.77.1` and `198.18.77.2`. Libvirt resources are named `gordon-v3-sandbox`. Only one sandbox is supported per host.

## Required container stack

Use the [Ubuntu toolchain guide](../../docs/v3/ubuntu-toolchain.md) to install the cached Podman 6.1.1 + pasta/Pesto bundle and runtime dependencies. The cloud image's packaged engine alone does not satisfy the v3 baseline. `sandbox up` does not yet provision the bundle automatically.

Copy the trusted archive into the guest with the sandbox's dedicated SSH key and pinned known-hosts file, then install it:

```sh
./dev/v3/sandbox exec python3 \
  /home/gordon/src/gordon/dev/v3/toolchain/install-bundle.py \
  /path/to/bundle.tar.gz TRUSTED_SHA256
./dev/v3/sandbox gordon sh \
  /home/gordon/src/gordon/dev/v3/toolchain/podman-v3 info
```

Use the explicit `podman-v3` wrapper for foundation tests. It selects isolated configuration/storage and leaves packaged Podman resources unchanged. Host AppArmor remains enabled; per-container AppArmor is not available on the selected rootless stack. Verify the remaining protections in actual containers.

## Daily commands

```sh
./dev/v3/sandbox ssh
./dev/v3/sandbox exec cloud-init status --long
./dev/v3/sandbox gordon systemctl --user is-system-running
./dev/v3/sandbox sync
./dev/v3/sandbox stop
```

- `ssh` and `exec` use the guest root account for explicit administrator actions.
- `gordon` runs as the unprivileged test account with its user-systemd environment.
- For shell syntax, pass `sh -c '...'` explicitly.
- `sync` overlays tracked and non-ignored files into `/home/gordon/src/gordon`, excluding `.git`. It does not remove deleted local files from the guest. Never put secrets in the synced checkout.
- `stop` preserves the disk. `up` starts it again.
- `destroy --yes` deletes the owned VM, disk, seed and network, including test data. Inspect ownership and preserve needed evidence first. Never use teardown as a substitute for a product recovery test.

## Fixtures and proofs

`fixtures/app-web-test` and `fixtures/app-game-test` are independent Go modules. `cmd/l4probe` checks TCP/UDP responses and observed client addresses. `cmd/foundationproof` provides bounded foundation experiments. Running a fixture directly with Podman is not evidence that Gordon's role boundaries or recovery work.

Use synthetic data and digest-pinned images for accepted proof runs. Keep raw logs, source archives, binaries and bundle caches outside the repository. Reports contain only relevant sanitized results and explicitly unverified checks.

## Local state

- `$XDG_STATE_HOME/gordon/v3-sandbox` (default `~/.local/state/...`): private ownership record, SSH keys, seed and generated XML.
- `$XDG_CACHE_HOME/gordon/v3-sandbox` (default `~/.cache/...`): verified base image and optional toolchain archives. Keep the expected bundle checksum separately trusted.
- `/var/lib/libvirt/images/gordon-v3-sandbox`: libvirt disk and seed.

The base Ubuntu image and Go archive hashes are pinned in the wrapper/templates. Apt dependencies are not frozen; this is not a byte-reproducible entire operating system. The seed contains a private guest host key and must remain protected.

## Diagnostics

```sh
virsh -c qemu:///system list --all
./dev/v3/sandbox exec systemctl --failed
./dev/v3/sandbox gordon sh \
  /home/gordon/src/gordon/dev/v3/toolchain/podman-v3 info
```

Libvirt permission or conflicting resource errors require administrator inspection. The wrapper does not repair them by changing polkit, removing ownership records or deleting foreign resources. Host firewall and privileged-port prerequisites remain administrator-owned.
