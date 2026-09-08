# 04 — Network & Traffic Contract (D3 + RCON policy)

Status: draft for maintainer acceptance. Freezes the single routing
projection: how active app definitions become reachable, reservation
overlap rules, backend reachability for Docker and rootless Podman,
CIDR/trust semantics, and the RCON private-default policy. P3
implements exactly this as a replacement of the #242 traffic design
(#242 tip preserved; reconstruction only after acceptance).

## 1. One traffic engine, one coordinated snapshot

- The V2 host traffic engine is retained. App route projections MERGE
  with global external routes and system routes (registry, Gordon
  domain) under ONE coordinated snapshot. The plan's "one traffic
  engine validates installation and app route conflicts together" is
  implemented as: conflict validation runs over the merged candidate
  (active app projections + global config) BEFORE persistence of any
  apply that changes reservations, and again at deploy preflight.
- No route publication from PENDING desired config. The traffic
  candidate is: ACTIVE per-service definitions PLUS the journaled,
  ready, operation-owned service transition (REVIEW FIX round 2,
  HIGH-2 — the previous "only active.json projects" rule made the
  frozen commit-before-publish sequence unimplementable). The op-owned
  overlay is EXPLICITLY distinguished from pending desired: it exists
  only inside a non-terminal journal op whose replacement passed
  readiness, carries the op id, and dies with the op (terminal op →
  overlay resolved into active.publish or discarded with the
  replacement removed). Ordinary reprojection (monitor ticks, unrelated
  app commits, terminal-failure handling) MUST NOT discard an
  engaged overlay before its owning op resolves it.
- Commit/publication recovery: traffic-commit-succeeded +
  active.publish-failed → the op journal records `traffic-committed`
  with the committed snapshot id; recovery replays ONLY
  active.publish (+ retire) from the journaled transition, NEVER
  re-projects from active (which would undo the shift). Conversely
  active.publish MUST NOT precede its service's traffic commit.
- Snapshot ownership: each traffic commit carries the op id +
  snapshot id. Concurrent commits race on snapshot id; the loser
  re-reads, re-projects, and retries (bounded, 3 attempts) or fails
  its op step with `traffic-snapshot-conflict`. Cross-app concurrent
  updates MUST NOT overwrite each other's routes.
- Staging retains the existing backend staging/validation ideas
  (`StageServiceTargets`/`CommitStagedServiceTargets` pattern) but
  with OPERATION identity instead of unowned global staging slots.

## 2. Projection: from active definition to snapshot entries

For each service in each active app definition:

| Manifest interface | Snapshot entry |
|---|---|
| `[[service.http]]` | HTTP router: `host` → backend `container-ip:port`, TLS mode per `tls` field + installation coverage |
| `[[service.tcp]]` | TCP router on `publish` bind → backend `container-ip:port` |
| `[[service.udp]]` | UDP router on `publish` bind → backend `container-ip:port` |
| `[[service.rcon]]` | TCP router on `publish` bind → backend, WITH policy tag `rcon` (§6) |

- Backend address = container's IP on the app's private network
  (Docker) or the equivalent Podman-routable address (§4). The proxy
  dials backends over the private network; backends are NEVER
  published on public binds except through declared `publish` binds.
- Withdrawal: removing one route withdraws ONLY its entries; the
  commit preserves all unrelated traffic (snapshot merge, not
  full-replace-then-re-add — or replace with diff-verified equality
  for unrelated entries; implementation proves preservation with the
  shared-listener removal test in P3.3).
- Release requires VERIFIED withdrawal (backend no longer in the
  committed snapshot AND bounded flow drain per installation
  `drain_timeout`), not just desired-route deletion or an optimistic
  ACK.

## 3. Reservation overlap rules (frozen)

Reservations live in the global table (`02-state.md` §5). Overlap
detection (validated at apply AND deploy preflight):

1. TCP and UDP are separate namespaces: `0.0.0.0:25565/tcp` and
   `0.0.0.0:25565/udp` coexist.
2. Within one proto: wildcard (`0.0.0.0`, `::`) vs specific-IP on the
   same port conflict — UNLESS the specific IP is covered by only one
   side and the implementation can prove disjoint binds (P3.2 proves
   per-platform behavior; default = conflict).
3. Same `ip:port:proto` twice within one candidate (two services,
   same or different apps) = conflict, including two `[[service.tcp]]`
   entries in the SAME app.
4. HTTP: same canonical host twice = conflict (covers system domains,
   external routes, and all apps installation-wide).
5. IPv4/IPv6: one `publish = "port"` (no IP) claims BOTH families as
   a single `dual` entry where the platform supports it; where the
   platform lacks IPv6 (P0.2: rootless Docker without IPv6), the entry
   degrades to IPv4-only and records `dual: false` (no silent promise
   of IPv6 reachability).
6. Same-app transition: old ACTIVE reservations persist until actual
   withdrawal (§1, `02-state.md` §5) — an app re-applying its own
   ports never self-conflicts.
7. Gordon-generated backend binds (if required, §4) are reserved with
   `owner: "gordon-backend"` and never collide with user `publish`
   binds (loopback range, verified free at generation).

## 4. Backend reachability (Docker + rootless Podman)

- Docker host-process reachability: HYPOTHESIS, not verified — P0.2
  proved sibling-container DNS/HTTP + cross-network isolation, but
  NO host-process → container-IP dial was recorded (review catch).
  P3 fixtures MUST prove host→backend dials on Docker AND Podman
  before freezing adapter dial behavior.
- Post-creation `network connect --alias` did NOT propagate the alias
  in the single probed variant (endpoint `Aliases` `None`, NXDOMAIN).
  The full-alias second-connect variant was NOT probed — HYPOTHESIS
  that re-attach fixes it. P3 fixtures MUST prove both variants
  (alias-at-create path the engine uses + disconnect/reconnect WITH
  full alias set) before freezing §4 consequences. Until proven, the
  frozen rule stays conservative: aliases are set at creation; any
  alias change recreates the container (not just re-attaches).
- Podman: NO local Podman exists on this machine (verified P0.2 —
  `podman: command not found`); P0.2 Podman evidence is PENDING and
  recorded as a gap in `07-runtime-evidence.md`. The contract REQUIRES
  before P3 implementation: rootless-Podman private-backend dial test
  (host → container IP), alias-at-create behavior, and pasta-network
  reachability notes. Runtime-specific mechanisms live in adapters,
  never in manifests. If Podman cannot meet the boundary, the
  contract — not the test — is renegotiated.
- Internal backend publication (host-visible backend ports), IF
  required for a runtime that cannot dial container IPs directly:
  loopback-only (`127.0.0.1`), Gordon-generated ephemeral ports,
  reserved in the table, visually distinguishable (`owner:
  "gordon-backend"`), never user-requestable. No such requirement is
  established for Docker; Podman evidence decides.

## 5. CIDR / trusted-proxy semantics (preserved, extended with care)

> REVIEW FIX (senior round 1, MEDIUM-10): the previous draft said
exposure is "governed" by installation policy without saying HOW
a generated listener attaches to it, while V2 RCON validation
resolves a NAMED entrypoint and requires EXACT CIDR-set match
(`builder.go:402-415`: entrypoint must exist, both sides non-empty,
`sameCIDRSet`). Frozen listener-policy resolution:

- Existing middleware semantics are UNTOUCHED: proxy allowlist checks
  the immediate peer; registry allowlist uses trusted-client
  extraction; Cloudflare header accepted ONLY with configured proxy
  trust AND known Cloudflare immediate peer. P3 keeps these distinct
  code paths; app config cannot rewire them.
- LISTENER-POLICY RESOLUTION (frozen): every `[[service.tcp]]`,
  `[[service.udp]]`, and `[[service.rcon]]` entry REQUIRES
  `entrypoint = "<name>"` naming an installation `entrypoints.<name>`
  (V2 host listeners). The generated listener INHERITS that
  entrypoint's `trusted_cidrs`, `raw_fallback` policy, and transport
  limits; a service CIDR set (`rcon.trusted_cidrs`) MUST be a SUBSET
  of (or exactly equal to, for RCON per V2 `sameCIDRSet`) the
  entrypoint's set — a service set that exceeds the entrypoint is a
  validation error (app never relaxes installation). Unknown
  entrypoint name = validation error. HTTP `[[service.http]]`
  entries are governed by the installation HTTP proxy allowlists
  (`proxy_allowed_ips` + trusted-proxy chain), NOT by entrypoint
  CIDRs — the Host-router path keeps existing middleware semantics.
  Registry allowlists govern registry traffic only and never app L4.
- Peer-identity and UDP-forwarding proofs are REQUIRED before any
  future route-CIDR control is even proposed (plan D3): observed
  source-IP behavior behind the proxy must be demonstrated per
  runtime through the ACTUAL proxy, not assumed and not via direct
  dials. P0.2 did NOT probe end-to-end proxy source identity
  (recorded OPEN in 07 §5); P3 fixtures must. UDP source identity
  remains OPEN (see `07`).

## 6. RCON policy (extends D3/D6 — senior-review mandate)

Verified current behavior (`traffic/builder.go:399` + builder tests):
a port named `rcon` (case-insensitive) OR marked `Private` defaults
private; explicit public requires an override; CIDR-gated variants
exist. Deleting standalone port policy MUST NOT silently remove this
protection. Frozen replacement:

1. `rcon` is an explicit interface kind in the manifest (NOT a naming
   convention, NOT a new transport — TCP wire behavior).
2. Default = private (loopback publish or CIDR-gated). Public
   requires BOTH `public = true` AND `trusted_cidrs = [...]`
   (non-empty, valid CIDRs). Either alone = validation error.
3. Until this replacement is accepted AND implemented, any publication
   path that cannot preserve the private default REFUSES the config
   (fail-closed; no silent public RCON).
4. Tests (P3.3): omitted policy stays private; explicit public
   override without CIDRs rejected; CIDR denial enforced against a
   real peer; generated-backend bypass impossible (backend binds are
   loopback-only and policy-tagged).

## 7. Bind failure & external occupation

- Apply reports `observed-occupied` (loopback dial succeeded) or
  `occupancy-unknown` per reservation; this is ADVISORY, never a
  lock claim.
- Deploy rechecks at preflight; REAL bind failure at shift/commit
  fails the step with `bind-failed`, retains the app's prior active
  reservations, keeps displaced NOTHING (old backends keep serving
  where possible), and reports the conflicting occupant to the
  extent reliably observable.
- Withdrawal of one route preserves unrelated traffic (§1); bind
  failure of one service does not withdraw siblings (partial policy,
  `03-deployment.md` §7).

## 8. Destructive-path prerequisites (senior-review ordering)

BEFORE the deploy engine activates (cutover gate), ownership-aware
protection MUST land on:

1. `volumes.Service.PruneVolumes` (prunes unmounted
   `gordon.managed=true` volumes — verified
   `internal/usecase/volumes/service.go:40-80`): until app-ownership
   aware, it DISABLES itself (returns explicit `prune-disabled`
   error) when ANY app state exists, OR filters to records proving
   non-app provenance. Unknown and old-V2 managed-label resources are
   NEVER implicitly eligible.
2. Image pruning (`PruneRuntime` dangling + `PruneRegistry`
   unreferenced + startup scheduler `startImagePruneScheduler`):
   MUST protect active definitions of RUNNING and STOPPED apps,
   in-flight op digests, and registry pinned indexes/manifests +
   all referenced manifests/configs/layers. Until then: scheduler
   disabled when app state exists; manual prune fails closed on
   pinned/unknown content.
3. Backup identity adaptation (`DetectDatabases`/`ListAttachments`
   coupling — verified `backup/service.go:57`) and required P5 API
   handlers land BEFORE cutover so deleting attachments in P4 never
   breaks retained backups mid-stack.

Prune-concurrency tests (prune vs deploy vs removal vs recovery) are
P6.3 exit criteria and cutover prerequisites.

## Decision required (D3 — maintainer acceptance)

1. Alias-at-create + CONTAINER RECREATION on alias change (§4) —
   accept? (Review fix: re-attach was unproven and is explicitly
   deferred to a later contract change with runtime evidence.)
2. No route-local CIDR field beyond RCON in v2.50 (§5) — accept?
3. RCON as explicit kind with `public` + `trusted_cidrs` pair (§6) —
   accept?
4. Loopback-only generated backend binds as the fallback shape (§4) —
   accept as the ONLY permitted internal-publication shape?
5. P0.2 Podman gap: accept Docker-only P1–P2 start with Podman
   evidence required before P3, or block the whole stack now?
   (Recommending: proceed P1–P2, gate P3 on Podman evidence.)
