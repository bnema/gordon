# 01 — App Manifest Contract (D6)

Status: draft for maintainer acceptance. Freezes the app TOML schema.
Parser: `pelletier/go-toml/v2` (`v2.4.3`, already in `go.mod`) with
`Decoder.DisallowUnknownFields()` — verified present in the vendored
source. No Viper defaults for app schema. No `env_file` support.

## 1. File identity

- One file defines exactly one app: top-level `name` (required).
- File name SHOULD match `<app>.toml` but the `name` field is
  authoritative; a mismatch is a warning, not an error.
- The file is intended for Git: it MUST NOT contain secret values.
  Documentation MUST carry the warning (see §8).

## 2. Top-level sections

```toml
name = "APP"                    # required, globally unique (§3)

[env]                           # optional, app-wide public env (§4)
KEY = "value"

[[service]]                     # 1..n, at least one required (§5)
...

[[network.shared]]              # optional, 0..n shared memberships (§6)
...

[backup.postgres]               # optional, 0..n named declarations (§7)
[backup.volume]
...
```

Unknown top-level keys, unknown keys inside any table, and duplicate
`[[service]]` names are hard errors. There is no compact
single-service shorthand: the expanded `[[service]]` array form is the
only form. (Rationale: the plan asked to freeze compact/expanded; one
form removes duplicate-item and merge-order ambiguity.)

## 3. Identity rules

- App name: `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` (DNS label, ≤63),
  additionally MUST NOT contain `--` (reserved as the logical-identity
  separator, see runtime identity below).
  Reserved: `gordon`, `registry`, `admin`, `localhost`. Case-insensitive
  uniqueness: `Blog` and `blog` collide.
- Service name: `^[a-z0-9]([a-z0-9_.-]{0,61}[a-z0-9])?$`, unique within
  the app (normalization `.`/`_`/`/` → `-` per existing
  `serviceRuntimeIdentifier`; two names normalizing identically are
  rejected at apply validation).
- Runtime container identity (REVIEW FIX senior round 1, HIGH-8):
  the naive `gordon-<app>-<service>` collides across apps
  (`(a-b,c)` vs `(a,b-c)` → `gordon-a-b-c`) and cannot name old +
  replacement simultaneously during web-style overlap. Frozen:
  STABLE logical identity `gordon-<app>--<service>` (DOUBLE dash
  disambiguates: app names never contain `--` — validation rejects
  them — so `gordon-a-b--c` parses unambiguously) carried as labels
  `gordon.app` + `gordon.app.service`, PLUS an instance suffix for
  the runtime NAME: `gordon-<app>--<service>--<short-op>` where
  `<short-op>` is the first 8 chars of the creating op ULID
  (initial deploy: `init`). retiring containers keep their instance
  names until removal; queries by logical identity use LABELS, never
  name parsing. Generated volume runtime names follow the same
  scheme: `gordon-<app>--<service>--vol-<name>`.
- Pass secret identity for service `S` of app `A`: the manifest maps
  ENV var name → service-LOCAL secret name
  (`DB_PASSWORD = "db-password"`); the stored pass path is
  `gordon/apps/<app>/<service>/<secret-name>` where `<app>` and
  `<service>` are the normalized identifiers above and
  `<secret-name>` matches the SERVICE-NAME charset (§3: lowercase
  alphanumerics + `_. -`, NOT the ENV-key charset — hyphens allowed,
  e.g. `db-password`). CANONICAL MODEL (review fix): ENV key and
  secret name are DISTINCT namespaces; the ENV key is never a path
  component. This is a NEW path prefix served by a NEW app-path
  adapter boundary (not the existing `PassStore` domain paths which
  build `gordon/env/…`); existing `gordon/env/<domain>/…` entries are
  untouched and never adopted.

## 4. `[env]` — app-wide public environment

- Flat `KEY = "value"` string map, injected into every service.
- Collision with any service secret name (§5.4) is a hard error.
- Values MUST be non-empty strings ≤ 64 KiB; no multi-line values
  (TOML basic strings only, no `"""` literals).
- Any value matching the existing secret-reference pattern
  (`${pass:…}` / `${sops:…}`, cf. `domain.ContainsSecretReference`)
  is rejected: secrets MUST be declared as secret names, never inlined.

