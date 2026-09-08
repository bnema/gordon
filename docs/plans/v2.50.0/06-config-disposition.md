# 06 — Global Config Disposition (D6, global side)

Status: draft for maintainer acceptance. Field-by-field table for the
existing `gordon.toml`: KEEP globally / MOVE to app manifests / REMOVE
with rejection. Reloadability class per kept field. P7 executes this
table; the cutover slice enforces the rejections.

Source of truth for current keys: `internal/app/run.go` `Config`
struct (`mapstructure` tags, 127 keys — relevant subset below) and
`internal/usecase/config/service.go` `Config` struct (`routes`,
`external_routes`, `network_groups`, `attachments`,
`auto_route_allowed_domains`, preview fields).

## 1. KEEP globally (installation policy — effective behavior preserved)

Reload class: **live** = applies on SIGHUP/reload without restart;
**restart** = reported as restart-required (truthfully, never silently
ignored).

| Key(s) | Reload | Notes |
|---|---|---|
| `server.port`, `tls_port`, `registry_port` | restart | Listeners (bind address now on its own row below) |
| `server.gordon_domain`, `registry_domain`, `legacy_registry_domains` | restart | Identity |
| `server.tls_cert_file`, `tls_key_file`, `force_https_redirect` | restart | TLS material |
| `server.data_dir` | restart | State root (`<dataDir>/apps/`) |
| `server.max_proxy_body_size`, `max_blob_chunk_size`, `max_blob_size`, `max_proxy_response_size`, `max_concurrent_connections` | live | Size/conn bounds; chunk default stays 95 MiB server-side vs 50 MiB push client |
| `server.registry_allowed_ips`, `proxy_allowed_ips` | live | Allowlist semantics UNCHANGED (§5, 04-network.md) |
| `server.registry_listen_address`, `port`, `tls_port`, `registry_port` | restart | REVIEW FIX: the bind address forms the listener (`run.go:2895`) — restart-required like all listeners, NOT live |
| `auth.*` (enabled, type, secrets_backend, username, token_secret, token_expiry, access_token_ttl) | live* | *backend switch needs restart; TTLs live. SOPS/`unsafe` STAY for non-app auth paths |
| `api.rate_limit.*` (incl. trusted_proxies) | live | |
| `tls.acme.*`, `dns.*` | restart | ACME/DNS |
| `entrypoints`, `traffic.*` (tcp/udp/tls routers, timeouts, limits, raw fallback, CIDR policy) | restart | Host traffic plane; app projections merge in, never override |
| `network_services` | restart | Standalone L4 services config (retained infrastructure; app model does not use it) |
| `external_routes` | live | Global reverse-proxy routes STAY here (plan contract — no artificial external services) |
| `images.allowed_registries`, `require_digest`, `prune.*` | live/prune-restart | Prune gated by ownership protection (`04-network.md` §8) |
| `containers.*` (memory/cpu/pids limits, security_profile) | restart | GENERATED container config MUST assert parity pre-cutover (senior mandate: compat AND strict profiles, read-only rootfs, no-new-privs, caps — tested on Docker AND Podman fixtures, not just TOML retention) |
| `volumes.*` (auto_create, prefix, preserve) | live | Naming/ownership policy where still relevant |
| `logging.*`, `telemetry.*` | live | Rotation, access log, OTel |
| `backups.*` (databases, volumes, s3, retention, schedules) | live | Storage INFRASTRUCTURE stays global; app files declare targets/schedules only. Named `backups.destinations.<name>` — PROPOSED, undecided (01-manifest.md D6.5) |
| `env.dir` | REMOVED (rejection pointer, one release) | REVIEW FIX: single disposition — the KEY stays parseable for one release SOLELY to emit `config-retired: env.dir was removed; values move to [env]/pass, no auto-import`. The env-dir IMPORT feature is removed. After one release the key joins strict rejection. |

## 2. MOVE to app manifests (removed from global schema)

