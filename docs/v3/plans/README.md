# V3-alpha implementation plans

Status: complete alpha roadmap aligned with ADR-003; detailed contract/proof tasks remain before dependent implementation

Date: 2026-09-06

## Start here

This set covers **all Alpha 1–5 work now**, split into bounded plans. It does not select mechanisms deliberately left open by the ADRs. Decision tasks produce reviewed specifications and evidence; dependent implementation tasks stop until those outputs are accepted. A complete roadmap is not blanket implementation approval.

Read [design](../design.md), [ADR-001](../adr-001-v3-foundation.md), [ADR-002](../adr-002-host-ingress.md), [ADR-003](../adr-003-alpha-scope-and-trust.md), and repository `AGENTS.md` first. ADR-003 supersedes conflicting historical requirements. The [multi-host note](../multi-host-evolution.md) is an evolution constraint, not cluster scope.

| Order | Plan | Outcome | Entry gate |
| --- | --- | --- | --- |
| 1 | [Alpha 1A: foundation decisions and proofs](alpha-1a-foundation-proofs.md) | Accepted confinement, capability, transport and installation contracts backed by reference-host evidence | Existing ADRs; authorized disposable test host |
| 2 | [Alpha 1B: installable foundation](alpha-1b-installation.md) | One verified distribution with the validated topology; recoverable fresh installation | A1A.0 resolved and applicable Alpha 1A contracts/proofs accepted |
| 3 | [Alpha 2: first web app](alpha-2-web-app.md) | Declarative apply, digest-pinned deploy, minimal release, stop and durable recovery | Alpha 1 complete; Alpha 2 decision gate accepted |
| 4 | [Alpha 3: multi-service security](alpha-3-multi-service.md) | Encrypted bbolt secrets, service volumes, private networks and safe partial failure | Alpha 2 complete; Alpha 3 contracts accepted |
| 5 | [Alpha 4: registry and rollback](alpha-4-registry-rollback.md) | Secure public OCI, bounded push events, full and service rollback | Alpha 3 complete; certificate and event contracts accepted |
| 6 | [Alpha 5: routes and lifecycle](alpha-5-routes-lifecycle.md) | TCP/UDP exposure, full lifecycle and automatic web overlap with bounded shutdown | Alpha 4 complete; rollout ordering/recovery contract accepted |

Alpha 1A experiments are not a shipped alpha and must not claim installer readiness. Every shipped milestone must install on a clean reference host. Public UDP app routes arrive in Alpha 5; transport-level UDP correctness is proven and integrated in Alpha 1, not deferred to the first game deployment.

## Native-network checkpoint before ingress

Maintainer direction, 2026-09-06: **run A1A.0 in the [foundation proof plan](alpha-1a-foundation-proofs.md) before implementing host ingress**. Retest pasta, distinguishing previously tested direct networking from native pasta/Pesto publication on named rootless bridges. If the reference-host proof meets the required behavior and isolation, skip the Gordon ingress role completely: amend the ADRs and remove its tasks from all stages, rather than shipping both paths. Five-role descriptions below remain the existing baseline, not an instruction to implement ingress before this checkpoint. Native functionality and acceptable port-change interruption have not yet been established.

## Baseline and handoff

