# Old/new compatibility harness

The executable CI slices are:

```bash
make compat-harness-config  # TestCompatibilityConfigShowJSON
make compat-harness-cli     # TestCompatibilityRoutesListJSON
make compat-harness-api     # Docker preflight + TestCompatibilityAdminAuthAndRouteCRUD
make compat-harness-proxy   # Docker preflight + managed, external, and zero-downtime routes
make compat-harness-security # Docker preflight + current edge isolation and component-auth gates
```

The baseline is `origin/main`; override it with `GORDON_COMPAT_BASELINE_REF=<ref>`. The executable real slices force `GORDON_COMPAT_RUN_REAL=1`. API and proxy also force `GORDON_COMPAT_REQUIRE_RUNTIME=1` and run `docker info`, so Docker is required and a missing runtime fails rather than skips. The proxy target uses one Go test invocation/build, parses JSON output, and requires each exact top-level managed, external, and drain test to pass once without skipping. Ordinary `go test ./...` keeps real scenarios gated off.

The three Docker routes use Docker's compatible CLI. `PodmanRequired=false` means they are not specifically Podman-backed, not that they need no runtime. Pending Podman e2e coverage remains opt-in (`GORDON_COMPAT_PODMAN=1`) and requires `podman info`.

Exact security-gate rerun:

```bash
GORDON_COMPAT_ARTIFACT_DIR=artifacts/compat GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test ./internal/testutils/compatoldnew -run '^(TestCompatibilitySecurityEdgeNoPodmanSocket|TestCompatibilitySecurityMissingComponentTokenRejected|TestCompatibilitySecurityWrongComponentTokenRejected|TestCompatibilitySecurityWrongScopeComponentTokenRejected)$' -count=1
```

This is a current-candidate security gate, not old/new parity: the edge test builds and starts the candidate edge in a Docker container with a fake authenticated control snapshot server, proves a snapshot-backed request works, then records only normalized contract booleans for socket mount/environment/file-descriptor absence. The three control EdgeService tests use a fresh isolated component-token store and prove missing, syntactically valid unknown, and wrong-scope credentials are denied while a valid edge credential receives a snapshot. Docker is an actionable local skip only outside the Make target; CI calls `docker info` and the target rejects skipped or duplicate selected tests. Reports contain no credential material, endpoints, socket paths, or container IDs.

Exact proxy-gate rerun:

```bash
GORDON_COMPAT_ARTIFACT_DIR=artifacts/compat GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=origin/main go test ./internal/testutils/compatoldnew -run '^(TestCompatibilityManagedHTTPRoute|TestCompatibilityExternalRoute|TestCompatibilityZeroDowntimeDrain)$' -count=1
```

Reports are under `artifacts/compat/{config,cli,api,proxy,proxy-external,proxy-zero-drain}/compat-report.json` (or the selected `COMPAT_ARTIFACT_DIR`), with `normalized.diff` and raw/normalized side files. The external route owns a run-unique Docker CGNAT network. The drain fixture keeps an old response open while a distinct replacement serves new traffic, then releases the old response; it owns a marker volume. Resources are exact-owned, labeled, and removed on success or failure; use those labels only for debugging cleanup.

Implemented scenarios are `cli/config-show-json`, `cli/routes-list-json`, `api/auth-missing-invalid`, `api/route-list-detail`, `api/route-add-update-remove`, `proxy/managed-http-route`, `proxy/external-route`, `proxy/zero-downtime-drain`, `security/edge-no-podman-socket`, `security/missing-component-token-rejected`, `security/wrong-component-token-rejected`, and `security/wrong-scope-component-token-rejected`. Remaining proxy shells (`unknown-host`, `h2c-backend`, `registry-domain-routing`, `body-size-limit`, `access-log-emitted`) remain pending, as do all migration scenarios and the registry/control/unsafe-runtime security shells. Pending is not coverage: selecting one fails.

Fixture checklist: use generic inputs; declare every surface; isolate old/new state; flag runtime needs; retain an actionable pending reason until executable; add an exact rerun and verify report side files.
