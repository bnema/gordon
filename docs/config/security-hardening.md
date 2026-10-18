# Security Hardening Controls

This page summarizes Gordon's security-related configuration knobs and the defaults operators should know before exposing an instance.

## Registry upload quotas

Registry blob uploads are bounded by two limits under `[server]`:

```toml
[server]
max_blob_chunk_size = "95MB"
max_blob_size = "1GB"
```

- `max_blob_chunk_size` limits a single upload chunk.
- `max_blob_size` limits the cumulative blob/layer upload size.
- Exceeding either limit returns an OCI-compatible size error and the failed upload is cleaned up.

## Admin logs permission

Container and deploy logs can include environment-derived output. Gordon gates log access behind a dedicated scope:

```bash
gordon auth token generate --subject ops --scopes "admin:logs:read" --expiry 30d
```

- `/admin/logs`, app failure diagnostics, and deploy failure logs require `admin:logs:read`.
- `admin:status:read` does not grant log access.
- Operation errors returned to `admin:apps:read` callers are stable and log-free. Application diagnostics are a separate field, redacted of the affected service's resolved secret values before storage, and returned only to callers holding `admin:logs:read`. If a declared secret cannot be resolved for redaction, Gordon drops the diagnostics instead of storing unredacted output. Mutation responses never include application output.
- Process and container log sources remain operator-owned data. Gordon redacts common credential patterns when serving logs through its API, but this is defense in depth rather than proof that arbitrary application output is secret-free. Restrict filesystem, journal, runtime, backup, and `admin:logs:read` access accordingly.

## Volume pruning scope

Volume pruning removes a volume only when Gordon has a durable `released` ownership record, the runtime app/incarnation/service labels agree with that record, and no container mounts it. A `gordon.managed=true` label by itself is not deletion authority; retained, unknown, contradictory, and unrelated volumes survive.

Use `gordon volumes prune --dry-run` before deletion. Do not substitute `docker volume prune` or an equivalent runtime command: runtime-native pruning bypasses Gordon's ownership and retention checks.

Use dedicated admin scopes:

- `admin:volumes:read` for listing volumes.
- `admin:volumes:write` for prune operations.

## Administrative app bind mounts

App manifests cannot name host paths. A host bind exists only when the operator declares a named policy in `gordon.toml`:

```toml
[app_mounts.app-logs]
source = "/srv/gordon/host-logs"
read_only = true
allowed_apps = ["metrics-agent"]    # exact, non-empty
allowed_services = ["web"]          # exact, non-empty
root = "/srv/gordon"                # optional boundary
```

The manifest references only the policy name: `[[services.<name>.bind]] name = "app-logs"` with an absolute container `path`.

- `allowed_apps` and `allowed_services` are exact, non-empty allowlists, so least privilege is enforced by construction. There is no wildcard, prefix, or empty-means-all form.
- `source` is resolved through symlinks and must be a regular file or directory under `root`. When `root` is omitted, the source's parent directory is the boundary. Devices, sockets, FIFOs, and escaping symlinks are refused.
- Read-only precedence: `read_only` on the policy or `readonly` on the manifest bind forces the mount read-only; a manifest never weakens its policy.
- Destinations must be absolute, normalized, and outside reserved container paths (`/`, `/proc`, `/sys`, `/dev`, `/boot`, and their children).
- The policy is re-resolved immediately before every container create, restart, and recovery. Removing a policy or an allowlist entry blocks future deploys; running containers keep their current mounts until redeployed.
- `gordon serve` reload republishes validated policies atomically. An edit that fails validation is rejected and the previous policies stay live.
- Gordon never creates, deletes, chowns, backs up, or prunes the host path; ownership, permissions, and backup remain the operator's responsibility.

## Administrative app devices (CDI)

App manifests cannot name host devices. A device grant exists only when the operator declares a named policy in `gordon.toml`:

```toml
[app_devices.transcode-gpu]
cdi = ["example.com/gpu=GPU-device-uuid"]
allowed_apps = ["video"]          # exact, non-empty
allowed_services = ["transcoder"]  # exact, non-empty
```

The manifest references only the logical name: `devices = ["transcode-gpu"]`.

- `cdi` holds explicit CDI device IDs. Raw `/dev` paths, unqualified names, and aggregate `=all` selectors are rejected.
- `allowed_apps` and `allowed_services` are exact, non-empty allowlists, so least privilege is enforced by construction. There is no wildcard, prefix, or empty-means-all form.
- Gordon resolves logical names to CDI IDs at activation time and encodes them as one native CDI `DeviceRequest`. Revisions persist the logical names, never the host resolution.
- The grant is re-resolved immediately before every preflight, container create, restart, and recovery. Revoking a grant blocks future deploys while the running service is untouched; changing a mapping never recreates a running container.
- Device-bearing creates require Podman 5.4+ or Docker 28.3+ with native CDI configured. Older or unrecognized engines fail with a structured `runtime-unsupported` error and never run without devices.
- Gordon installs no drivers, manages no quotas, and injects no device environment: images carry their own runtime expectations.

### Plain device nodes under rootless Podman

CDI is not GPU-specific. Any device node can be exposed through a user-level CDI spec, so `/dev/kvm` and `/dev/net/tun` reach a rootless Podman service without raw `/dev` paths in manifests and without any privilege:

```yaml
# ~/.config/cdi/vm.yaml  (rootless user, mode 0644)
cdiVersion: "0.6.0"
kind: example.com/vm
devices:
  - name: kvm
    containerEdits:
      deviceNodes:
        - path: /dev/kvm
  - name: net-tun
    containerEdits:
      deviceNodes:
        - path: /dev/net/tun
```

```toml
# ~/.config/containers/containers.conf (the rootless user that runs the Podman API service)
[engine]
# Setting cdi_spec_dirs replaces the defaults: keep /etc/cdi and /var/run/cdi if you use them.
cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi", "/home/<user>/.config/cdi"]
```

```toml
# gordon.toml
[app_devices.my-vm-device]
cdi = ["example.com/vm=kvm", "example.com/vm=net-tun"]
allowed_apps = ["myapp"]
allowed_services = ["worker"]
```

- No Gordon change is needed (verified against the Podman 5.7 sources; confirm with a live create on your host). Gordon sends the IDs as a native `cdi` `DeviceRequest`; the Podman compat API forwards them to the same CDI injection as `podman run --device <id>`, which reads `cdi_spec_dirs` from the containers.conf of the user running the API service. Restart that service (`systemctl --user restart podman.socket podman.service`) after editing `containers.conf`; edits to the spec files themselves are picked up on the next container create.
- Under rootless Podman the OCI runtime (crun or runc) cannot `mknod` in a user namespace and falls back to bind-mounting the host device node into the container. The node must therefore be accessible to the rootless user on the host: `/dev/kvm` and `/dev/net/tun` are usually `0666` or group-owned (`kvm`). Supplementary groups do not carry into the user namespace, so prefer a `0666`/ACL-based permission over group membership.
- CDI device names Gordon accepts match `[A-Za-z0-9][A-Za-z0-9_.-]*` (so `kvm` and `net-tun` are valid; names containing `:` are not).
- A missing spec dir entry, an unreadable spec, or an unknown device name surfaces as a redacted create failure (`runtime error redacted`); check the Podman service journal for the CDI error.

## Administrative app capabilities

App manifests cannot add Linux capabilities. A grant exists only when the operator declares a named policy in `gordon.toml`:

```toml
[app_capabilities.vm-net]
add = ["NET_ADMIN"]
allowed_apps = ["myapp"]    # exact, non-empty
allowed_services = ["worker"]     # exact, non-empty
```

The manifest references only the logical name: `capabilities = ["vm-net"]`.

