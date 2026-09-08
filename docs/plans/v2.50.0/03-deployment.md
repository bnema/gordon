# 03 — Deployment Engine Contract (D2 + restart authority)

Status: draft for maintainer acceptance. Freezes the single app
deployment engine: eligibility, preflight, step ordering, readiness,
shutdown, partial-failure policy, and who owns restarts. P4 implements
exactly this; until the cutover gate, this engine is unreachable and
the existing `services.Service.Reconcile` path stays the only engine.

## 1. One engine, explicit cutover

- The deploy engine lives in a NEW `internal/usecase/deployment/`
  package. Reusable container helpers move inward (refactor), but
  `services.Service.Reconcile`, the route-container engine, and all
  implicit deployment paths (`ImagePushedHandler` application
  effects, `ConfigReloadHandler` deployment effects, autoroute,
  label/env-file inference, pin/preview hooks) are removed AT the
  cutover slice — not before (prepared replacements must build and
  pass together), not after (no two permanent engines).
- Exactly one workload engine is reachable before AND after cutover.
  The cutover slice flips bootstrap, handlers, event subscriptions,
  monitor/recovery, schedulers, and config rejection TOGETHER.

## 2. Deploy input capture

Deploy operates on a CAPTURED revision, never on live desired:

```text
deploy APP [--revision REV | --deploy flag from apply]
  1. resolve REV (default: current desired)
  2. persist journal op-<ulid> with input = { revision, normalized spec,
     pinned digests AFTER preflight } BEFORE any effect
  3. execute steps; record planned/succeeded/failed/not-run per step
```

- `apply --deploy` chains with EXACTLY the revision apply accepted
  (the returned `REV` is passed through, not re-resolved).
- Service-targeted deploy (`deploy APP --service SVC`) REFUSES when
  desired ≠ active-known-effective for any service (pending
  desired/effective divergence): it never activates part of a new
  manifest. Targeted deploy against a converged app (desired == active
  revision) re-runs that service's steps only (restart/replace in
  place from pinned digests).
- `restart` reuses active pinned digests (no re-resolution).
  Explicit `deploy` re-resolves mutable tags in preflight.

## 3. Preflight (no workload mutation before ALL pass)

For each service in the captured revision, in order:

1. **Image resolution**: resolve every image ref to a digest
   (installation registry for unqualified; allowlist + require-digest
   policy for external). ANY failure → op fails at `preflight`,
   NOTHING is activated, previously running services untouched.
2. **Secret presence**: read every path in `secrets_required` from
   pass. Missing/unreadable → `secret-missing`, same fail-closed.
   Values are held in memory only for container creation; never
   written to journal/state/logs.
3. **Image volume inspection**: `InspectImageVolumes` (existing
   boundary) — images declaring `VOLUME`s that are NOT explicitly
   mapped in the manifest FAIL preflight with `unmanaged-image-volume`
   (no silent anonymous-volume creation). Explicit mapping of every
   declared image volume is required.
4. **Reservation recheck**: re-validate the revision's reservations
   against the live global table (apply-time check may be stale).
5. **Resource preconditions**: named volumes exist-or-creatable,
   networks ensurable, ownership records consistent.

Preflight records the pinned digest table into the journal. Steps
after preflight use ONLY pinned digests.

## 4. Step ordering — two replacement state machines

> REVIEW FIX (senior round 1, HIGH-3): the previous single sequence
> flipped active per service before the final traffic commit (while
> state §4.4 flipped after commit), never named container START
> (V2 `createAndStart` does Create+Start, `services/service.go:240`),
> had no stop-old-before-start-new branch for volume owners, and
> reversed the removal predicate. Frozen below: separate stateless
> vs volume/game machines with ONE publication point each, and
> removal = present in ACTIVE, absent from captured DESIRED.

### 4A. Stateless web-style replacement (no volumes, TCP/HTTP)

