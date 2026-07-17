# Old/new compatibility harness

The executable CI slices are:

```bash
make compat-harness-config  # TestCompatibilityConfigShowJSON
make compat-harness-cli     # TestCompatibilityRoutesListJSON
make compat-harness-api     # Docker preflight + TestCompatibilityAdminAuthAndRouteCRUD
```

The baseline is `origin/main`; override it with `GORDON_COMPAT_BASELINE_REF=<ref>`. Exact focused reruns are:

```bash
GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityConfigShowJSON$' -count=1
GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRoutesListJSON$' -count=1
GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityAdminAuthAndRouteCRUD$' -count=1
```

Use `GORDON_COMPAT_ARTIFACT_DIR=artifacts/compat` to preserve reports at `artifacts/compat/{config,cli,api}/compat-report.json` and `normalized.diff`. The API slice requires a reachable Unix-socket Docker daemon and must not skip in CI. Podman work is opt-in (`GORDON_COMPAT_PODMAN=1`) and requires `podman info`.

Implemented scenarios are exactly `cli/config-show-json`, `cli/routes-list-json`, `api/auth-missing-invalid`, `api/route-list-detail`, and `api/route-add-update-remove`. All other shells, including migration and security, are pending: they are not coverage, and selecting them fails.

Fixture checklist: use generic inputs; declare every surface; isolate old/new state; flag runtime needs; retain an actionable pending reason until executable; add an exact rerun and verify both report files.