- `add` holds explicit Linux capability names, written without the `CAP_` prefix. Only this allowed set is accepted: `AUDIT_WRITE`, `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `FSETID`, `IPC_LOCK`, `KILL`, `MKNOD`, `NET_ADMIN`, `NET_BIND_SERVICE`, `NET_RAW`, `SETFCAP`, `SETGID`, `SETPCAP`, `SETUID`, `SYS_CHROOT`, `SYS_NICE`, `SYS_RESOURCE`. Everything else is rejected when the config loads, including `ALL`, `SYS_ADMIN`, `SYS_PTRACE`, `SYS_MODULE`, `SYS_RAWIO`, `SYS_BOOT`, `SYS_TIME`, `DAC_READ_SEARCH`, `BPF`, `PERFMON`, `MAC_ADMIN`, `MAC_OVERRIDE`, `AUDIT_CONTROL`, and `SYSLOG`. These can reach the host or defeat container confinement; a workload that needs one is not an unprivileged service.
- `allowed_apps` and `allowed_services` are exact, non-empty allowlists. There is no wildcard, prefix, or empty-means-all form. Unknown keys inside `[app_capabilities.<name>]` fail closed.
- Granted capabilities are added on top of Gordon's default set (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETUID`, `SETGID`, `NET_BIND_SERVICE`). Gordon's `cap_drop = ALL` and `no-new-privileges` stay in force, and containers are never privileged. The runtime guard re-checks the resolved list before every create.
- Capabilities apply inside the container's own user and network namespaces. Under rootless Podman, `NET_ADMIN` lets the service create TAP devices and nftables rules in its own network namespace, not on the host. Because `no-new-privileges` stays on, a process only holds the added capabilities when it runs as container root (uid 0 in the user namespace, which is an unprivileged host user under rootless Podman); a non-root container user cannot gain them through file capabilities.
- Gordon resolves logical names at activation time. Revisions persist the logical names, never the resolution. The grant is re-resolved before every preflight, container create, restart, and recovery. Revoking a grant blocks future deploys and restarts while the running service is untouched.
- Adding or removing a policy name shows as `service/<name>/capabilities` in diffs and recreates the container on the next deploy; reorder-only input is a no-op. Changing what a policy grants does not recreate a running container: the next deploy serves the new resolution.

## External image registries

Docker Hub, `ghcr.io`, `quay.io`, and Gordon's configured registry are always allowed. Add every other registry hostname and non-default port explicitly:

```toml
[images]
allowed_registries = ["registry.internal:5000"]
require_digest = true
```

- `docker.io` and Docker Hub's canonical pull host `registry-1.docker.io` are equivalent.
- Other registries are accepted only when their exact hostname+port is configured. Allowlisting does not configure credentials; external resolution and pulls are anonymous unless the runtime already has suitable access.
- `require_digest = true` requires every image, including Gordon registry images, to use `@sha256:<64 hex chars>`.
- The allowlist restricts hostnames, not resolved IPs. It cannot prove a DNS hostname is non-private and does not replace firewall or runtime egress controls.

## Smart TCP Raw Fallback

Raw TCP fallback on a `smart_tcp` edge entrypoint is disabled by default. Unknown non-HTTP/non-TLS bytes reach a backend only when the entrypoint explicitly sets `raw_fallback` to a TCP router and the fallback source policy allows the peer.

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
raw_fallback = "ssh-fallback"
raw_fallback_trusted_cidrs = ["100.64.0.0/10"]
```

Use `raw_fallback_trusted_cidrs` for private raw fallback. Public raw fallback requires an explicit acknowledgement:

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
raw_fallback = "public-raw"
allow_public_raw_fallback = true
```

Security behavior:

- PROXY protocol v1 and v2 prefixes are rejected; Gordon does not trust or parse PROXY headers on smart TCP entrypoints.
- `trusted_cidrs` and `raw_fallback_trusted_cidrs` use the peer socket IP, not `X-Forwarded-For`.
- HTTP-looking or TLS-looking malformed traffic is rejected and never bypasses to raw fallback.
- Entry-point-wide `trusted_cidrs` applies before sniffing to all protocols; raw fallback has its own narrower policy for private fallback exposure.

## Docker network isolation

Per-app Docker networks are enabled by default. To block direct external egress from isolated networks, opt into Docker internal networks:

```toml
[network_isolation]
enabled = true
internal = true
```

`internal = false` remains the compatibility default because some applications need direct egress during startup.

Every app container joins an incarnation-owned private network derived from the app's internal UUID; shared memberships come only from explicit `[[network.shared]]` declarations, and memory/CPU/PID limits apply on every create and recovery path.

## Readiness helper containers

Probing an internal HTTP port uses one bounded helper container per readiness attempt, removed immediately after. Gordon force-removes the helper under an independent cleanup context, including on failure and timeout. There is no operator configuration for the helper; its image and limits are fixed.

- The helper image is digest-pinned and multi-arch: `alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc`. The image must be pre-provisioned and available offline on the target host, because the probe never pulls.
- The helper attaches only to the target app's private network. It never joins a shared network or a host network.
- It never publishes a host port and has no mounts, volumes, secrets, environment, or runtime socket.
- It runs non-root (uid/gid `65534`) with a read-only root filesystem, all capabilities dropped, `no-new-privileges`, and bounded CPU, memory, and PIDs.
- It targets the exact inspected container IP on that network, never a service alias, and revalidates the container's execution start before trusting the result; a restarted generation is discarded.
- A target that listens only on its own loopback fails readiness.
- Helpers left behind by an abrupt daemon or host stop are reclaimed by the next probe once they are older than ten minutes; a live session is never touched.

## Registry exposure

- With `auth.enabled = true`, registry requests are authenticated and repository-scoped; the public proxy forwards registry domains to the internal registry.
- With `auth.enabled = false`, the registry is local-only: the public proxy refuses registry-domain requests, and the registry handler accepts only direct loopback connections carrying the instance credentials.
- Registry requests are parsed once into a validated operation used by both authorization and dispatch, so the repository a token is checked against is exactly the repository served.
- Blob and upload access is repository-scoped: an upload UUID is usable only by the repository that started it, and a blob is served only to a repository that completed an upload of that digest.

## Container runtime profile

```toml
[containers]
security_profile = "compat" # or "strict"
```

- `compat` preserves broad image compatibility while retaining `no-new-privileges` and default capability restrictions.
- `strict` enables a read-only root filesystem, drops all capabilities, and only adds `NET_BIND_SERVICE`.

Use `strict` for images designed to write only to mounted volumes and run without extra Linux capabilities.

## Runtime guard

Every app container config passes a final guard right before the runtime creates the container. Bind checks also run at deploy preflight and before every restart or recovery of an existing container. A refused config creates or starts no container: the operation fails and its preflight or service step error contains `runtime guard violation`. The guard refuses:

- network mode `host`, `container:<id>`, or `ns:<path>`;
- a volume name containing `/` or `\`, which the runtime would read as a host path;
- a bind whose host source is equal to, under, or a parent of a protected path, compared both as written and after resolving symlinks.

Protected paths:

- the data directory and the loaded config file;
- files and directories set in the config: `server.tls_key_file` and the backup `storage_dir` values;
- runtime sockets: `/var/run/docker.sock`, `/run/docker.sock`, `/run/podman/podman.sock`, `$XDG_RUNTIME_DIR/docker.sock`, `$XDG_RUNTIME_DIR/podman/podman.sock`, the `unix://` path of `DOCKER_HOST`, and the `server.runtime` socket;
- rootless runtime storage and config: `~/.local/share/containers`, `~/.local/share/docker`, `${XDG_CONFIG_HOME:-~/.config}/containers`, and `~/.docker`;
- the systemd user units: `${XDG_CONFIG_HOME:-~/.config}/systemd/user`;
- Gordon runtime credentials: `$XDG_RUNTIME_DIR/gordon`, `~/.gordon/run`, and `$GORDON_CLOUDFLARE_API_TOKEN_FILE`;
- key stores: `$GNUPGHOME`, `~/.gnupg`, `$PASSWORD_STORE_DIR`, `~/.password-store`, `$SOPS_AGE_KEY_FILE`, and `${XDG_CONFIG_HOME:-~/.config}/sops/age`.

