# 02 — App State Store Contract (D1 + pass consistency)

Status: draft for maintainer acceptance. Freezes the persistence
mechanism, on-disk layout, atomicity protocol, journal/recovery rules,
and the pass secret-identity contract. P2 implements exactly this.

## 1. Recommendation: atomic-file JSON store, no new dependency

`go.mod` contains no embedded transactional storage (verified: no
`sqlite`/`bbolt`/`pebble` dependency, direct or indirect). D1 forbids
introducing a dependency by inference and forbids repurposing
`filesystem/manifest.go` (OCI manifest storage). The frozen choice is:

**Versioned JSON files under the installation data dir, atomic
write = temp file + fsync + rename + dir fsync, cross-process
coordination = `flock(2)` on a store lock file, all with stdlib only
(`os`, `encoding/json`, `syscall.Flock`).**

Evidence for atomic-file over embedded transactional storage:

- State size is small (tens of apps × few-KiB revisions + journals);
  no query load beyond key lookup by app name and single global
  reservation scan — no index engine needed.
- Crash semantics needed are exactly what rename-atomicity gives:
  a reader never observes a torn revision. A DB would add WAL
  recovery machinery for no query benefit.
- Coordination needed is a single coarse mutex across processes
  (daemon + CLI local path); `flock` on one file provides it without
  a lock-service dependency.
- Human-debuggable state (plain JSON) matters for a documented
  breaking release with manual backup/restore procedures.

If the maintainer prefers an embedded store, that is a contract
change requiring a named dependency and its own crash evidence —
not a P2 implementation detail.

## 2. Layout (under `resolveDataDir`, default `~/.gordon`)

```text
<dataDir>/apps/
  store.json                # store format version + global reservation table
  store.lock                # flock file (zero bytes, lock only)
  <app>/                    # <app> = normalized app name (§3, 01-manifest.md)
    desired.json            # latest accepted desired revision (or absent)
    active.json             # per-service effective definitions (or absent, §3.2)
    intent.json             # running/stopped intent (always present after first deploy)
    intents/
      apply-<ulid>.json     # durable apply intents: staged→committed→applied (§4)
    revisions/
      rev-<ulid>.json       # immutable accepted desired revisions (GC: keep last 32
                            # PLUS every revision referenced by active/in-flight, §3.2)
    journal/
      op-<ulid>.json        # durable operation records, append-only per op
    ownership.json          # volume/secret/network ownership records
```

- `store.json`: `{ "version": 1, "reservations": [ … ] }`.
  Reservations are GLOBAL (cross-app listener conflicts) and live in
  this single file so one lock acquisition covers conflict checks.
- Per-app files are owned by exactly one app mutation at a time.
- `filesystem/manifest.go` (OCI blobs/manifests under
  `<dataDir>/repositories`) is untouched by this layout.
- Version handling: unknown `version` > supported → refuse ALL app
  mutations with a descriptive error, no overwrite, no downgrade path
  (binary downgrade against new app-state format is unsupported per
  plan). Corrupt JSON → refuse the affected app scope, keep serving
  other apps, surface `app-state-corrupt` diagnostic; never
  auto-repair by deleting.

## 3. Record schemas

### 3.1 Desired revision (`desired.json`, `revisions/rev-<ulid>.json`)

```json
{
  "revision": "rev-01K5X7QZ3AB0C9D8EFGHJKMNPQ",
  "app": "blog",
  "supersedes": "rev-01K5X7QZ3AB0C9D8EFGHJKMNP0",
  "accepted_at": "2026-09-08T15:04:05Z",
  "source_sha256": "e3b0…",
  "spec": { "…normalized manifest…": true },
  "reservations": [
    { "proto": "tcp", "ip": "0.0.0.0", "port": 25565, "service": "server" },
    { "proto": "udp", "ip": "0.0.0.0", "port": 28015, "service": "server" },
    { "proto": "http", "host": "blog.example.com", "service": "web" }
  ],
  "secrets_required": ["gordon/apps/blog/web/database-url"],
  "secrets_env": {"gordon/apps/blog/web/database-url": "DATABASE_URL"},
  "status": "pending"
}
```