## 5. `[[service]]`

```toml
[[service]]
name = "web"                                  # required (§3)
image = "registry.example.com/blog/web:1.4.2" # required (§5.1)
command = ["node", "server.js"]               # optional override
replicas = 1                                  # optional, default 1; max frozen at 1 for v2.50 (single instance)
stop_grace = "10s"                           # optional, default 10s, max 5m (see 03-deployment.md §6)

[service.env]            # NOT ALLOWED — reserved key, hard error if present.
                         # Service-specific values MUST use secrets (§5.4),
                         # even when non-confidential (plan product contract).

[service.readiness]      # optional; defaults to type "none" (§5.2)
type = "http"            # none | tcp | http | log (http is NEW in v2.50;
                         # V2 StandaloneService supports none|tcp|log only,
                         # log requiring path+contains, 1 MiB log cap)
path = "/healthz"        # http only; log keeps V2 path+contains pair
contains = "ready"       # log only: substring match in recent logs
port = 8080                # NEW: required when >1 TCP-capable interface;
                         # selects the container port probed by tcp/http
timeout = "30s"          # parsed by time.ParseDuration, 1s..10m

[[service.http]]         # 0..n HTTP interfaces (§5.3)
host = "blog.example.com"
port = 8080              # container port
tls = "auto"             # auto | always | never

[[service.tcp]]          # 0..n TCP interfaces (§5.3)
entrypoint = "tcp"       # REQUIRED: names installation entrypoints.<name>
port = 25565
publish = "0.0.0.0:25565"

[[service.udp]]          # 0..n UDP interfaces (§5.3)
entrypoint = "udp"       # REQUIRED (same resolution as tcp)
port = 28015
publish = "0.0.0.0:28015"

[[service.rcon]]         # 0..n RCON interfaces (§5.3; TCP transport + policy)
entrypoint = "tcp"       # REQUIRED (policy subset of entrypoint CIDRs)
port = 28016
publish = "127.0.0.1:28016"
# public = true  +  trusted_cidrs REQUIRED to expose publicly (default private)

[service.secrets]        # optional name map (§5.4)
DB_PASSWORD = "db-password"   # ENV name -> secret name in pass
API_TOKEN = "api-token"

[[service.volume]]       # 0..n named volumes only (§5.5)
name = "web-data"
path = "/data"
readonly = false

[service.backup]         # optional backup declarations (§7)
postgres = ["main"]
volume = ["web-data"]
```

### 5.1 Image references

- Required form: `[registry/]repository[:tag][@digest]`.
- **Unqualified** references (no registry host, e.g. `blog/web:1.4`)
  resolve ONLY against the selected Gordon installation's registry.
  No Docker Hub fallback, no search.
- **Explicit external** registries (e.g. `docker.io/library/postgres:16`)
  remain subject to the existing installation policy:
  `images.allowed_registries`, `images.require_digest`, SSRF
  restrictions. An app MUST NOT relax installation restrictions.
- Missing tag normalizes to `:latest` explicitly and is recorded as
  such in the revision (no silent default downstream). `latest` is
  valid; deploy re-resolves mutable tags, restart reuses pinned digest
  (see `03-deployment.md`).
- `command` overrides the image entrypoint; no `args`/`entrypoint`
  split — one verbatim argv array.

### 5.2 Readiness

Types: `none` (default), `tcp` (V2: dial published TCP port until
success or timeout — verified `service.go:470-522`), `http` (NEW in
v2.50: `GET http://<backend>:port<path>`, 2xx/3xx), `log` (V2:
`path` + `contains` BOTH required, 1 MiB log cap — verified
`service.go:533-601`, `domain/service.go:236-259`). `timeout` bounds
the whole check, default `30s`, range `1s`–`10m`. UDP services MUST NOT use a readiness type that
claims application readiness from a bind; allowed types for UDP-only
services: `none` or `log`. This is enforced at validation.

### 5.3 Interfaces (app entrypoints)

Terminology: V2 `entrypoints` in `gordon.toml` are host listeners;
app `[[service.http/tcp/udp/rcon]]` entries are service interfaces.
Separate types and names at the owning boundary.

