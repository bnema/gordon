# 05 — Admin API & CLI Contract (D4 + D5)

Status: draft for maintainer acceptance. Freezes the wire surface for
app mutations: endpoints, DTOs, auth scopes, operation ids,
cancellation/retry behavior, and the CLI lifecycle surface. P5
implements exactly this.

## 1. Single writer: daemon-owned mutations (D4 recommendation)

Verified current shape: local `ControlPlane` calls use cases
in-process (`controlplane_local.go`); remote calls HTTP
(`controlplane_remote.go` → `remote.Client`); non-idempotent requests
are NOT replayed and return `OutcomeUnknownError` on ambiguity
(`client.go:350-433`, `retryMaxAttempts = 4`, idempotent-only retry).
Frozen decision:

**Daemon-owned app mutations through the EXISTING V2 administration
transport for BOTH paths.** Concretely:

- The daemon (`gordon serve`) is the ONLY process that writes app
  state or executes workload effects. The local CLI path does NOT
  write state files directly: when a daemon socket/endpoint is
  reachable, local commands proxy through it (same DTOs, same auth);
  when NO daemon is reachable, local `apps apply`/`deploy`/lifecycle
  commands FAIL with `daemon-unavailable` — no local-write fallback.
  (Rationale: two writers + `flock` covers the file race but NOT the
  in-memory traffic-snapshot / monitor-intent races; one effect owner
  eliminates the class.)
- No new transport: reuse the existing admin HTTP listener + token
  auth (`admin/*` routes in `handler.go:matchRoute`). No V3
  SSH-only/Unix-only requirements.
- Cross-process locking (`02-state.md` `flock` + journal) remains as
  defense-in-depth AND as the upgrade path (old CLI + new daemon →
  explicit version rejection, never silent local writes).

## 2. Endpoints (admin prefix `/admin`)

All app endpoints are mutations except the three reads. Old mutation
endpoints are DELETED end-to-end at cutover (handlers, DTOs, boundary
methods, mocks, auth scopes, completions, docs) — see §7.

```text
POST   /admin/apps/apply              # validate+persist desired (or dry-run)
GET    /admin/apps                    # list apps (desired+active summary)
GET    /admin/apps/{app}              # show desired + active + intent + op ref
POST   /admin/apps/{app}/deploy       # activate a revision
POST   /admin/apps/{app}/stop         # durable stopped intent + stop exact ids
POST   /admin/apps/{app}/start        # clear stopped intent + ensure running
POST   /admin/apps/{app}/restart      # restart from pinned digests
POST   /admin/apps/{app}/remove       # withdraw workloads, retain data (§5)
GET    /admin/apps/{app}/operations/{op}  # op journal/status
GET    /admin/apps/{app}/operations/by-key/{key}  # idempotency recovery (§2d)
GET    /admin/apps/{app}/diff         # desired-vs-active normalized diff
POST   /admin/apps/{app}/secrets/set  # write pass values (names must exist in desired/active)
POST   /admin/apps/{app}/secrets/delete  # refused when referenced (§6, 02-state.md)
GET    /admin/routes                  # read-only (existing, retained as read)
GET    /admin/routes/{domain}         # read-only (existing, retained as read)
```

- Request size bound: apply body ≤ 1 MiB (`MaxBytesReader` + explicit
  `413`); manifest files are small by construction (§4, 01-manifest).