```text
1. network.ensure     — create/reuse nets (add new; removals deferred)
2. container.create   — create replacement (pinned digest, pass env,
                         ownership labels, installation limits/profile)
3. container.start    — START replacement (explicit step; V2 parity)
4. readiness.wait     — bounded check PROBING THE REPLACEMENT's private
                         backend directly (§5 fix); on timeout → stop +
                         remove replacement, FAIL service
5. traffic.stage      — stage candidate backends WITHOUT committing
6. traffic.commit     — single coordinated commit (snapshot ownership,
                         04-network.md §1); ONLY NOW does the replacement
                         serve
7. active.publish     — flip this service's effective definition in
                         active.json (per-service, §3.2 of 02-state.md)
8. container.retire   — drain old per installation drain_timeout, stop
                         old by EXACT id, remove WITHOUT volume flags,
                         prune removed memberships
```

### 4B. Volume-owning / game / UDP replacement (interrupting)

```text
1. network.ensure / volume.ensure (volumes NEVER deleted)
2. unsafe-restart.record — durably record `old_image_unsafe: true` for
   this service in the journal BEFORE the replacement can write. From
   this point, crash recovery MUST NOT restart the old image.
3. container.stop-old — stop old by EXACT id (SIGTERM → SIGKILL after
   stop_grace); game/UDP: DECLARED interrupting in deploy output
4. container.create + container.start (replacement)
5. readiness.wait (none|log for UDP-only; NEVER bind-claims)
6. traffic.commit (same snapshot ownership)
7. active.publish (per-service flip)
8. container.retire-old (remove old container, no volume flags)
```

- No early exposure: backends serve only after readiness (4A) or
  after start+readiness with declared interruption (4B).
- Initial deploy (no active) skips retire; interrupted initial deploy
  resurrects NOTHING.
- Service removal (present in ACTIVE, absent from captured DESIRED):
  stop exact container, remove (no volume flags), mark
  volumes/secrets `retained`, withdraw routes. NEVER the reverse
  predicate.

## 5. Readiness fields (explicit, bounded)

Reusing the manifest §5.2 types. Engine semantics:

- `none`: proceed immediately after container running state.
- `tcp`: INTENTIONAL CHANGE from V2 semantics (verified V2
  `tcpReadinessAddress`, `services/service.go:503-522`, dials the
  PUBLISHED address). In the app model `publish` belongs to Gordon's
  router and traffic shifts only AFTER readiness — dialing it would
  test the OLD backend (replacement) or fail (initial deploy).
  Frozen: probe the REPLACEMENT's private backend directly
  (`<container-ip>:<port>` on the app private network, or the
  loopback-generated backend bind where the runtime requires it).
  Port selection: when exactly ONE TCP-capable interface (http/tcp/
  rcon container port) exists, it is probed. When MORE than one
  exists, explicit `readiness.port = <container-port>` (NEW manifest
  field, see 01-manifest.md §5.2) selects; absent with >1 candidate =
  validation error. Output labels L4-accept, NOT application health.
- `http`: NEW in v2.50. `GET` backend path, accept 2xx/3xx until timeout.
- `log`: V2 semantics — `path` + `contains` both required, 1 MiB cap.
- Default timeout `30s`, bounds `1s`–`10m` (manifest validation).
- UDP-only services: `none` or `log` ONLY (validation rejects
  `tcp`/`http` for services with UDP interfaces and no TCP/HTTP
  interface). NEVER claim UDP application readiness from a bind.
- Readiness failure stops and removes the REPLACEMENT; the old
  container keeps serving (web-style) or the service stays on old
  definition until policy decides (§7). The failed replacement's
  volumes are NOT rolled back if the new image may have written
  data — report `replacement-writes-unknown`, keep new volumes
  attached to the retained record, never auto-restart the old image
  against possibly-migrated data (plan mandate).

## 6. Shutdown semantics (stop signals, drain, identity)

- Stop targets the EXACT container id recorded in active/journal —
  never by name (a stale retry must not kill a replacement).
- Signal: `SIGTERM`, then `SIGKILL` after `stop_grace` (per-service
  manifest field, default `10s`, max `5m`; game servers SHOULD set
  higher). Stream drain: proxy stops routing NEW streams at shift;
  existing L4 flows drain up to `drain_timeout` (installation traffic
  config), then force-close. UDP sessions: the single BusyBox `nc -l -u`
  fixture ignored SIGTERM (`docker stop -t 5` waited the full 5.1s) —
  that fixture's behavior, NOT proof that graceful app shutdown is
  impossible. FROZEN as PRODUCT POLICY (independent of the fixture):
  UDP/game replacement is DECLARED interrupting; deploy output states
  interruption, never zero-downtime.