- `http`: `host` required (canonicalized: lowercase, trailing-dot
  stripped, IDNA as-is — no new punycode dependency). `port` is the
  container port. `tls`: `auto` (serve HTTPS when installation TLS
  covers the host, else HTTP), `always` (error if TLS unavailable),
  `never` (plain HTTP even if TLS available).
- `tcp`/`udp`: `port` required (container port 1–65535). `publish`
  is the literal host bind `IP:port` or `port` (binds `0.0.0.0` and
  `::` per dual-stack availability). Hostnames are NOT accepted in
  `publish` — no mutable-hostname reservations.
- `rcon` is a TCP transport with policy: default private. A public
  `rcon` requires BOTH `public = true` AND non-empty `trusted_cidrs`
  (validated CIDRs, cf. existing `parseCIDRAllowlist` semantics).
  Omitting the policy keeps it loopback-only. Until the replacement
  policy below is accepted, any `rcon` that cannot preserve the
  private default is rejected (senior-review mandate).
- A service MAY declare zero interfaces (worker). `replicas` > 1 is
  rejected in v2.50 (single instance per service; scaling is out of
  scope, not silently half-supported).

### 5.4 Secrets

- `[service.secrets]` maps ENV var name (ValidateEnvKey charset) →
  service-LOCAL secret NAME (service-name charset, §3 — hyphens
  allowed). The stored path is
  `gordon/apps/<app>/<service>/<secret-name>`; the ENV key NEVER
  appears in the path (canonical model, §3).
- ENV names match `ValidateEnvKey`; secret names match service-name
  charset (§3). No cross-service references: `web` can only use
  `gordon/apps/APP/web/*`. No `env_file`.
- Secret VALUES live only in `pass`. BOOTSTRAP ORDER (review fix):
  1. `apply` registers NAMES (validation of names/collisions only,
     reads NO values);
  2. `apps secrets set APP --service SVC …` writes VALUES (refused
     for names absent from desired/active — the API check now has
     something to check against);
  3. deploy preflight reads VALUES; missing fails `secret-missing`
  BEFORE any workload mutation.
- Secret updates affect the NEXT deploy/restart, never running env.
  Deleting a secret referenced by desired OR active config is refused.

### 5.5 Volumes

- Named volumes only: `name` (same charset as service names) +
  container `path` (absolute, no `..`, no host-bind syntax).
- No bind mounts. No service-shared volumes: a volume `name` MUST be
  claimed by at most one service in the app. Sharing across apps goes
  through named shared networks + explicit backup/restore, not shared
  storage.
- Replacement reuses volumes. Removed services leave volumes retained
  and visible; no automatic deletion. Gordon NEVER deletes user
  volumes in v2.50 paths (see `03-deployment.md` invariants).

## 6. Shared networks

```toml
[[network.shared]]
network = "blog-cache"     # Gordon-managed shared network name
services = ["web", "api"]  # per-service membership
aliases = ["cache"]        # short DNS names visible on this network
```

- The app's private network is automatic (one per app, named
  `gordon-app-<app>`); it is not declared.
- Shared networks are created/reused ONLY within verified Gordon
  ownership (label `gordon.managed=true` AND name prefix `gordon-`;
  foreign networks are never adopted). Membership is per service;
  deploy adds AND removes memberships without disconnecting unrelated
  services.
- Short DNS names on the private network; app-qualified aliases
  (`<alias>.<app>`) on shared networks to avoid cross-app collision.
- Alias charset: DNS label; collision with another app's alias on the
  same shared network fails apply with a conflict error.

## 7. Backups

Full declaration schema (REVIEW FIX senior round 1, MEDIUM-9 — the
previous draft discussed schedules without a field for them):

```toml
[service.backup]
postgres = ["main"]      # keys into per-service [[service.database]] (below)
volume = ["web-data"]    # volume names declared in §5.5

[[service.database]]      # 0..n explicit DB declarations (NEW; replaces
name = "main"             # V2 attachment-image inference)
type = "postgres"         # ONLY postgres in v2.50 (no new engines)
schedule = "daily"        # hourly|daily|weekly|monthly (BackupSchedule vocab)
```

