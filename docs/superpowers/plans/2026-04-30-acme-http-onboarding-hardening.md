# ACME HTTP Onboarding Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan phase-by-phase. Each phase contains sub-tasks with checkbox steps (`- [ ]`) for tracking. Review gates happen at the end of each phase, not after every sub-task.

**Goal:** Make Gordon's public HTTP/TLS behavior safe and practical for ACME HTTP-01, Cloudflare Full/Strict origin HTTPS, and internal CA onboarding without stealing application paths.

**Architecture:** Keep ACME HTTP-01 as the highest-priority public HTTP route. Move CA onboarding resources into a Gordon-owned `/.well-known/gordon/*` namespace, keep legacy direct-HTTP onboarding paths for compatibility, and gate HTTPS onboarding to `server.gordon_domain` so app hosts can own `/ca`. Delay runtime ACME reconciliation until listeners are bound. Treat Cloudflare Flexible HTTP-origin mode as legacy/compat; the target mode is Cloudflare Full/Strict with Cloudflare connecting to Gordon over HTTPS and validating a Gordon-served certificate. Limit ACME obtains per reconcile run so enabling ACME on an existing multi-route host does not burst through every route and risk Let's Encrypt rate limits.

**Tech Stack:** Go 1.25, `net/http`, Cobra CLI, Gordon hexagonal app/usecase/adapters, testify tests.

---

## Phase 1: Failing behavior tests

**Goal:** Codify desired HTTP/onboarding routing and ACME startup timing before production edits.

**Files:**
- Modify: `internal/app/run_http_handlers_test.go`
- Modify/Create: `internal/app/public_tls_startup_test.go` if helper isolation is needed

### Sub-task 1: Well-known HTTP onboarding paths

- [ ] Add tests showing direct HTTP serves `/.well-known/gordon/`, `/.well-known/gordon/ca.crt`, and `/.well-known/gordon/ca.mobileconfig`.
- [ ] Keep existing direct HTTP `/`, `/ca`, `/ca.crt`, `/ca.mobileconfig` compatibility tests green.
- [ ] Run `rtk go test ./internal/app/... -run 'TestCreateHTTPHandlers_DirectHTTP.*Gordon|TestCreateHTTPHandlers_DirectHTTP.*CA'` and confirm new tests fail before implementation.

### Sub-task 2: HTTPS host gating

- [ ] Add tests showing `httpsHandler` serves `/ca` only when `req.Host == cfg.Server.GordonDomain`.
- [ ] Add tests showing `/ca` on an arbitrary app host reaches the proxy path instead of onboarding.
- [ ] Run `rtk go test ./internal/app/... -run 'TestCreateHTTPHandlers_HTTPSOnboarding'` and confirm the arbitrary-host test fails before implementation.

### Sub-task 3: Delayed ACME runtime startup

- [ ] Add a small app-level test helper/fake proving runtime public TLS `Reconcile` and `StartRenewalLoop` happen after proxy readiness.
- [ ] Run the targeted test and confirm it fails with current service-construction-time reconciliation.

### Sub-task 4: ACME obtain batch limiting

- [ ] Add usecase tests proving one reconcile run obtains only a bounded number of missing certificates.
- [ ] Add a test proving a later reconcile run can continue with the remaining missing certificates.
- [ ] Run `rtk go test ./internal/usecase/publictls/... -run 'Batch|Limit|Reconcile'` and confirm the new batch-limit tests fail before implementation.

**Phase review checkpoint:** Confirm tests express product behavior, not implementation details.

---

## Phase 2: Implementation

**Goal:** Pass tests with minimal production changes.

**Files:**
- Modify: `internal/app/run.go`
- Modify: `internal/app/run_http_handlers_test.go`

### Sub-task 1: Onboarding route changes

- [ ] Add `/.well-known/gordon/` direct HTTP onboarding routes.
- [ ] Keep legacy direct HTTP `/`, `/ca`, `/ca.crt`, `/ca.mobileconfig` routes.
- [ ] Replace unconditional HTTPS `/ca*` registrations with a host-gated handler that only serves onboarding on `server.gordon_domain`.

### Sub-task 2: Delayed runtime ACME startup

- [ ] Change runtime service creation to initialize public TLS without immediate reconcile/renewal loop side effects.
- [ ] After registry, HTTP proxy, and TLS listeners are ready, call public TLS `Reconcile` and `StartRenewalLoop`.
- [ ] Preserve local CLI read-only behavior: `NewKernelQuiet` must still avoid reconcile/renewal loop side effects.

### Sub-task 3: ACME obtain batch limiting

- [ ] Add a conservative default obtain batch size for runtime reconciliation.
- [ ] Make `Service.Reconcile` process at most the configured number of missing targets per run.
- [ ] Log when targets remain pending so operators understand that issuance is intentionally staged.

### Sub-task 4: Compatibility check

- [ ] Verify ACME challenge handler is still registered before onboarding/proxy handlers.
- [ ] Verify trusted proxy traffic keeps existing behavior.
- [ ] Verify TLS-disabled HTTP behavior is unchanged.

**Phase review checkpoint:** Review for startup ordering, goroutine lifecycle, and route conflicts.

---

## Phase 3: Docs and verification

**Goal:** Document the operational model and validate the branch.

**Files:**
- Modify: `docs/config/server.md`
- Modify: `docs/concepts.md` if needed
- Modify: `docs/cli/tls.md` if status docs need wording
- Modify: `docs/config/server.md` to document Cloudflare Full/Strict origin HTTPS expectations

### Sub-task 1: Documentation

- [ ] Document that gray-cloud HTTP-01 requires public/NAT/firewall access to port 80.
- [ ] Document that Cloudflare-only IP allowlisting on port 80 is incompatible with gray-cloud HTTP-01; use DNS-01 or open 80 for direct validation.
- [ ] Document `/.well-known/gordon/*` CA onboarding paths and mark `/ca*` direct HTTP paths as legacy compatibility.
- [ ] Explain Cloudflare flexible HTTP origin as legacy/explicit, not preferred for end-to-end HTTPS.
- [ ] Document Cloudflare Full/Strict expected flow: browser -> Cloudflare HTTPS edge cert, Cloudflare -> Gordon HTTPS origin cert. Public ACME certs served by Gordon are the preferred origin certificate because Cloudflare Strict can validate them without a custom origin trust setup.

### Sub-task 2: Verification

- [ ] Run `rtk gofmt -w` on changed Go files.
- [ ] Run targeted app tests.
- [ ] Run `rtk go test ./...`.
- [ ] Run `rtk golangci-lint run ./...`.
- [ ] Request Go reviewer feedback and fix blocking issues.

**Phase review checkpoint:** Ensure docs match code and Igor findings.