- `spec` is the FULLY NORMALIZED manifest (defaults applied,
  missing tag → explicit `:latest`, hosts canonicalized). The source
  file bytes hash (`source_sha256`) is stored for audit; the file
  itself is never re-read.
- `status`: `pending` (accepted, not deployed) — transitions to
  `active` only via a completed deploy op that pins it (see §3.2).
  No-change apply (normalized spec identical to current desired)
  returns the existing revision idempotently: no new file, no journal.

### 3.2 Active definition (`active.json`)

> REVIEW FIX (senior round 1, HIGH-2): the previous single-revision
> active schema could not represent partial deployment (services on
different definitions) and revision GC could evict the active
> revision. Active now stores PER-SERVICE effective definitions with
> immutable revision references; GC MUST retain every referenced
> revision.

```json
{
  "app": "blog",
  "converged_revision": "rev-01K5X7QZ3AB0C9D8EFGHJKMNPQ",
  "converged": false,
  "services": {
    "web": {
      "effective_revision": "rev-01K5X7QZ3AB0C9D8EFGHJKMNPQ",
      "activated_by": "op-01K5X7R1AB0C9D8EFGHJKMNPR",
      "activated_at": "2026-09-08T15:06:11Z",
      "image": "registry.example.com/blog/web:1.4.2",
      "digest": "sha256:abc…",
      "container": "ctr-77c…",
      "spec": { "…full normalized per-service definition…": true }
    },
    "db": {
      "effective_revision": "rev-01K5X7QZ3AB0C9D8EFGHJKLMN00",
      "activated_by": "op-01K5X7Q9AB0C9D8EFGHJKMNPQ",
      "activated_at": "2026-09-08T14:58:02Z",
      "image": "docker.io/library/postgres:16.4",
      "digest": "sha256:def…",
      "container": "ctr-1ab…",
      "spec": { "…full normalized per-service definition…": true }
    }
  },
  "stop_intent": false
}
```

- Each service's `spec` is the FULL normalized per-service
  definition (interfaces, volumes, secrets paths, readiness) copied
  from its effective revision at activation — traffic projection,
  diff, secret protection, and stopped-app recovery read `spec`,
  never just digests. `converged` is true iff all services share
  `converged_revision`; otherwise the app is `partial` and diff/API
  surfaces report per-service effective revisions (see `05-api-cli.md`
  §3 — the `active.services` map replaces the single-revision shape).
- `pinned` digests: restart reuses them; only explicit deploy
  re-resolves mutable tags.
- Revision GC: keep last 32 PLUS every revision referenced by
  `desired.json`, any service `effective_revision` in ANY app's
  `active.json`, or any non-terminal journal op input. Referenced
  revisions are NEVER evicted.
- Reboot recovery reads `active.json` service specs (+ `intent.json`),
  NEVER `desired.json` pending revisions or fresh tag contents.

### 3.3 Intent (`intent.json`)

```json
{ "app": "blog", "stopped": false, "updated_by": "op-…", "updated_at": "…" }
```

Durable stopped intent. `stopped: true` survives reboot, engine
restarts, and Gordon absence — nothing starts these containers except
an explicit deploy/start op (see `03-deployment.md` restart authority).

### 3.4 Operation journal (`journal/op-<ulid>.json`)

Single file per operation, rewritten atomically at each step
transition (temp+rename). Never contains secret VALUES.

```json
{
  "op": "op-01K5X7R1AB0C9D8EFGHJKMNPR",
  "kind": "deploy",
  "app": "blog",
  "input_revision": "rev-…",
  "started_at": "…",
  "steps": [
    { "id": "preflight", "state": "succeeded" },
    { "id": "network.ensure", "state": "succeeded", "detail": "net=gordon-app-blog" },
    { "id": "service.web.replace", "state": "succeeded",
      "before": "ctr-9f2…", "after": "ctr-77c…" },
    { "id": "service.db.replace", "state": "failed", "error": "…" },
    { "id": "service.db.replace", "state": "not-run" }
  ],
  "outcome": "partial"
}
```

