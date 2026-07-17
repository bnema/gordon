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
```

The real old/new scenarios require `GORDON_COMPAT_RUN_REAL=1`. The three Make targets set it themselves, so they cannot pass by skipping; the API target also sets `GORDON_COMPAT_REQUIRE_RUNTIME=1` and fails without Docker. Ordinary `go test ./...` leaves real scenarios gated off, so it needs neither a baseline checkout nor a container runtime. Podman-only fixture work remains opt-in with `GORDON_COMPAT_PODMAN=1`; it requires a working `podman info`.

The Make targets derive `COMPAT_ARTIFACT_DIR` from `GORDON_COMPAT_ARTIFACT_DIR` when set, otherwise retain reports under the ignored repository-root `artifacts/compat` directory. Relative `GORDON_COMPAT_ARTIFACT_DIR` values are resolved from the repository root; absolute values are used unchanged. An explicit `COMPAT_ARTIFACT_DIR=...` make variable still takes precedence. Each target deterministically overwrites its expected files, then prints the baseline ref, report path, and exact focused rerun command. Every slice writes private (`0600`) diagnostic files:

- `compat-report.json` and `normalized.diff`
- `old.raw.json`, `new.raw.json`, `old.normalized.json`, and `new.normalized.json`

The harness redacts tokens, authorization credentials, and secret-bearing metadata before writing artifacts, including recursively embedded JSON diagnostics. A configured baseline is compared normally: a behavior difference against a custom ref (including the pre-fix API delete behavior) is reported rather than hidden or allowlisted.

Focused reruns are:

```bash
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityConfigShowJSON$' -count=1
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRoutesListJSON$' -count=1
GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityAdminAuthAndRouteCRUD$' -count=1
```

## Scenario and fixture policy

Exactly five scenario names are implemented: `cli/config-show-json`, `cli/routes-list-json`, `api/auth-missing-invalid`, `api/route-list-detail`, and `api/route-add-update-remove`. Every other scenario shell is `pending`, including all migration and security shells. Pending scenarios are not coverage: selecting one fails, and the policy guards keep them from silently becoming passing work.

When adding a fixture:

1. Use generic config, domains, credentials, and paths.
2. Declare every compatibility surface it exercises.
3. Isolate old and new data/home directories.
4. Mark Podman requirements and provide an actionable pending reason until real execution exists.
5. Add an exact rerun command and verify the report plus all four raw/normalized side files.
