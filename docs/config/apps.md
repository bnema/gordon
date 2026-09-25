# App Manifest

Application workloads are declared in standalone TOML files — one file per app — not in `gordon.toml`. Apply with `gordon apps apply --file <app>.toml`, activate with `gordon apps deploy <app>`.

The file is intended for Git: it must never contain secret values. Service-specific values use `secrets` even when non-confidential; values stay in pass.

## Minimal Example

```toml
name = "blog"

[services.web]
image = "gordon.mydomain.com/blog:1.4.2"

[[services.web.http]]
host = "blog.mydomain.com"
port = 3000
```

Each `[services.<name>]` table declares one service, and the table key is the service name. A name may contain dots, so quote the key when it does: `[services."web.api"]`, `[[services."web.api".http]]`.

## Full Example

```toml
name = "blog"

[env]                          # optional, app-wide public env for all services
APP_ENV = "production"

[services.web]                    # one table per service; the key is the name
image = "gordon.mydomain.com/blog:1.4.2"
command = ["node", "server.js"]  # optional override
stop_grace = "30s"               # optional, default 30s
devices = ["transcode-gpu"]      # 0..n logical names from [app_devices.<name>]

[services.web.readiness]         # optional explicit readiness
type = "http"
path = "/healthz"
port = 3000
timeout = "30s"

[[services.web.http]]            # 0..n HTTP interfaces
host = "blog.mydomain.com"
port = 3000
tls = "auto"                     # auto | always | never

[[services.web.http]]            # optional private interface
visibility = "internal"          # public (default) | internal
port = 8080                      # required; no host or tls

[services.web.secrets]           # ENV name -> secret name (values in pass)
DATABASE_URL = "database-url"

[[services.web.volume]]          # 0..n named volumes only
name = "web-data"
path = "/data"
readonly = false

[[services.web.bind]]            # 0..n; name references [app_mounts.<name>]
name = "app-logs"                # policy name, never a host path
path = "/var/log/app"            # absolute container destination
readonly = true

[[services.web.database]]        # explicit database declarations
name = "main"
type = "postgres"
schedule = "daily"

[services.web.backup]            # backup targets by reference
postgres = ["main"]
volume = ["web-data"]

[[network.shared]]               # optional shared-network memberships
network = "backend"
services = ["web"]
```

## Identity Rules

- App name: DNS label (lowercase alphanumerics and hyphens, max 63), must not contain `--`, reserved: `gordon`, `registry`, `admin`, `localhost`. Case-insensitive uniqueness.
- Service name: the `[services.<name>]` table key — `[a-z0-9_.-]`, max 63, quoted when it contains a dot. TOML keys are unique, so one table exists per name; names that share a runtime identifier after normalization (`web.api` and `web-api`) are rejected.
- Bind name: `[a-z0-9_.-]`, max 63, must not contain `--`, unique within its service.
- Device name: `[a-z0-9_.-]`, max 63, must not contain `--`, unique within its service.
- Removing an app ends its incarnation: the name is freed, volumes and secrets are archived as retained under the old internal UUID, desired/active/intent state is cleared, and the next apply allocates a new UUID. A new app reusing the name never adopts the old secrets or volumes.

## Services

One or more explicitly named image-backed services per app, each in its own `[services.<name>]` table. Exactly one container per service — no replicas field. A service may expose HTTP, TCP, UDP interfaces, or none.

```toml
[[services.web.tcp]]
entrypoint = "game"     # names installation entrypoints.<name>
port = 25565
publish = "0.0.0.0:25565"

[[services.web.udp]]
entrypoint = "game"
port = 28015
publish = "0.0.0.0:28015"
```

RCON is ordinary TCP: use TCP interfaces, no special RCON kind.

`publish` must match the entrypoint listener it attaches to exactly: the same host, port, and transport. The traffic manager binds the entrypoint address, so a declaration that differs (for example a loopback bind on a wildcard entrypoint, or a different port) is rejected at apply time instead of being silently widened. `0.0.0.0` and an omitted host are the same wildcard.