- The input (revision id, pinned digests captured at preflight) is
  persisted BEFORE any effect. Resume-after-crash observes actual
  runtime state (container ids, network attachments) and continues or
  reports; it never assumes a step recorded as started actually
  completed.
- A stale retry MUST NOT remove a container id that differs from the
  recorded `before`/`after` identity (replacement happened since).

### 3.5 Ownership (`ownership.json`)

```json
{
  "app": "blog",
  "volumes": [{ "name": "pgdata", "service": "db", "runtime_name": "gordon-blog--db--vol--pgdata", "state": "attached" }],
  "services": { "db": { "restart_unsafe": false } },
  "secrets": [{ "service": "db", "env": "POSTGRES_PASSWORD", "name": "postgres-password", "path": "gordon/apps/blog/db/postgres-password", "state": "referenced" }],
  "networks": [{ "name": "gordon-app-blog", "role": "private" }]
}
```

- Removed services move their volumes/secrets to `"state":
  "retained"` — visible to inspection, never auto-deleted.
- UNKNOWN resources (no ownership record, any labels) are NEVER
  cleanup candidates. Old-V2 `gordon.managed=true` volumes without an
  app ownership record are preserved, not adopted.

## 4. Atomicity protocol

> REVIEW FIX (senior round 1, HIGH-1): individual rename atomicity
> does NOT make a multi-file sequence transactional. The protocol
> below replaces the previous write-order description with a durable
> intent + single commit point + recovery-before-mutation. The old
> claim "failed apply leaves NO new files visible" is withdrawn:
> failed/orphaned intents are VISIBLE in `intents/` until GC, and
> that visibility is what makes recovery possible.

1. Lock order (fixed, global): **store.lock → app dir lock
   (`<app>/.lock`) → traffic snapshot → runtime effects.** Every
   mutation path acquires in this order; no path acquires a subset in
   a different order.
2. Apply (frozen intent protocol):
   a. Validate + compute normalized spec (no writes).
   b. Under `store.lock`, write `intents/apply-<ulid>.json`
      (temp+fsync+rename+dirsync) containing the FULL candidate:
      normalized spec, reservation deltas, supersedes pointer,
      source hash. State: `staged`.
   c. THE SINGLE COMMIT POINT is one atomic rename of
      `intents/apply-<ulid>.json` from `staged` to `committed`
      (same-dir rename = atomic). Before this rename, a crash means
      "nothing happened" (orphan `staged` intents are ignored by
      readers and GC'd).
   d. AFTER commit, materialize effects in order: revision file →
      `desired.json` pointer → `store.json` reservation table.
      A crash HERE means "committed but partially materialized".
   e. RECOVERY-BEFORE-MUTATION: every apply/deploy boot path first
      scans `intents/` for `committed`-but-incompletely-materialized
      intents and finishes their materialization (idempotent file
      writes keyed by content hash) BEFORE accepting new mutations.
      Only then is the intent moved to `applied` (GC-eligible).
   f. Reservation AUTHORITY (review fix round 2 — checkpoint + deltas):
      the AUTHORITATIVE state is (a) the `store.json` reservation table
      AS CHECKPOINTED at each completed materialization, plus (b) the
      ordered overlay of `committed`-but-unmaterialized intent deltas.
      The table is authoritative for everything materialized; intents
      are authoritative for everything committed-but-pending. Intent GC
      is SAFE only for `applied` (fully materialized) intents — their
      deltas are already folded into the checkpoint. Release transitions
      (withdrawal verified, `04-network.md` §1) enter the model as
      checkpoint updates at `active.publish`/retire time, never as intent
      deletions. Recovery rebuilds by loading the checkpoint, then
      replaying non-`applied` intents in ULID order. Rename/fsync failure
      at (b) → `staged` intent + atomic failure, safe to retry. Failure
      at (d) → recovery (e) completes it; re-query by intent id returns
      the committed result (see idempotency, `05-api-cli.md` §2).
      Rollback after an uncertain commit is NEVER promised — only forward
      completion.
3. Dry-run performs steps up to and including validation and conflict
   check against a SNAPSHOT copy — it writes nothing and reserves
   nothing.
4. Deploy: persist journal with input + `pending` steps BEFORE first
   effect; each step transition is an atomic journal rewrite. Per-service
   `active.publish` follows that service's traffic commit (machines 4A/4B
   in `03-deployment.md`); there is no whole-app pointer flip — the
   previous sentence claiming otherwise is superseded. Deploy storage
   failures (journal write/sync errors) are distinct from per-service
   runtime failures: a storage failure halts the op with
   `outcome-unknown`-class semantics (re-query by op/idempotency key)
   and NEVER reports per-service `failed` for steps never engaged.
