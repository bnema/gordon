# Old/new compatibility harness

The executable CI slices are:

```bash
make compat-harness-config  # TestCompatibilityConfigShowJSON
make compat-harness-cli     # TestCompatibilityRoutesListJSON
make compat-harness-api     # Docker preflight + TestCompatibilityAdminAuthAndRouteCRUD
make compat-harness-proxy   # blocking Docker preflight + TestCompatibilityManagedHTTPRoute
```

The baseline is `origin/main`; override it with `GORDON_COMPAT_BASELINE_REF=<ref>`. The executable real slices force `GORDON_COMPAT_RUN_REAL=1`. API and proxy also force `GORDON_COMPAT_REQUIRE_RUNTIME=1` and run `docker info`, so Docker is required and a missing runtime fails rather than skips. The proxy target verifies that `TestCompatibilityManagedHTTPRoute` passed, rejecting a skipped or missing real test. Ordinary `go test ./...` keeps real scenarios gated off.

The managed route uses Docker's compatible CLI. Its `PodmanRequired=false` means it is not specifically Podman-backed, not that it needs no runtime. Future Podman e2e coverage remains opt-in (`GORDON_COMPAT_PODMAN=1`) and requires `podman info`.

Exact managed-route rerun:

```bash
GORDON_COMPAT_ARTIFACT_DIR=artifacts/compat GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^TestCompatibilityManagedHTTPRoute$' -count=1
```

Reports are under `artifacts/compat/{config,cli,api,proxy}/compat-report.json` (or the selected `COMPAT_ARTIFACT_DIR`), with `normalized.diff` and raw/normalized side files. Managed route containers and its temporary image are labeled and removed on success or failure; use those labels only for debugging cleanup.

Implemented scenarios are `cli/config-show-json`, `cli/routes-list-json`, `api/auth-missing-invalid`, `api/route-list-detail`, `api/route-add-update-remove`, and `proxy/managed-http-route`. Remaining proxy shells (`unknown-host`, `external-route`, `h2c-backend`, `registry-domain-routing`, `body-size-limit`, `zero-downtime-drain`, `access-log-emitted`) remain pending, as do all migration and security shells. Pending is not coverage: selecting one fails.

Fixture checklist: use generic inputs; declare every surface; isolate old/new state; flag runtime needs; retain an actionable pending reason until executable; add an exact rerun and verify report side files.