Image registry names and digest syntax are validated during manifest apply, resolution, deployment preflight, and immediately before every pull, including boot, restart, and recovery. Docker Hub (`docker.io` and canonical pull host `registry-1.docker.io`), `ghcr.io`, `quay.io`, and Gordon's configured registry are allowed by default. Add other exact registry hostname+port entries with `images.allowed_registries`; this does not configure credentials, and external resolution and pulls use anonymous access. When `images.require_digest` is enabled, every image reference, including Gordon registry images, must use `@sha256:<64 hex chars>`. See [Images](./images.md). This is a hostname allowlist, not DNS/IP validation or runtime egress enforcement.

## Environment and Secrets

- `[env]` is app-wide public env injected into all services. Each key must be disjoint from every `[services.<name>.secrets]` key in the app.
- `[services.<name>.secrets]` maps ENV var name to service-local secret name. Values are written with `gordon apps secrets set` and stay in pass under `gordon/apps/<uuid>/<service>/<name>`. Running containers keep the values they were created with; `gordon apps deploy` applies new values. `restart` does not.
- There is no `[services.<name>.env]` key — service-specific values must use secrets.

## Volumes and Databases

- Named volumes only, and no service-shared volumes. Replacement reuses volumes; removed services leave volumes retained and visible.
- Manifests never carry host paths. A `[[services.<name>.bind]]` references an `[app_mounts.<name>]` policy the operator declares in `gordon.toml`; a bind whose name has no matching policy is rejected at apply time. See [Volumes](./volumes.md) and [Security Hardening](./security-hardening.md).
- Named volumes are Gordon-owned app data: created, labeled, retained, backed up, and pruned by Gordon. Administrative binds are operator-owned host locations: Gordon mounts them and never creates, deletes, owns, backs up, or prunes them.
- `[[services.<name>.bind]]` requires an absolute, normalized container `path` and an optional `readonly`. The policy's `read_only` and the bind's `readonly` force read-only together: either side wins and a manifest can never weaken its policy. Reserved destinations (`/`, `/proc`, `/sys`, `/dev`, `/boot` and their children) and paths colliding with a declared volume or another bind are rejected at apply time.
- `devices` lists logical device names granted by `[app_devices.<name>]` policy the operator declares in `gordon.toml`; a device whose name has no matching policy, or whose policy does not allow the app+service pair, is rejected at apply time. Gordon resolves each name to explicit CDI IDs at activation time and encodes them as one native CDI `DeviceRequest`. Revisions persist the logical names, never the host resolution. A device add/remove shows as `service/<name>/devices` in diffs; reorder-only input is a no-op. Changing a mapping never recreates a running container: the next deploy serves the new resolution. Revoking a grant fails subsequent deploys closed while the running service is untouched. Device-bearing creates require Podman 5.4+ or Docker 28.3+ with native CDI configured; older or unrecognized engines return a structured `runtime-unsupported` error and never run without devices. Gordon installs no drivers, manages no quotas, and injects no `NVIDIA_*` environment: images carry their own runtime expectations.
- A service with any bind runs as a single writer, like a volume-backed service: replacements never serve two generations at once.
- A volume declared `readonly = true` is mounted read-only in the container; the service cannot modify protected data.
- `[[services.<name>.database]]` declares databases explicitly (no image inference). Only PostgreSQL is supported, and each database declares its own backup `schedule` (`hourly`, `daily`, `weekly`, or `monthly`).
- `[services.<name>.backup]` lists the declared databases (`postgres`) and volumes (`volume`) that are backup targets. A declared database or volume that is not referenced here is never backed up. Schedules follow the declaration through deploys; stored backups are never deleted when declarations change.

## HTTP Interfaces