5. `fsync` strategy: file fsync before rename, parent-dir fsync after
   rename. PHASE-SPECIFIC I/O OUTCOMES (review fix — replaces the old
   blanket "fails atomically" rule, which contradicted the commit
   protocol):
   - Definitely pre-commit (failure in §4.2a–b, before the commit
     rename returns): NOT accepted, `app-state-io`, safe to retry.
   - Commit durability UNCERTAIN (commit rename returned but dir-fsync
     failed or response lost): `outcome-unknown`; the client MUST
     re-query by intent id / idempotency key — recovery (§4.2e) will
     have completed or will complete materialization. NEVER report
     "not accepted".
   - Definitely committed (commit + dir-fsync acked, crash during
     §4.2d materialization): ACCEPTED, materialization pending;
     recovery completes forward; re-query returns the committed result.

## 5. Reservation rules (global table in `store.json`)

- Namespaces: TCP and UDP are SEPARATE. HTTP is keyed by
  canonical host. TLS-`always` and `auto`-with-TLS share the 443
  listener namespace (conflict = same host).
- Overlap detection: wildcard (`0.0.0.0`/`::`) vs specific-IP binds
  on the same port+proto conflict; IPv4/IPv6 dual binds of one
  declaration are ONE reservation entry with `ip: "dual"`.
- Scope: desired + active + in-flight (journal steps not terminal)
  are all checked. Same-app update RETAINS old active reservations
  until actual withdrawal (no self-conflict window for the deploy to
  fall into).
- Literal binds only (`publish` accepts IP literals; hostnames
  rejected at manifest validation). No DNS resolution feeds the
  reservation table.
- External occupation (a non-Gordon host process on the port) is
  NOT a reservation-table entry: apply reports `observed-occupied`
  when a loopback dial succeeds, `unknown` otherwise; deploy rechecks
  and real bind failure fails the step with `bind-failed` (see
  `04-network.md`). An effect-free apply MUST NOT claim to lock ports
  against unrelated host processes.
- Gordon-generated backend binds (ephemeral loopback ports for
  private backends, if D3 requires them) are reserved entries with
  `owner: "gordon-backend"`.

## 6. Pass secret-identity contract (extends D1/D4)

Keep `pass`, NOT a new secret database. App secrets use the NEW path
prefix (no adoption of `gordon/env/…`):

```text
gordon/apps/<app>/<service>/<secret-name>   # value entry (one pass item per name)
```

