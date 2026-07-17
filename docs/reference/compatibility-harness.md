# Compatibility harness

The old/new harness compares `origin/main` (the baseline) with the current checkout. Override the baseline only when needed:

```bash
GORDON_COMPAT_BASELINE_REF=<branch-tag-or-commit> make compat-harness-config
```

## Executable slices

Run the CI slices exactly as follows:

```bash
make compat-harness-config  # TestCompatibilityConfigShowJSON
make compat-harness-cli     # TestCompatibilityRoutesListJSON
make compat-harness-api     # Docker preflight + TestCompatibilityAdminAuthAndRouteCRUD
make compat-harness-proxy   # Docker preflight + managed, external, and zero-downtime routes
```

The executable real old/new targets set `GORDON_COMPAT_RUN_REAL=1` themselves, so they cannot pass by skipping; API and proxy also set `GORDON_COMPAT_REQUIRE_RUNTIME=1` and run `docker info`, making Docker a hard CI/local requirement. The proxy target runs its three real tests in one Go test build/invocation, parses its JSON output, and requires each exact top-level test to pass once with no skip. Ordinary `go test ./...` leaves real scenarios gated off, so it needs neither a baseline checkout nor a container runtime.

The three Docker scenarios use Docker's compatible CLI intentionally. Their `PodmanRequired=false` means *not specifically Podman*; it does not mean no runtime is required. Pending Podman e2e coverage remains opt-in through `GORDON_COMPAT_PODMAN=1` and requires `podman info`.

The Make targets derive `COMPAT_ARTIFACT_DIR` from `GORDON_COMPAT_ARTIFACT_DIR` when set, otherwise retain reports under the ignored repository-root `artifacts/compat` directory. Relative `GORDON_COMPAT_ARTIFACT_DIR` values are resolved from the repository root; absolute values are used unchanged. An explicit `COMPAT_ARTIFACT_DIR=...` make variable still takes precedence. Each target deterministically overwrites its expected files, then prints the baseline ref, report path, and exact focused rerun command. Every slice writes private (`0600`) diagnostic files:

- `compat-report.json` and `normalized.diff`
- `old.raw.json`, `new.raw.json`, `old.normalized.json`, and `new.normalized.json`

The harness redacts tokens, authorization credentials, and secret-bearing metadata before writing artifacts, including recursively embedded JSON diagnostics. A configured baseline is compared normally: a behavior difference against a custom ref (including the pre-fix API delete behavior) is reported rather than hidden or allowlisted.

Focused reruns are:

```bash
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityConfigShowJSON$' -count=1
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRoutesListJSON$' -count=1
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityAdminAuthAndRouteCRUD$' -count=1
GORDON_COMPAT_ARTIFACT_DIR=artifacts/compat GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^(TestCompatibilityManagedHTTPRoute|TestCompatibilityExternalRoute|TestCompatibilityZeroDowntimeDrain)$' -count=1
```

The proxy reports are `artifacts/compat/proxy/`, `artifacts/compat/proxy-external/`, and `artifacts/compat/proxy-zero-drain/` (or beneath `<COMPAT_ARTIFACT_DIR>`), each with `compat-report.json` and diagnostic side files. The external route uses a run-unique exact-owned Docker CGNAT network. The drain route holds an old response open, deploys a distinct replacement, confirms new traffic, and only then releases the old response; it uses an exact-owned marker volume. Harness resources are labeled and removed on completion, including failure paths; inspect Docker only by printed run-specific labels while debugging.

## Scenario and fixture policy

Exactly eight scenario names are implemented: `cli/config-show-json`, `cli/routes-list-json`, `api/auth-missing-invalid`, `api/route-list-detail`, `api/route-add-update-remove`, `proxy/managed-http-route`, `proxy/external-route`, and `proxy/zero-downtime-drain`. Remaining proxy shells are pending: `proxy/unknown-host`, `proxy/h2c-backend`, `proxy/registry-domain-routing`, `proxy/body-size-limit`, and `proxy/access-log-emitted`. All other shells, including migration and security, are also pending. Pending scenarios are not coverage: selecting one fails, and policy guards keep them from silently becoming passing work.

When adding a fixture:

1. Use generic config, domains, credentials, and paths.
2. Declare every compatibility surface it exercises.
3. Isolate old and new data/home directories.
4. Mark Podman requirements and provide an actionable pending reason until real execution exists.
5. Add an exact rerun command and verify the report plus all four raw/normalized side files.