- Every mutation request carries `Idempotency-Key: <ulid>` (client
  generated, REQUIRED). IDEMPOTENCY PROTOCOL (review fix):
  a. The daemon ATOMICALLY persists `{key, request_fingerprint,
     state: pending, op}` in the app's journal index BEFORE any effect
     (same temp+rename discipline as §4, 02-state.md). The fingerprint
     is sha256 over a VERSIONED TUPLE (review fix round 3, HIGH-1 —
     body-only hashing let `stop`/`start`/`remove` with identical bodies
     share a result): `v1 | HTTP-method | canonical route+operation |
     app | service-or-empty | sha256(normalized semantic payload)`.
     The redundant body `idempotency_key`, when present, is EXCLUDED
     from the payload hash (it must equal the header per (e)).
  b. A duplicate submission with the SAME key + SAME fingerprint
     while `pending` BLOCKS until the first completes, then returns
     the RECORDED result (no re-execution). After completion it
     returns the recorded result immediately.
  c. SAME key + DIFFERENT fingerprint → `409 idempotency-key-reuse`
     (client bug; never executes).
  d. The FIRST response always includes the `op` id; on transport
     ambiguity the client recovers by RE-QUERYING WITH THE SAME KEY
     (`GET /admin/apps/{app}/operations/by-key/{key}` →
     `{op, state, result}`) instead of needing an op id it never
     received. Key records: PENDING claims NEVER expire (explicit
     prohibition — a pending claim is a live lock; expiry would orphan
     it). COMPLETED records TTL 24h for replay convenience; after
     expiry the key reads as NEVER-SEEN (distinguishable code
     `idempotency-key-unknown`, NOT `expired`), and reusing it as
     new is FORBIDDEN without operator outcome inspection first
     (review fix round 3, HIGH-2 — expiry never proved non-execution:
     the operator MUST query app state/show + op history and issue an
     explicit new intent; the CLI refuses silent re-keyed retry of a
     completed-then-expired mutation).
  e. Header-vs-body precedence: the HEADER is authoritative; a body
     `idempotency_key` that differs → `400 idempotency-key-mismatch`.
  This replaces the previous "store executed keys" sentence, which
  left the crash-after-effect window open.
- **Never retry a mutation on ambiguous response without idempotency**: the CLI surfaces
  `OutcomeUnknownError` semantics through to the operator
  (`outcome-unknown: op may have executed; query
  /operations/{op} before retrying`) and retries ONLY with the same
  key.
- Cancellation: client `ctx` cancel aborts WAITING, never an engaged
  step — the op continues to a safe point server-side and the journal
  records `client-detached`; the operator re-queries the op.

## 3. DTOs (frozen field names)

```json
// POST /admin/apps/apply  { "manifest_toml": "...", "dry_run": false }
// NOTE: apply is idempotent-by-content, NOT by Idempotency-Key claim:
// dry-run writes NOTHING (02-state.md §4.3 — exempt from key claims),
// no-change apply returns the existing REV with no journal (exempt),
// and accepted apply returns `intent` (the durable apply intent id)
// instead of `op` (review fix round 3, MEDIUM-4 — the old text demanded
// both "every mutation persists a claim" and "operation: null").
// Recovery for accepted apply uses the INTENT id
// (GET .../operations/by-key/{intent} → {revision, state}).
{ "app": "blog",
  "former_revision": "rev-…", "resulting_revision": "rev-…",
  "pending": true, "noop": false,
  "diff": { "added": […], "removed": […], "changed": […] },
  "warnings": ["…"], "intent": "apply-…" }

// dry_run=true → same shape, resulting_revision is "rev-preview"
// (never persisted), "pending" false, intent null.

// POST /admin/apps/{app}/deploy  { "revision": "rev-…"(optional),
//   "service": "web"(optional) }
{ "op": "op-…", "app": "blog", "revision": "rev-…",
  "outcome": "success|partial|failed",
  "services": { "web": { "result": "deployed|failed",
      "effective_revision": "rev-…", "before": "ctr-…",
      "after": "ctr-…", "restart_unsafe": false } },
  "steps": [ { "id": "service.web.replace", "state": "succeeded",
                "before": "ctr-…", "after": "ctr-…" } ],
  "cleanup_warnings": [ { "service": "web", "leftover": "ctr-…",
                "detail": "retire failed after publish" } ],
  "effective": { "converged": false, "converged_revision": "rev-…",
    "services": { "web": "rev-…", "db": "rev-…" } },
  "observed": { "running": […], "stopped": […] },
  "retained": { "volumes": […], "secrets": […] } }
// (review fix round 3, MEDIUM-5: per-service terminal results +
// cleanup warnings + per-service effective map replace the old
// steps-only DTO and scalar effective.revision.)

// GET /admin/apps/{app}
{ "app": "blog",
  "desired": { "revision": "rev-…", "status": "pending|active" },
  "active": { "converged": false, "converged_revision": "rev-…",
    "services": { "web": { "effective_revision": "rev-…",
      "digest": "sha256:…", "container": "ctr-…",
      "restart_unsafe": false } } },
  "intent": { "stopped": false },
  "last_op": { "op": "op-…", "outcome": "success" } }
// (review fix round 3, HIGH-3: per-service restart_unsafe surfaced so
// operators can see WHY recovery is blocked; start/restart below gate
// on it with `recovery-blocked-unsafe`.)
```