- `<app>`/`<service>`: normalized identifiers (`01-manifest.md` §3).
  `<secret-name>`: SERVICE-NAME charset (lowercase alphanumerics +
  `_. -`, hyphens allowed) — DISTINCT from the ENV key namespace
  (`ValidateEnvKey`). The ENV key NEVER appears in the path
  (canonical model; review fix). Path traversal (`..`, `/`, `\`)
  rejected by `secrets.ValidatePath` + sanitization in the NEW
  app-path adapter boundary (the existing `PassStore.keyPath`
  validates ENV keys for `gordon/env/…` and is NOT reused — a new
  constructor builds `gordon/apps/…` paths with service-name-charset
  validation).
- NO manifest sidecar (`.keys`-style) for app secrets: the desired
  revision's `secrets_required` list IS the membership record. This
  removes the self-heal divergence class (today `ListKeys` merges the
  manifest with directory listing and writes back — verified in
  `PassStore.ListKeys`). For app paths, reads MUST NOT self-heal:
  validation is effect-free; deploy preflight reads values once and
  reports missing keys without writing anything to pass.
- Ordering (pass and app-state are NOT one transaction) — BOOTSTRAP
  ORDER (review fix, matches 01-manifest.md §5.4):
  1. `apply` registers secret NAMES (validation only, NO value reads).
  2. Operator writes secret VALUES to pass via
     `apps secrets set APP --service SVC …` (separate op; refused for
     names absent from desired/active).
  3. Deploy preflight reads VALUES; missing/unreadable (missing
     pass, missing GPG key) fails preflight with `secret-missing`
     BEFORE any workload mutation; nothing is activated.
  4. Deploy records only secret PATHS (`…/<secret-name>`) in the
     journal, never values or hashes (hashes would leak
     change-frequency metadata into diffs).
- Deletion protection: deleting a secret whose path appears in ANY
  of desired / active / in-flight (journal non-terminal) for the app
  is refused with `secret-referenced`. Direct operator edits in pass
  outside Gordon are tolerated but never trusted: preflight re-reads
  and fails closed on disagreement; no repair writes.
- Existing backends stay: installation `auth.secrets_backend`
  (`pass`/`sops`/`unsafe`) and token handling are untouched. App
  secret VALUES additionally accept ONLY the `pass` backend (SOPS /
  `unsafe` never serve app secrets); agreeing on pass for app
  secrets does NOT authorize deleting SOPS/`unsafe` elsewhere.

## 7. Crash / recovery matrix

| Crash point | Recovery behavior |
|---|---|
| During apply, intent `staged` (pre-commit) | Orphan intent ignored by readers; GC removes; state unchanged; safe to retry |
| During apply, after commit, mid-materialization | Recovery-before-mutation (§4.2e) completes materialization from the committed intent; re-query by intent id returns the committed result |
| After apply, before deploy | Desired `pending`; reboot ignores it; active untouched |
| During deploy preflight | Journal `failed` at `preflight`; no effects; safe to retry |
| Between journal step N and N+1 | Resume observes runtime (container ids, attachments, route snapshot); completed steps skipped by identity match, never by sequence assumption |
| Between stop-intent persist and container stop | On boot, intent `stopped:true` + container still running → stop the EXACT recorded container id, report `recovered-stop` |
| During traffic commit | Snapshot ownership (see `04-network.md`): only the op holder commits; stale holder's commit rejected by snapshot id |
| Host reboot, any state | Load active specs + intent + `restart_unsafe` flags BEFORE traffic. Missing running-intent containers recreated from active pinned digests — EXCEPT services with `restart_unsafe: true` → report `recovery-blocked-unsafe`, do nothing. Stopped stays stopped; pending desired never activated; interrupted initial deploy resurrects NOTHING |
| Corrupt/incompatible store | All app mutations refused with explicit error; running workloads untouched; no auto-repair |

## Decision required (D1 — maintainer acceptance)

1. Atomic-file JSON + `flock` (stdlib only) as the single mechanism —
   accept, or name the embedded store + its crash evidence?
2. Layout under `<dataDir>/apps/` with `store.json` global table —
   accept exact paths?
3. `revisions/` retention (last 32) — accept number?
4. No `.keys` sidecar for app secrets; desired revision is the
   membership record — accept?
5. App secret values via `pass` backend ONLY — accept?
6. Fixed lock order store → app → traffic → runtime — accept?