A protected path that does not exist yet is also protected in its symlink-resolved form, based on its closest existing parent. Backup directories are protected: a backup app cannot bind them, even read-only.

Gordon's enabled file logs (the Gordon log file's directory, the access log's directory, and `logging.container_logs.dir`) can be bound read-only so a log shipper can read them. Under the data directory this applies only at or under `<data_dir>/logs`; outside it, only when the directory neither is, contains, nor lies under another protected path. A writable bind on them is refused. When a log directory does not qualify, for example a log file directly in `$HOME`, the log file itself stays protected and no read-only exception applies.

The only other exception is Gordon's own read-only secret file mount: `<data_dir>/secrets-run/<app>/<container>/<KEY>` at `/run/secrets/<KEY>`. A refused `[app_mounts]` policy fails before the running generation is replaced or restarted. Point the policy `source` outside every protected path.

## Process memory protection

On Linux, `gordon serve` marks itself non-dumpable (`PR_SET_DUMPABLE=0`) as its first step, before loading its config file or any backend secret. Other processes of the same user can no longer read its `/proc/<pid>/environ`, `mem`, or file descriptors, attach with ptrace, or get a core dump. Startup fails if the flag cannot be set.

Limits:

- Secrets passed as environment variables, such as `GORDON_AUTH_TOKEN_SECRET`, are readable for the short time between process start and that first step. Prefer a secrets backend.
- Helper processes (`pass`, `gpg`, `sops`, `journalctl`) are dumpable again after `exec`; secret tools hold the values they decrypt while they run. Gordon starts them without its `GORDON_*` variables.
- Root, or a process of the same user that can replace the Gordon binary or its systemd unit, is not stopped.

## Hardened systemd user unit

Add these directives to the `[Service]` section of the [user unit](../installation.md#user-service-recommended-for-rootless):

```ini
[Service]
NoNewPrivileges=yes
UMask=0077
LockPersonality=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
SystemCallArchitectures=native
```

Apply and check:

```bash
systemctl --user daemon-reload
systemctl --user restart gordon
systemctl --user status gordon
systemd-analyze --user security gordon.service
```

## Credential scoping

- **Cloudflare DNS-01 token**: grant only `Zone:Read` and `DNS:Edit`, limited to the zones Gordon serves. A leaked token can issue certificates for, and take over, every hostname in those zones. See [Server](./server.md).
- **Registry push tokens**: issue one token per repository and pipeline instead of a global one:

  ```bash
  gordon auth token generate --subject ci-blog --scopes push --repo blog
  ```

## Protection boundary

Gordon v3 protects against:

- file-delivered secret values showing up in container env, `docker inspect`, or `/proc/1/environ`;
- app configs that mount the runtime socket, Gordon data, config, credentials, or key stores, or that join the host or another container's namespaces;
- same-user processes reading the Gordon process's memory or environment through `/proc`, within the limits above.

Gordon v3 does not protect against:

- a compromised Gordon process, proxy, or registry: they run as the same user that owns the runtime socket and `pass`;
- a malicious image deployed for an app: it receives that app's secrets;
- root on the host.

## Related

- [Auth](./auth.md)
- [Secrets](./secrets.md)
- [Images](./images.md)
- [Network Isolation](./network-isolation.md)
- [Volumes](./volumes.md)
- [Reference](./reference.md)