- Volume-owning replacement: after the new container may have
  written, the OLD image is never auto-restarted (data-format risk).
  Operator recovers via explicit deploy of a chosen revision.

## 7. Partial-failure policy (frozen proposal)

- Services are independent units: failure in service X marks its
  remaining steps `not-run`, records `failed` with the exact error,
  and CONTINUES with the next service (sorted order). Rationale:
  a broken worker must not block a security fix to `web`.
- Traffic commit covers ONLY services whose replace steps succeeded.
  Failed services keep old active backends (web) or stay stopped on
  old definition (initial deploy).
- Op outcome: `success` (all steps succeeded), `partial` (≥1
  succeeded AND ≥1 failed), `failed` (preflight failed or zero
  service steps succeeded).
- Deploy output (text + JSON, no secrets): captured `REV`, `OP`,
  per-step planned/succeeded/failed/not-run with identities
  (container ids, digests, networks), resulting effective vs observed
  state, retained volumes/secrets list. Exit code: `0` success,
  `2` partial, `1` failed (exact codes frozen here; CLI maps them).
- Retry of a partial op uses a NEW op id, re-captures the revision,
  and re-observes runtime — never resumes step numbers blindly.

## 8. Restart authority (extends D2/D5 — senior-review mandate)

Current actors conflict: container service sets
`RestartPolicyAlways` (verified `service.go:289,3055`), monitor
restarts crashes/unhealthy (`monitor.go`), startup recovery is a
third actor. Frozen resolution:

1. **Gordon is the sole restart authority for app containers.**
   Engine-created app containers use `RestartPolicyNo` (engine NOT
   started) at the runtime layer. Restart semantics live in ONE place:
   the monitor, driven by durable intent.
2. **Monitor rules** (replaces current exit-code inference):
   - intent `stopped:true` → ensure stopped; if observed running
     (engine restart while Gordon absent, unauthorized start), stop
     the exact container and report `unauthorized-start-stopped`.
     Reconciliation-after-start is NOT sufficient; the container is
     actively stopped.
   - intent running + container exited/crashed → restart with
     backoff (existing `restartRecord` mechanism retained), bounded
     restarts then `crashed` state + diagnostic (no infinite loop).
   - intent running + container missing (reboot, daemon loss) →
     recreate from ACTIVE pinned digest (never desired, never
     re-resolved tag).
   - in-flight op containers (journal non-terminal, id matches) →
     monitor does NOT touch them; the op owner decides.
3. **Daemon shutdown/boot ordering**: on Gordon shutdown, monitor
   stops issuing restarts; running containers keep running (engine
   policy `no` means engine won't restart them either — a host reboot
   then relies on Gordon boot recovery from active+intent).
   On boot: load active+intent BEFORE any traffic staging; start
   missing running-intent containers; enforce stopped intent; THEN
   stage traffic.
4. **Crash between stop-intent persist and container stop**: boot
   recovery observes intent `stopped:true` + running container →
   stops exact id, reports `recovered-stop` (see 02-state.md matrix).

## 9. Non-negotiable volume preservation (restated as engine rules)

- No engine step calls `RemoveVolume`. No container removal passes
  volume-deletion flags (anonymous volumes included).
- `PruneVolumes` / image prune paths fail closed on ANY app-owned or
  unknown resource until ownership-aware protection lands (cutover
  prerequisite; see `04-network.md` §8 and senior-review ordering:
  P6.3 BEFORE engine activation).
- Upgrade acceptance (seeded populated named+anonymous volumes,
  checksums, update/reject/reload/recovery/failure injection,
  assert-everything-preserved) runs BEFORE release, alongside
  fresh-install tests. This contract does not weaken it.

## Decision required (D2 — maintainer acceptance)

1. Partial-failure `continue-with-next-service` policy — accept, or
   fail-fast whole-op alternative?
2. Exit codes `0/2/1` for success/partial/failed — accept?
3. `RestartPolicyNo` + Gordon-monitor-as-sole-authority — accept?
   (Alternative: engine `unless-stopped` + intent sync; NOT
   recommended — engine restarts bypass intent during Gordon absence.)
4. `stop_grace` default `10s`, max `5m` — accept?
5. Never-auto-restart-old-volume-owning-image rule — accept as
   absolute, or allow operator override flag?