`[[services.<name>.http]]` declares 0..n HTTP interfaces per service. `visibility` is optional and defaults to `public` when the key is absent or empty.

```toml
[[services.web.http]]            # public: proxied by host
host = "blog.mydomain.com"
port = 3000
tls = "auto"                     # auto | always | never

[[services.web.http]]            # internal: not published (app private network only)
visibility = "internal"
port = 8080
```

- `public`: `host` is required and must be a valid public hostname, and `tls` is `auto` (default when absent), `always`, or `never`. A public interface gets a proxy route, a global host reservation, a certificate target when TLS applies, and a `127.0.0.1` loopback backend publication.
- `internal`: creates no proxy route, no host reservation, no certificate target, and no host port publication, so it is never reachable through the host or proxy plane. `port` is required, `host` must be absent, and `tls` must be absent — any declared TLS value is rejected. It is still a declared TCP-capable container port for readiness metadata. Reachability follows network membership, not visibility: any container attached to a network this service joins can reach the port — sibling services on the app's own private network, and peers on any `[[network.shared]]` network the service is enrolled in.

There is no `.internal` pseudo-domain: internal interfaces carry no hostname at all.

A container port declared by both an internal HTTP interface and an externally backed interface (public HTTP or TCP) is rejected at apply time, because publication is socket-level. Duplicate internal HTTP ports within a service are rejected the same way.

## Readiness

`type = "http"` requires an origin-form `path` beginning with a single `/`. Absolute URLs, authority forms such as `@host:port`, scheme-relative paths, and control characters are rejected at apply time. The probe always dials the declared loopback backend, never follows redirects, ignores environment proxy settings, and is bounded per request and for the whole operation. A public or otherwise published interface keeps this loopback probe. An internal HTTP port is instead probed over the app private network by one bounded, short-lived helper, using HTTP or TCP according to the interface's `[services.<name>.readiness]` type; the internal port is never temporarily published to the host to probe it.

## TLS

TLS applies to public interfaces only; an internal interface never declares `tls`.

- `auto` keeps the host eligible for HTTP and HTTPS; plaintext is redirected when an HTTPS endpoint exists and redirects are enabled.
- `always` is enforced: a plaintext request to an `always` host is redirected whenever an HTTPS endpoint exists, and refused with `421 Misdirected Request` when none does. The backend is never reached over plaintext.
- `never` stays plaintext and is never issued an app certificate.

## Networks

Each app gets a private network automatically. `[[network.shared]]` adds services to named shared networks, created/reused only within verified Gordon ownership. Deploy adds AND removes memberships without disconnecting unrelated services.

Services of the same app communicate over that private network and resolve each other by service alias. Different apps are isolated by default; cross-app traffic requires both services to declare the same `[[network.shared]]` membership. See [Network Isolation](./network-isolation.md).

## Telemetry

When [telemetry log export](./telemetry.md#logs) is enabled, the stdout and stderr of every service are exported by default under `service.name = "<app>.<service>"`. `logs = false` opts out; a service setting overrides the app setting:

```toml
[telemetry]
logs = false         # no service of this app exports...

[services.web.telemetry]
logs = true          # ...except web
```

Changing either setting is a service change: it applies on the next deploy.

## Strictness

Unknown fields, duplicate service names, unresolved service/entrypoint/backup refs, secret/env collisions, unsafe identities, and forbidden mounts are hard errors at apply time. Canonical host conflicts (including system domains and external routes) fail before persistence.

The keyed `[services.<name>]` form is the only accepted app schema: manifests written for an earlier v3 alpha using `[[service]]` with a `name` key and `[service.*]` tables are rejected with a keyed-schema diagnostic, not converted. See [Migrating a v3 alpha app manifest](../upgrading.md#migrating-a-v3-alpha-app-manifest).

## Related

- [Apps CLI](../cli/apps.md)
- [Secrets](./secrets.md)
- [Volumes](./volumes.md)
- [External Routes](./external-routes.md)
