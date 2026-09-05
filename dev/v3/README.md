# Gordon v3 dev VM

A small Go wrapper around **libvirt + cloud-init + SSH**. It creates an Ubuntu 26.04 amd64 VM, copies a checkout, and stops or deletes the VM. No Makefile additions, deployment engine, daemon, or host DNS/trust-store management.

Gordon v3 is not implemented yet. The example apps below run directly with rootless Podman; their success is **not** evidence that Gordon's edge, installer, routes, or security policy works.

## Ubuntu dependency caveat: socket activation

As checked on **2026-09-05**, the Ubuntu 26.04 guest installs `aardvark-dns`
**1.16.0-3** alongside Podman; that is also the candidate in its configured Ubuntu
repositories. It does **not** include the socket-inheritance fix in upstream
**2.1.0**. Check your guest rather than assuming the package version:

```sh
./dev/v3/sandbox exec apt-cache policy aardvark-dns
```

With rootless Quadlet socket activation and named bridge networks, the affected
DNS helper can retain inherited TCP/UDP listeners after the container and socket
units stop. Restarting a TCP socket then fails with `Address already in use`.
See [the upstream report](https://github.com/podman-container-tools/podman/issues/27854)
and [the fix](https://github.com/containers/aardvark-dns/pull/710).

**Development workaround:** use upstream aardvark-dns **2.1.0**, or a distribution
package with that fix backported, for socket-activation experiments. A temporary
2.1.0 substitution in the disposable VM passed TCP/UDP forwarding, listener
withdrawal with another container still running, and subsequent restart. The
packaged binary was restored afterward. If testing a source build, preserve the
original binary and restore it after stopping the experimental containers and
networks; do not replace packages on a production host using this procedure.

The sandbox does not apply this workaround automatically. The direct-Podman
examples below do not use socket activation. A fixed DNS helper alone does not
establish Gordon's ingress design or resolve dynamic-listener ownership and
other Alpha 1 security/lifecycle requirements.

## CachyOS setup (once)

The administrator installs and authorizes libvirt once. After that, **every sandbox command runs as your normal user without sudo**.

```sh
sudo pacman -S --needed qemu-full libvirt passt edk2-ovmf \
  cloud-image-utils curl openssh dnsmasq nftables
sudo systemctl enable --now virtqemud.socket virtnetworkd.socket virtstoraged.socket
sudo usermod -aG libvirt "$USER"
```

Log out/in, then check `virsh -c qemu:///system uri`. Some hosts require an explicit libvirt/polkit authorization policy; configure that as administrator, not through this tool. Go 1.27, Git, GNU tar and KVM are required. The wrapper never installs host packages or changes authorization policy.

For a CachyOS mirror 404, refresh mirrors with `sudo cachyos-rate-mirrors`, then perform a **full** upgrade with `sudo pacman -Syyu`. Never use a standalone `pacman -Sy`.

## Daily use

From the checkout:

```sh
./dev/v3/sandbox up
./dev/v3/sandbox ssh
./dev/v3/sandbox exec uname -a
./dev/v3/sandbox gordon podman info
./dev/v3/sandbox sync
./dev/v3/sandbox stop
./dev/v3/sandbox destroy --yes
```

- `up` waits for SSH and cloud-init, with a ten-minute readiness deadline. First boot downloads about 824 MiB and installs guest packages.
- `ssh` logs in as root using a dedicated key. `gordon` runs a command as the unprivileged guest account with its own systemd user environment.
- `exec` and `gordon` accept a program and arguments. For shell syntax, pass `sh -c '...'` explicitly.
- `sync` sends Git-tracked and non-ignored files to `/home/gordon/src/gordon`, excluding `.git`. It is an overlay, not a delete/mirror operation: removed local files remain until you remove the guest checkout. Do not put secrets in tracked/non-ignored files.
- `stop` waits up to a minute for graceful shutdown, preserving data.
- `destroy --yes` removes only the owned VM, disk, seed and network. Ubuntu's verified download remains cached. To reset, run `destroy --yes`, then `up`.
- Partial creation is not automatically repaired. Inspect the error, run `destroy --yes`, then retry. Never delete the ownership record to bypass a conflict.

## Fixed, explicit defaults

| Setting | Value |
| --- | --- |
| Guest | Ubuntu 26.04 amd64, Go 1.27.1 |
| Resources | 4 vCPU, 4 GiB RAM, 40 GiB sparse disk |
| SSH | `127.0.0.1:2222`, root key-only, no agent forwarding |
| Test network | isolated `198.18.77.0/24` |
| Host / VM IP | `198.18.77.1` / `198.18.77.2` |
| Libvirt names | `gordon-v3-sandbox` |

For KISS, these are fixed in the templates and wrapper, not another configuration API. Only one VM is supported per host. Address conflicts fail instead of replacing routes.

`cloud-init/` contains readable VM, network and pool XML plus cloud-init files. The official Ubuntu URL and SHA-256 are pinned in `cmd/sandbox/main.go`; updates are ordinary reviewed code changes. Guest apt packages are **not** frozen, so the checksum pins the base image, not a byte-reproducible entire VM.

Runtime files:

- `$XDG_STATE_HOME/gordon/v3-sandbox` (default `~/.local/state/...`): UUID ownership record, dedicated client/host SSH keys, seed and generated XML; private to your user.
- `$XDG_CACHE_HOME/gordon/v3-sandbox` (default `~/.cache/...`): verified Ubuntu image.
- libvirt pool `/var/lib/libvirt/images/gordon-v3-sandbox`: disk and cloud-init seed.

The seed contains the guest SSH host key. Protect the state directory and libvirt storage. The key is pinned **before boot**, not learned via `ssh-keyscan`. The guest has outbound access through passt; this is a development VM, not a malware-analysis sandbox.

## Example apps (manual, inside the VM)

The fixtures are independent Go modules so their dependencies do not affect Gordon. First run `sandbox sync`, then enter the guest as gordon:

```sh
./dev/v3/sandbox gordon bash
```

### app-web-test: web + PostgreSQL + Valkey

In that guest shell:

```sh
cd ~/src/gordon/dev/v3/fixtures/app-web-test
CGO_ENABLED=0 go build -trimpath -o app-web-test .
podman build -t localhost/app-web-test:dev .
podman network create app-web-test
podman volume create app-web-postgres-data
podman run -d --name app-web-postgres --network app-web-test --network-alias postgres \
  -e POSTGRES_USER=app -e POSTGRES_PASSWORD=sandbox -e POSTGRES_DB=app \
  -v app-web-postgres-data:/var/lib/postgresql:U \
  docker.io/library/postgres@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2
podman run -d --name app-web-valkey --network app-web-test --network-alias valkey \
  docker.io/valkey/valkey@sha256:ccfa19b0d743e48927e1c8c14e39e0acb97b5cea347fef0bfe340247fea920cd
podman run -d --name app-web-test --network app-web-test -p 198.18.77.2:8080:8080 \
  -e 'DATABASE_URL=postgres://app:sandbox@postgres:5432/app?sslmode=disable' \
  -e VALKEY_ADDR=valkey:6379 localhost/app-web-test:dev
```

These are disposable **test credentials**, never production defaults. PostgreSQL 18's volume belongs at `/var/lib/postgresql`. DB/cache have no host port publication.

From CachyOS, without modifying DNS:

```sh
curl --resolve app-web-test.gordon.test:8080:198.18.77.2 \
  http://app-web-test.gordon.test:8080/
```

Each successful request increments PostgreSQL and Valkey counters. A 503 during dependency startup is expected; retry after checking `podman logs app-web-postgres`.

### app-game-test: one service, public TCP/UDP and private RCON

In the guest gordon shell:

```sh
cd ~/src/gordon/dev/v3/fixtures/app-game-test
CGO_ENABLED=0 go build -trimpath -o app-game-test .
podman build -t localhost/app-game-test:dev .
podman volume create app-game-test-data
podman run -d --name app-game-test \
  -p 198.18.77.2:27015:27015/tcp -p 198.18.77.2:27015:27015/udp \
  -p 198.18.77.2:27016:27016/tcp \
  -v app-game-test-data:/data:U localhost/app-game-test:dev
podman exec app-game-test /app-game-test probe 127.0.0.1:27017
```

No domain is needed. `27017/tcp` exists inside the container but is not published. Events are stored in the game's named volume.

From CachyOS:

```sh
go run ./dev/v3/cmd/l4probe tcp 198.18.77.2:27015 hello 198.18.77.1
go run ./dev/v3/cmd/l4probe udp 198.18.77.2:27015 hello 198.18.77.1
go run ./dev/v3/cmd/l4probe tcp 198.18.77.2:27016 status 198.18.77.1
go run ./dev/v3/cmd/l4probe closed 198.18.77.2:27017 unused 198.18.77.1
```

For a distinct L4 client, create a namespace **inside the guest** via `sandbox ssh`:

```sh
ip netns add client
ip link add client-host type veth peer name client-ns
ip link set client-ns netns client
ip address add 198.18.77.9/32 dev client-host
ip link set client-host up
ip route add 198.18.77.10/32 dev client-host
ip -n client address add 198.18.77.10/32 dev client-ns
ip -n client link set lo up
ip -n client link set client-ns up
ip -n client route add 198.18.77.2/32 dev client-ns
cd /home/gordon/src/gordon
go build -o /tmp/l4probe ./dev/v3/cmd/l4probe
ip netns exec client /tmp/l4probe tcp 198.18.77.2:27015 hello 198.18.77.10
ip netns exec client /tmp/l4probe udp 198.18.77.2:27015 hello 198.18.77.10
ip netns delete client
```

This exercises rootless publication with a separate client address. It does not replace external ingress/firewall tests or prove that a future L4 proxy preserves source IP.

## Browser DNS and TLS: explicit and optional

No host resolver or system trust-store changes. For browser testing, add the name only to the **guest** `/etc/hosts`:

```sh
./dev/v3/sandbox exec sh -c 'printf "198.18.77.2 app-web-test.gordon.test\n" >> /etc/hosts'
```

Use a foreground SSH SOCKS tunnel in another terminal (Ctrl-C closes it):

```sh
state="${XDG_STATE_HOME:-$HOME/.local/state}/gordon/v3-sandbox"
ssh -F /dev/null -i "$state/client" -p 2222 \
  -o UserKnownHostsFile="$state/known_hosts" -o StrictHostKeyChecking=yes \
  -o ForwardAgent=no -o ExitOnForwardFailure=yes -N -D 127.0.0.1:1080 root@127.0.0.1
```

Configure a dedicated browser profile for SOCKS5 `127.0.0.1:1080` with remote DNS, or use `curl --socks5-hostname 127.0.0.1:1080 http://app-web-test.gordon.test:8080/`.

HTTPS/CA generation is intentionally not automated. When a TLS fixture or Gordon edge is available, keep the CA private key in the VM, export only its certificate, and use `curl --cacert ca.crt --resolve ...`. Do not use `-k` or install a development CA system-wide. A browser can trust it only in its dedicated test profile.

## Reboot and cleanup

Fixtures are plain containers, not automatically started services. After a VM reboot:

```sh
./dev/v3/sandbox gordon podman start app-web-postgres app-web-valkey app-web-test app-game-test
```

PostgreSQL counters and game event files should persist. Use `podman rm -f ...` for test containers; named volumes survive container removal. `destroy --yes` deletes the entire VM disk and all fixture data.

Diagnostics:

```sh
virsh -c qemu:///system list --all
virsh -c qemu:///system console gordon-v3-sandbox
./dev/v3/sandbox exec cloud-init status --long
./dev/v3/sandbox exec systemctl --failed
./dev/v3/sandbox gordon systemctl --user is-system-running
```

A libvirt permission failure is a host setup problem: the tool reports it rather than running sudo or changing polkit. Locks are released by the kernel on exit; no stale lock directory needs manual removal.