- JSON and text outputs have EQUIVALENT semantics; stable sorted
  ordering; NO secret values or sensitive command output anywhere
  (values never leave pass; `observed` contains ids/digests/hosts).
- Error envelope: DELIBERATE VERSIONED CHANGE from the current
  `dto.ErrorResponse{Error}` single-field shape (verified
  `dto/error.go:4-6`, `admin/handler.go:607`; the remote parser
  already tolerates `cause`/`hint`/`logs`, `client.go:372-396`).
  Frozen v2.50 app-mutation envelope:
  `{ "error": "<code>", "message": "…", "cause": "…", "hint": "…" }`
  (`cause`/`hint` optional; `logs` NEVER on mutations — no sensitive
  command output). Client parsing MUST accept both the legacy
  single-field and the new envelope during the mixed-version window.
  Frozen code registry: `app-state-io`, `app-state-corrupt`,
  `manifest-invalid`, `reservation-conflict`, `secret-missing`,
  `secret-referenced`, `image-unresolvable`,
  `unmanaged-image-volume`, `bind-failed`,
  `traffic-snapshot-conflict`, `daemon-unavailable`,
  `outcome-unknown`, `prune-disabled`, `op-not-found`,
  `config-retired`, `config-unknown`, `scope-retired`, `endpoint-retired`,
  `idempotency-key-reuse`, `idempotency-key-mismatch`,
  `idempotency-key-unknown`, `recovery-blocked-unsafe`.

## 4. Auth scopes (extends existing model)

Verified model (`domain/auth.go`): resources `routes | secrets |
config | status | logs | volumes | *`, actions `read | write | *`.
Frozen extension — ONE new resource, no new action vocabulary:

- `AdminResourceApps = "apps"` with existing `read`/`write`.
- Mapping: `apps apply/list/show/diff` → `apps:read` for reads,
  `apps:write` for apply; `deploy/stop/start/restart/remove` →
  `apps:write`; `secrets set/delete` → `apps:write` AND
  `secrets:write` (both scopes required — app secret writes stay
  under the secrets authority too); op queries → `apps:read`.
- Old mutation authorization: REVIEW CORRECTION — `domain/auth.go`
  defines BROAD resources only (`routes|secrets|config|status|logs|
  volumes|*`), NOT per-feature bootstrap/attachment/autoroute/preview/
  pin scope entries. What cutover actually removes is the ENDPOINT +
  its `HasAccess` check site. Authenticated requests reaching a retired
  route get `410 Gone` + `{"error":"endpoint-retired"}` (review fix
  round 3, MEDIUM-6 — the old text promised `403` here, contradicting
  the §7 `410` contract; ordinary authN/authZ failures keep their
  existing 401/403 behavior on LIVE routes). The retired surface list
  (§7) names endpoint paths, not scope strings; there is no scope
  string to delete from the registry beyond the NEW `apps` resource
  lifecycle.

## 5. CLI lifecycle surface (D5 proposal for acceptance)

Group: `manage` (local-or-remote via resolver, like current routes).
All list/show support `--json` (existing `writeJSON`); output via
`cmd.OutOrStdout()`; `cmd.Context()`; errors from `RunE`.