- Repository: `/home/brice/Projects/gordon`, module `github.com/bnema/gordon`.
- Inspected integration base: `origin/v3-alpha`, `93e3a9dd0fe13c116d78493e4427a54c425f57a9` (includes image-label policy, PR #254).
- Planning branch: `docs/v3-alpha-implementation-plans`; worktree created under configured `.worktrees/`.
- User requested the entire plan set be saved in the repository on 2026-09-06. This authorizes these documents, not unresolved architecture decisions or deployments.
- No source changes are required to consume the plans. Executors must obtain the eventual merged planning commit, then branch from the latest `origin/v3-alpha`. Do not execute from an unpublished local copy without carrying these documents over explicitly.
- Before each task, inspect current symbols, predecessor commits and applicable ADRs. On drift, update the task rather than resetting files or following obsolete line numbers.

### Inspected integration anchors

Paths below are relative to the repository root. Existing code is v2 unless stated otherwise.

| Existing path/symbol | Planning use; not a v3 behavior endorsement |
| --- | --- |
| `main.go`, `cli.NewRootCmd()` | Executable entry; one binary must dispatch the future roles |
| `internal/adapters/in/cli/root.go`, `newServeCmd` in `serve.go` | Current legacy command registration and serve dispatch to replace |
| `internal/app/run.go`: `Run`, `createServicesWithOptions`, `createOutputAdapters` | Monolithic composition to retire, not split mechanically |
| `internal/app/kernel.go`: `NewKernel`, `NewKernelQuiet` | Local CLI currently constructs services; v3 local admin must instead use control's socket |
| `internal/adapters/in/cli/controlplane*.go`, `remote/` | Testable dispatch/output patterns; do not retain v2 HTTP/token or local-kernel authority |
| `internal/adapters/out/docker/runtime.go` | Existing broad runtime surface; not the v3 Podman contract. Label, port, volume and environment inference are particularly unsuitable |
| `internal/domain/labels.go`, `internal/boundaries/{in,out}/` | Existing layering and ownership anchors; v3 ownership must not trust inherited image labels |
| `internal/usecase/{container,config,proxy,registry,secrets,volumes}/` | Inspect individual reusable helpers/tests only; route-owned workloads are obsolete |
| `Dockerfile`, `install.sh`, `.goreleaser.yaml`, `pkg/version/` | Current v2 image/bootstrap/version flow, not an identical-binary distribution implementation |
| `.github/workflows/ci.yml`, `.golangci.yml`, `.mockery.yaml` | Existing checks/generation; CI filters currently omit installer/Containerfile-only changes |
| `dev/v3/cmd/sandbox/`, `dev/v3/cmd/l4probe/`, `dev/v3/README.md` | Existing disposable-host tooling and manual proof context |
| `dev/v3/fixtures/{app-web-test,app-game-test}/` | Existing independent Go modules and illustrative manifests; not working Gordon v3 deployments |

New paths named in stage plans are **proposed work locations**, not existing APIs. Keep domain and use cases adapter-independent. Interfaces belong at consumer boundaries; transport DTOs belong in adapters. Do not create empty stage packages in advance. Exact consequential contracts must be accepted at their gate before code or generated mocks are written.

## Shared constraints

1. Fresh install, one host account/rootless Podman engine, no Docker/rootful runtime, cluster, v2 migration or compatibility layer. Never import the archived `v3-deprecated` system wholesale.
2. One host executable and one component image containing the exact executable; five roles, four independent containers and confined host ingress. No shared pod, host network/PID/IPC/devices, privilege escalation or extra capabilities. Preserve LSM/seccomp and read-only roots where practical.
3. Only runtime receives Podman. Runtime's full-engine authority is an accepted risk, not strong isolation. Only its narrow edge ingress-network reconciliation may mutate a Gordon component resource.
4. Control owns desired state/listener authorization and bbolt control state; runtime owns actual state and separately encrypted bbolt secrets; edge owns app/public-registry routing/TLS; registry owns OCI/authentication/outbox; conditional ingress owns opaque host transport. No host descriptors reach edge. No internal TCP API, gRPC, protobuf or component bearer-token scheme. Edge traffic/credential compromise is accepted, not direct private-store or administration authority.
5. App manifest is the configuration source. Apply has no runtime effects; full deploy alone activates pending configuration. Runtime receives pinned digests. Reject `latest`. Image labels have no configuration or display effect; inherited labels grant no management authority.
6. Releases are immutable; execution intent is separate and durable. Recovery must not resolve a new tag, activate pending configuration, revive stopped apps or blindly replay effects.
7. Reservations cover desired, active and in-flight state. Shared-route withdrawal is edge-owned; dedicated/final-listener withdrawal also needs ingress cleanup. Uncertain withdrawal retains reservations.
8. Secrets are service-owned/write-only, public environment is app-wide, collisions fail. Volumes are named/service-owned, never host binds/shared service volumes. Rollback cannot restore old secret values or undo writes to volumes.
9. UDP recreate invalidates per-listener epochs before backend mutation. Sessions are bounded and disposable, never recovered. Ingress restart interrupts TCP and loses UDP; no transparent recovery promise.
10. Firewall/sysctls and privileged host prerequisites belong to the administrator; Gordon performs no privileged setup, system-account creation or system-service installation. Public access requires applicable containment/TLS proofs. Native CrowdSec is post-alpha, not assumed protection. Web overlap uses structural HTTP-only/no-volume eligibility and application-owned concurrency safety, with finite per-service stop_timeout defaulting to 30s.

## Approved choices, remaining contracts and proofs

ADR-003 fixes bbolt control state, encrypted runtime bbolt secrets with a separate key, trusted-but-fallible edge including registry TLS, private-by-default registry publication, automatic HTTP-only/no-volume overlap, `stop_timeout = "30s"` by default, entirely rootless setup, pasta-first testing and post-alpha CrowdSec. These are not open product questions.

The table below separates contract work from proof gates. Storage schemas, DTOs, atomicity, deadlines and concrete error handling refine approved behavior; they do not reopen the storage engine or demand a new product choice for every field. TLS modes/issuance/origin trust and the validated network topology still need decisions. Propose bounded contracts, ask material unresolved questions one at a time, and record consequential choices in focused ADRs with a critical review. Supply test vectors/evidence; do not describe an unrun proof as acceptance.

| Gate | Owner task | Required output before implementation |
| --- | --- | --- |
| N0 | A1A.0 | Native networking proof; success removes host ingress and all its dedicated tasks |
| F1 | A1A.1–2 | Applicable mounts/socket/peer/startup contract; rootless confinement proof only if ingress retained |
| F2 | A1A.3–5 | TCP/UDP IPC, replay/epoch rules, listener persistence/recovery, public/local mapping, network/source-IP and private-pull proof |
| F3 | A1A.6 | Distribution/install configuration, identity and journal contract; exact host command syntax and Podman API subset |
| W1 | A2.1 | bbolt schema/transactions, operation serialization, reservations, edge snapshots, shutdown/reboot ownership and initial HTTP/TLS contracts |
| S1 | A3.1 | Shared-network syntax, encrypted bbolt record/key/recovery/injection and volume ownership contracts |
| R1 | A4.1 | Explicit public registry TLS/origin trust/authentication and bounded push-event contracts; no edge-impersonation gate |
| L1 | A5.1 | Multi-entrypoint acceptance, switch/signal/stop_timeout/stream cleanup and recovery; no concurrency-safety classifier |

F1/F2 are iterative: a failed experiment may require revising a candidate contract and repeating tests. They are not permission to ship an insecure fallback. A dependency may proceed only for the accepted portion; the overall stage cannot pass with a missing gate.

## Checks and evidence

Run from the feature worktree root unless stated otherwise. These IDs apply to every plan.

| ID | Command or procedure | Expected result |
| --- | --- | --- |
| C1 | `git diff --check`; verify changed Markdown fences and relative links | No whitespace errors, unbalanced fences or broken local links |
| C2 | `go build ./...` | All root-module packages build |
| C3 | `go test ./...` | All root-module tests pass; failures classified, never deleted to pass |
| C4 | `go test ./... -race` | No races; required at stage exit when practical, unavailable runs explicitly block a claim of race verification |
| C5 | `golangci-lint run ./...` | Pass before every code commit; do not widen inherited suppressions to hide new boundary problems |
| C6 | `mockery`, then C2/C3/C5 | Regenerate after boundary changes; generated diff limited to intended interfaces |
| C7 | `go test ./dev/v3/cmd/sandbox/... ./dev/v3/cmd/l4probe/...` | Tooling tests pass; absent tests are not evidence of transport correctness |
| C8 | Authorized clean Ubuntu 26.04 LTS VM, Go 1.27, rootless Podman and systemd/Quadlet; run the stage's new acceptance tests | Record host/package versions, clean revision, identity, commands, assertions and sanitized results |
| C9 | In each changed `dev/v3/fixtures/*` module: `go test ./...` and `go build ./...` | Fixture modules build/test independently; root `./...` does not cover nested modules |
| C10 | New narrow package tests with `go test ./<created-package>/...`; select exact test names once created | Each task's success, refusal and interruption assertions pass before full checks |

C8 is a procedure until its harness exists, not a fictional runnable command. A1A.1 creates the harness and records exact invocations in `dev/v3/README.md`; each later task extends that documented entry point. Never report a VM assertion as passed because its unit-test fake passed. Use test-only failure injection at persisted phase/effect boundaries, not sleeps or a production arbitrary-command API.

For every task record in its PR: task IDs, gate/ADR references, changed paths, test commands/results, proof artifact location, unresolved findings, and interruption resume point. Store sanitized reference-host reports under proposed `dev/v3/proofs/`; no credentials, private keys or bulk OCI payloads. Missing security/recovery evidence keeps the task open.

### Observed planning baseline

- `go version`: `go1.27.1-X:nodwarf5 linux/amd64`.
- `golangci-lint` is available at `/home/brice/go/bin/golangci-lint`; lint was not run for this documentation-only work.
- `go test ./dev/v3/cmd/sandbox/... ./dev/v3/cmd/l4probe/... ./pkg/version/...`: passed; sandbox has tests, l4probe and version reported no test files.
- No full build/test/race run, VM execution, installer run, ingress proof or deployment was performed during planning. Prior experiments are described in ADR-002 and the dev README, not upgraded to current implementation evidence.

## PR and rollout rules

Each checkbox is a bounded outcome, normally one or a few buildable PRs. Split larger tasks by owning boundary without activating a half-implemented behavior. New branches/worktrees start at latest `origin/v3-alpha`; signed conventional commits, verified signatures, Draft PRs target `v3-alpha`, never `main`. Apply repository review requirements and resolve critical findings before implementation of consequential decisions. These plans themselves do not authorize commits, publication or infrastructure mutation.

Within a code transition, inactive v2 packages may remain temporarily to keep intermediate commits buildable. The v3 executable must not dispatch to them. Delete obsolete code/tests/dependencies with ownership-aware replacement coverage, not by rewriting expected failures as success. No dual-mode v2/v3 product or migration harness.

Installation testing is restricted to explicitly authorized disposable hosts. Preserve unknown edits, volumes and credentials. Same-generation incomplete-install recovery is supported; replacing an installed distribution, component update/rollback and `gordon update` remain unavailable until a separate lifecycle ADR and implementation outside this alpha plan. Never use reinstalling a newer alpha as an implicit upgrade test.

## Scope completion matrix

| Accepted outcome | Planned tasks |
| --- | --- |
| Native-network retest, applicable ingress isolation/IPC, client policy, private pulls | A1A.0–5; A1B.2, A1B.5–6 |
| One distribution, source installer, five-role readiness and recovery | A1A.6; A1B.1–6 |
| V2 command/bootstrap removal, SSH administration, CLI inspection | A1B.1, A1B.5; A2.5; A3.5; A4.4; A5.5 |
| Apply/deploy, immutable releases, ignored image labels, stopped intent | A2.1–6 |
| Secrets, volumes, networks, safe multi-service failure | A3.1–6 |
| Public OCI, auto-deploy, full/service rollback | A4.1–6 |
| TCP/UDP routes, stop/restart/remove/purge, web replacement | A5.1–6 |
| Documentation, clean install/reboot/failure checks per stage | Final task of each stage and C1–C10 |

Retain the design's v2 feature matrix: attachments/autoroute/bootstrap/pin/reload/partial routes/token printing removed; backups/previews/CA inspection/traffic diagnostics/GC deferred. No app-level image shorthand, `env_file`, generic readiness engine, concurrency declaration/classifier, image-label inference, native CrowdSec, public control API, cluster machinery or update channel is added by these plans.

## Related

- [Accepted design](../design.md)
- [Foundation ADR](../adr-001-v3-foundation.md)
- [Host-ingress ADR](../adr-002-host-ingress.md)
- [Development host guide](../../../dev/v3/README.md)