| Global key today | App manifest home | Notes |
|---|---|---|
| `services[]` (standalone, incl. readiness/ports/volumes/secrets/env_file) | `[[service]]` | No `env_file`; secrets via pass names; readiness explicit |
| per-service images | `[[service]] image` | Unqualified → installation registry |
| app env intentions via env dir | `[env]` + `[service.secrets]` | Env-dir IMPORT removed; values re-declared by operator |
| `service_routes` (HTTP routes to standalone services, #242 shape) | `[[service.http]]` | #242 public DTO/model removed at cutover (after backup/P5 prerequisites per ordering) |
| backup targets/schedules per workload | `[service.backup]` + `[[service.database]]` | Per-workload RETENTION is REMOVED (installation retention only); destinations/credentials stay global (§1) |
| shared-network membership intentions | `[[network.shared]]` | Ownership-verified create/reuse only |

Default inheritance: NO silent inheritance of competing global/app
values. Where a global default exists (e.g. `containers.*` limits),
it applies as a FLOOR/CEILING the app cannot weaken; the app does not
redeclare it. Explicit per-app overrides of installation policy are
NOT in v2.50 (rejected as unknown fields if attempted).

## 3. REMOVE with startup+reload rejection (fail-closed diagnostic)

Presence of ANY of these before mutation → refuse start/reload with
`config-retired: key '<key>' was removed in v2.50; <hint>`:

```text
routes.* (old routes→image)                 hint: declare [[service.http]] in an app file, then 'apps apply'
attachments.*, network_groups.*             hint: declare [[service]] + volumes; attachments are removed
services.* (global standalone)              hint: one [[service]] per app file
service_routes.*                            hint: [[service.http]] (see 04-network.md §1)
auto.enabled, auto.allowed_domains,         hint: feature removed; declare explicit interfaces
  auto_route.enabled, auto_route_allowed_domains (both spellings +
  legacy alias; verified config/service.go:153-160)
auto.preview.*, auto.preview.enabled/ttl/   hint: staging is an ordinary app file (01-manifest.md §8.2)
  separator/tag_patterns/data_copy/env_copy
network_isolation.enabled,                  hint: removal TBD — NO disposition frozen here;
  network_isolation.network_prefix          P7 MUST explicitly keep-or-retire before strict
                                            decoding lands (open item, not silent keep)
auth.password                               hint: KEEP reading one release as rejection pointer
                                            (`config-retired`: inline password removed;
                                            use auth.username + token_secret backend),
                                            then join strict rejection
image-label defaults (gordon.* label inference for routing/env)
```

> REVIEW FIX round 3, MEDIUM-7: the previous inventory used umbrella
> names (`autoroute`, `preview.*`) that match NO actual key — the loader
> reads `auto.*` + legacy `auto_route.*` spellings. `external_routes`
> is loaded outside `run.go Config` (config/service.go) and MUST be
> included in the strict-validation schema (KEEP, live class). Strict
> decoding covers the UNION of both loaders; anything outside the
> union is `config-unknown`, anything inside the retired set is
> `config-retired`.

- The rejection is a SMALL explicit retired-key check (retired-key
  set), NOT a legacy parser. REVIEW FIX: current startup/reload use
  ordinary `v.Unmarshal` (`run.go:411,1855`), NOT `UnmarshalExact` —
  so strict unknown-key rejection for NON-retired keys is a NEW
  v2.50 decision (frozen: yes, adopt `UnmarshalExact`-equivalent
  strictness for `gordon.toml` at the same cutover; retired keys
  produce the `config-retired` diagnostic, other unknown keys produce
  `config-unknown`).
- Migration helpers and old backup aliases: delete ONLY where
  superseded (backup identity adaptation keeps working paths);
  symbols merely containing `legacy` are reviewed individually, not
  bulk-deleted (plan mandate).
- `gordon.toml.example`, `docs/config/`, `docs/deployment/`, README,
  CLI docs updated from present v2.50 behavior in P7.2 — docs
  describe the product, not the migration history (changelog covers
  the break separately).

## 4. Reloadability contract (frozen classes)

- **Installation-only reload**: SIGHUP / `POST /admin/reload` / file
  watchers touch ONLY §1-live keys. Reload NEVER activates desired
  app state, re-resolves images, flips intent, or restarts workloads.
  Watchers/signals obey the same rule (existing
  `registerReloadCoordinatorHooks` container-config applier is
  re-scoped at cutover; its reconcile/standalone-service arms are
  removed with the old engine).
- `config show` prints installation data; `apps show` prints
  desired/active data. NEITHER exposes secrets (values, hashes, or
  change metadata).
- Restart-required changes via reload are REPORTED
  (`restart_required: [keys]`) and NOT applied — truthful, never
  silent.

## 5. Effective security parity gate (senior-review mandate)

Retaining TOML keys is INSUFFICIENT. Before cutover, generated
Docker AND Podman container configs are asserted field-by-field:
memory/CPU/PIDs limits, capabilities, no-new-privileges,
strict-profile read-only rootfs + declared read-only mounts, for
app containers AND backup helper containers separately. App manifests
cannot weaken installation policy; V3 hardening defaults are NOT
imported — accepted V2 profiles are preserved. Failure = cutover
blocked.

## Decision required (D6-global — maintainer acceptance)

1. `env.dir` lingering one release as rejection pointer vs immediate
   removal — accept linger?
2. `network_services` retained as infrastructure although the app
   model doesn't use it — accept, or remove with the rest?
3. Named `backups.destinations.<name>` — accept proposal track
   before P6, else single global destination stands?
4. Reload classes table (§1 live/restart) — accept per-key classes?
