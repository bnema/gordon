# App Manifest

Application workloads are declared in standalone TOML files — one file per app — not in `gordon.toml`. Apply with `gordon apps apply --file <app>.toml`, activate with `gordon apps deploy <app>`.

The file is intended for Git: it must never contain secret values. Service-specific values use `secrets` even when non-confidential; values stay in pass.

## Minimal Example

```toml
name = "blog"

[[service]]
name = "web"
image = "gordon.mydomain.com/blog:1.4.2"

[[service.http]]
host = "blog.mydomain.com"
port = 3000
```

## Full Example

```toml
name = "blog"

[env]                          # optional, app-wide public env for all services
APP_ENV = "production"

[[service]]
name = "web"
image = "gordon.mydomain.com/blog:1.4.2"
command = ["node", "server.js"]  # optional override
stop_grace = "10s"               # optional, default 10s

[service.readiness]              # optional explicit readiness
type = "http"
path = "/healthz"
port = 3000
timeout = "30s"

[[service.http]]                 # 0..n HTTP interfaces
host = "blog.mydomain.com"
port = 3000
tls = "auto"                     # auto | always | never

[service.secrets]                # ENV name -> secret name (values in pass)
DATABASE_URL = "database-url"

[[service.volume]]               # 0..n named volumes only
name = "web-data"
path = "/data"
readonly = false

[[service.database]]             # explicit database declarations
name = "main"
type = "postgres"
schedule = "daily"

[service.backup]                 # backup targets by reference
postgres = ["main"]
volume = ["web-data"]

[[network.shared]]               # optional shared-network memberships
network = "backend"
services = ["web"]
```

## Identity Rules

- App name: DNS label (lowercase alphanumerics and hyphens, max 63), must not contain `--`, reserved: `gordon`, `registry`, `admin`, `localhost`. Case-insensitive uniqueness.
- Service name: `[a-z0-9_.-]`, max 63, unique within the app.
- Removing an app ends its incarnation: the name is freed, volumes and secrets are archived as retained under the old internal UUID, desired/active/intent state is cleared, and the next apply allocates a new UUID. A new app reusing the name never adopts the old secrets or volumes.

## Services

One or more explicitly named image-backed services per app. Exactly one container per service — no replicas field. A service may expose HTTP, TCP, UDP interfaces, or none.

```toml
[[service.tcp]]
entrypoint = "game"     # names installation entrypoints.<name>
port = 25565
publish = "0.0.0.0:25565"

[[service.udp]]
entrypoint = "game"
port = 28015
publish = "0.0.0.0:28015"
```

RCON is ordinary TCP: use TCP interfaces, no special RCON kind.

`publish` must match the entrypoint listener it attaches to exactly: the same host, port, and transport. The traffic manager binds the entrypoint address, so a declaration that differs (for example a loopback bind on a wildcard entrypoint, or a different port) is rejected at apply time instead of being silently widened. `0.0.0.0` and an omitted host are the same wildcard.

Image registry names and digest syntax are validated during manifest apply, resolution, deployment preflight, and immediately before every pull, including boot, restart, and recovery. Docker Hub (`docker.io` and canonical pull host `registry-1.docker.io`), `ghcr.io`, `quay.io`, and Gordon's configured registry are allowed by default. Add other exact registry hostname+port entries with `images.allowed_registries`; this does not configure credentials, and external resolution and pulls use anonymous access. When `images.require_digest` is enabled, every image reference, including Gordon registry images, must use `@sha256:<64 hex chars>`. See [Images](./images.md). This is a hostname allowlist, not DNS/IP validation or runtime egress enforcement.

## Environment and Secrets

- `[env]` is app-wide public env injected into all services. Each key must be disjoint from every `[service.secrets]` key in the app.
- `[service.secrets]` maps ENV var name to service-local secret name. Values are written with `gordon apps secrets set` and stay in pass under `gordon/apps/<uuid>/<service>/<name>`. Secret updates affect the next deploy/restart, not running containers.
- There is no `[service.env]` key — service-specific values must use secrets.

## Volumes and Databases

- Named volumes only; no bind mounts, no service-shared volumes. Replacement reuses volumes; removed services leave volumes retained and visible.
- A volume declared `readonly = true` is mounted read-only in the container; the service cannot modify protected data.
- `[[service.database]]` declares databases explicitly (no image inference). Only PostgreSQL is supported, and each database declares its own backup `schedule` (`hourly`, `daily`, `weekly`, or `monthly`).
- `[service.backup]` lists the declared databases (`postgres`) and volumes (`volume`) that are backup targets. A declared database or volume that is not referenced here is never backed up. Schedules follow the declaration through deploys; stored backups are never deleted when declarations change.

## Readiness

`type = "http"` requires an origin-form `path` beginning with a single `/`. Absolute URLs, authority forms such as `@host:port`, scheme-relative paths, and control characters are rejected at apply time. The probe always dials the declared loopback backend, never follows redirects, ignores environment proxy settings, and is bounded per request and for the whole operation.

## TLS

- `auto` keeps the host eligible for HTTP and HTTPS; plaintext is redirected when an HTTPS endpoint exists and redirects are enabled.
- `always` is enforced: a plaintext request to an `always` host is redirected whenever an HTTPS endpoint exists, and refused with `421 Misdirected Request` when none does. The backend is never reached over plaintext.
- `never` stays plaintext and is never issued an app certificate.

## Networks

Each app gets a private network automatically. `[[network.shared]]` adds services to named shared networks, created/reused only within verified Gordon ownership. Deploy adds AND removes memberships without disconnecting unrelated services.

## Strictness

Unknown fields, duplicate service names, unresolved service/entrypoint/backup refs, secret/env collisions, unsafe identities, and forbidden mounts are hard errors at apply time. Canonical host conflicts (including system domains and external routes) fail before persistence.

## Related

- [Apps CLI](../cli/apps.md)
- [Secrets](./secrets.md)
- [Volumes](./volumes.md)
- [External Routes](./external-routes.md)