```text
gordon apps apply --file blog.toml [--dry-run] [--deploy]
  # --dry-run + --deploy together → hard error (plan contract).
  # --deploy chains EXACTLY the accepted REV; persistence success and
  # deploy outcome reported SEPARATELY (deploy may fail after apply
  # succeeded — exit code 2/1 per 03-deployment.md §7).
gordon apps list [--json]
gordon apps show APP [--json]            # desired + active + intent
gordon apps diff APP [--json]            # normalized desired-vs-active
gordon deploy APP [--revision REV] [--service SVC] [--json]
gordon restart APP [--service SVC] [--json]   # pinned digests, no re-resolve;
                                         # REFUSED with recovery-blocked-unsafe
                                         # for services with restart_unsafe set
                                         # (explicit deploy clears it; HIGH-3)
gordon stop APP [--json]                 # durable stopped intent; preserves ALL data
gordon start APP [--json]                # clear intent; ensure running from ACTIVE;
                                         # services with restart_unsafe set stay
                                         # blocked (recovery-blocked-unsafe) until
                                         # explicit deploy — start is NOT an escape
                                         # hatch (HIGH-3)
gordon remove APP [--json]               # withdraw workloads; volumes+secrets
                                         # retained as owned orphans; name reserved
gordon apps secrets set APP --service SVC KEY=VALUE… [--stdin] [--json]
gordon apps secrets delete APP --service SVC KEY [--json]
  # values via flag (discouraged, shell history) or --stdin / interactive
  # prompt (preferred); confirms names exist in desired/active first.
  # --service is REQUIRED (service-scoped storage; review fix).
gordon logs APP [--service SVC] [--follow] [--tail N] [--json]
gordon status APP [--json]               # effective vs observed, per service
```

- There is deliberately NO `purge` in the frozen surface: the plan's
  `purge` (explicit destructive volume deletion) requires a
  separately accepted destructive-action contract (exact preview,
  verified ownership, explicit confirmation, refusal of unknown/old-V2
  resources). Until then deletion paths fail closed. `remove` retains
  everything; manual `volumes prune` stays ownership-gated
  (`04-network.md` §8).
- Domain-based `deploy`/`restart`/`logs`/`status` keep working for
  non-app resources until cutover, then route to app/service
  inspection (diagnostic placement settled here: `status APP`
  replaces domain status for apps; `routes` stays read-only global).
- Push (`pkg/registrypush` 50 MiB chunking, auth/redirection,
  cleanup, progress, integration tests) is PRESERVED with deploy/route
  inference DELETED — push never deploys, creates routes, or modifies
  manifests.

## 6. Reload semantics (installation-only)

- SIGHUP / `reload` endpoint / file watchers: installation settings
  ONLY. Reload NEVER activates desired app state, re-resolves images,
  or flips intent. Setting classes (frozen in `06-config-disposition.md`):
  reloadable live (log level, rate limits, allowlists) vs
  restart-required (ports, data dir, TLS files) reported truthfully —
  `config show` (installation) and `apps show` (desired/active) never
  expose secrets.
- Old-config presence (routes→image, attachments, services, …) is
  REJECTED at startup AND reload BEFORE any app/runtime mutation,
  with a small explicit diagnostic naming the offending keys (not a
  legacy parser, not a migration).

## 7. Deletion inventory (cutover executes; frozen here)

Delete end-to-end (CLI command + ControlPlane method + remote client
method + admin handler + DTO + boundary method + mock + auth scope +
event subscription + completion + docs): `pin`, `preview
create/list/delete/extend`, `attachments` family, `bootstrap`,
`autoroute allow`, `routes add/remove/purge`, push deploy/route
inference. `services.Service.Reconcile` removed when the deploy
engine wires in. `RegisterRoutes`/`matchRoute` entries for the above
removed; old clients hitting them get `410 Gone` +
`{"error":"endpoint-retired"}` (not 404 — explicit signal for
mixed-version clients), EXCEPT phased internal consumers first
(backup `ListAttachments` coupling adapted BEFORE its callers die —
senior-review ordering).

## Decision required (D4+D5 — maintainer acceptance)

1. Daemon-only writes, local-without-daemon FAILS (`§1`) — accept?
   (Alternative: local direct-write + flock; NOT recommended §1.)
2. `Idempotency-Key` required on all app mutations — accept?
3. `410 Gone` + `endpoint-retired` for removed mutations — accept?
4. NO `purge` until destructive-action contract — accept?
5. `stop` preserves all data + durable intent; `remove` retains
   volumes/secrets as reserved orphans — accept exact semantics?
6. `apps:read/write` (+ `secrets:write` for secret values) — accept?