- The `postgres = [...]` list REFERENCES `[[service.database]]`
  names; unresolvable refs are validation errors. The previous
  `"main"` example now resolves against the declared table.
- Schedule lives ON the database declaration (not floating).
  Retention is INSTALLATION retention (no per-app override —
  06-config-disposition.md §2 is corrected to REMOVE, not move,
  per-workload retention).
- Declaration names the WHAT and the schedule source; destination
  credentials stay in global `gordon.toml` (`backups.*`), never in the
  app file. Named global destinations (`backups.destinations.<name>`)
  are PROPOSED but not frozen here — see Decision required.
- Engine scope is frozen: PostgreSQL logical backups + volume
  archives to S3 only. No new engines.
- Schedule values reuse the existing `BackupSchedule` vocabulary:
  `hourly|daily|weekly|monthly`. Retention reuses installation
  retention; per-app retention overrides are NOT in v2.50.
- Changing/removing declarations updates schedules only on deploy;
  stored backups are never deleted by declaration changes.

## 8. Worked examples

### 8.1 Web + worker/database

```toml
name = "blog"

[env]
APP_ENV = "production"
LOG_LEVEL = "info"

[[service]]
name = "web"
image = "registry.example.com/blog/web:1.4.2"

[service.readiness]
type = "http"
path = "/healthz"
timeout = "30s"

[[service.http]]
host = "blog.example.com"
port = 8080
tls = "auto"

[service.secrets]
DATABASE_URL = "database-url"

[[service.volume]]
name = "web-uploads"
path = "/app/uploads"

[service.backup]
volume = ["web-uploads"]

[[service]]
name = "db"
image = "docker.io/library/postgres:16.4"

[service.secrets]
POSTGRES_PASSWORD = "postgres-password"

[[service.database]]
name = "main"
type = "postgres"
schedule = "daily"

[[service.volume]]
name = "pgdata"
path = "/var/lib/postgresql/data"

[service.backup]
postgres = ["main"]
volume = ["pgdata"]
```

### 8.2 Staging as an ordinary app

```toml
name = "blog-staging"

[env]
APP_ENV = "staging"

[[service]]
name = "web"
image = "registry.example.com/blog/web:1.4.2-rc.1"

[[service.http]]
host = "staging-blog.example.com"
port = 8080
tls = "auto"
```

No pin/preview machinery: staging is a second file, second app.

### 8.3 TCP/UDP/RCON game service

```toml
name = "rust"

[[service]]
name = "server"
image = "registry.example.com/games/rust:2026.09"

[service.readiness]
type = "log"
path = "/data/logs/server.log"
contains = "Server startup complete"
timeout = "5m"

[[service.udp]]
entrypoint = "udp"
port = 28015
publish = "0.0.0.0:28015"

[[service.rcon]]
entrypoint = "tcp"
port = 28016
publish = "127.0.0.1:28016"
# private by default; public exposure needs public = true + trusted_cidrs

[[service.volume]]
name = "rust-data"
path = "/data"
```

### 8.4 Shared database

```toml
name = "shop"

[[service]]
name = "api"
image = "registry.example.com/shop/api:2.0.0"

[[service.http]]
host = "shop.example.com"
port = 8080
tls = "auto"

[service.backup]
postgres = ["main"]

[[network.shared]]
network = "shop-cache"
services = ["api"]
aliases = ["cache"]
```

## Decision required (D6 — maintainer acceptance)

1. Single `[[service]]` form only (no compact shorthand)? Recommending
   YES for the reasons in §2.
2. `replicas` frozen at 1 for v2.50 — accept, or drop the field
   entirely until scaling is designed?
3. Missing-tag → `:latest` normalization recorded in revision — accept?
4. `[service.env]` as reserved-but-rejected (fail-closed guidance)
   vs. silently ignoring — recommending reject.
5. Named global backup destinations (`backups.destinations.<name>`)
   schema — proposal needed before P6; absent that, destinations are
   the single existing global S3/volume configuration.
6. SOPS/`unsafe` installation backends stay for non-app auth paths
   (agreeing on pass for app secrets does NOT delete them) — confirm.
