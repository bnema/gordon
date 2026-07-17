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
make compat-harness-proxy   # blocking Docker preflight + TestCompatibilityManagedHTTPRoute
```

The executable real old/new targets set `GORDON_COMPAT_RUN_REAL=1` themselves, so they cannot pass by skipping; API and proxy also set `GORDON_COMPAT_REQUIRE_RUNTIME=1` and run `docker info`, making Docker a hard CI/local requirement. The proxy target additionally rejects a skipped or absent `TestCompatibilityManagedHTTPRoute` result. Ordinary `go test ./...` leaves real scenarios gated off, so it needs neither a baseline checkout nor a container runtime.

The managed route uses Docker's compatible CLI intentionally. Its `PodmanRequired=false` means *not specifically Podman*; it does not mean no runtime is required. Future Podman e2e scenarios remain opt-in through `GORDON_COMPAT_PODMAN=1` and require `podman info`.

The Make targets derive `COMPAT_ARTIFACT_DIR` from `GORDON_COMPAT_ARTIFACT_DIR` when set, otherwise retain reports under the ignored repository-root `artifacts/compat` directory. Relative `GORDON_COMPAT_ARTIFACT_DIR` values are resolved from the repository root; absolute values are used unchanged. An explicit `COMPAT_ARTIFACT_DIR=...` make variable still takes precedence. Each target deterministically overwrites its expected files, then prints the baseline ref, report path, and exact focused rerun command. Every slice writes private (`0600`) diagnostic files:

- `compat-report.json` and `normalized.diff`
- `old.raw.json`, `new.raw.json`, `old.normalized.json`, and `new.normalized.json`

The harness redacts tokens, authorization credentials, and secret-bearing metadata before writing artifacts, including recursively embedded JSON diagnostics. A configured baseline is compared normally: a behavior difference against a custom ref (including the pre-fix API delete behavior) is reported rather than hidden or allowlisted.

Focused reruns are:

```bash
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityConfigShowJSON$' -count=1
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRoutesListJSON$' -count=1
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityAdminAuthAndRouteCRUD$' -count=1
GORDON_COMPAT_ARTIFACT_DIR=artifacts/compat GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityManagedHTTPRoute$' -count=1
```

The proxy report is `artifacts/compat/proxy/compat-report.json` (or `<COMPAT_ARTIFACT_DIR>/proxy/compat-report.json`), with the same diagnostic side files as the other slices. The harness labels and removes its managed route containers and temporary image on completion, including failure paths; inspect Docker only by the printed run-specific labels while debugging.

## Scenario and fixture policy

Exactly six scenario names are implemented: `cli/config-show-json`, `cli/routes-list-json`, `api/auth-missing-invalid`, `api/route-list-detail`, `api/route-add-update-remove`, and `proxy/managed-http-route`. The remaining proxy shells are pending: `proxy/unknown-host`, `proxy/external-route`, `proxy/h2c-backend`, `proxy/registry-domain-routing`, `proxy/body-size-limit`, `proxy/zero-downtime-drain`, and `proxy/access-log-emitted`. All other shells, including migration and security, are also pending. Pending scenarios are not coverage: selecting one fails, and the policy guards keep them from silently becoming passing work.

When adding a fixture:

1. Use generic config, domains, credentials, and paths.
2. Declare every compatibility surface it exercises.
3. Isolate old and new data/home directories.
4. Mark Podman requirements and provide an actionable pending reason until real execution exists.
5. Add an exact rerun command and verify the report plus all four raw/normalized side files.
