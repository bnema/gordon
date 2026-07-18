# Old/new compatibility harness

Canonical operator documentation is [docs/reference/compatibility-harness.md](../../../docs/reference/compatibility-harness.md). The executable source of truth is the exact test selection and JSON no-skip assertions in `Makefile`.

```bash
make compat-harness-config
make compat-harness-cli
make compat-harness-api
make compat-harness-proxy
make compat-harness-traffic
make compat-harness-runtime
make compat-harness-registry
make compat-harness-security
GORDON_COMPAT_PODMAN=1 make compat-harness-migration
GORDON_COMPAT_PODMAN=1 make count2
```

Parity scenarios compare `origin/main` unless `GORDON_COMPAT_BASELINE_REF` is set. Current-only split scenarios verify traffic streams, runtime contracts, durable registry delivery, component authentication/socket isolation, interrupted migration, and authentic rootless-Podman old-to-split migration.

Reports live below ignored `artifacts/compat/` (or `GORDON_COMPAT_ARTIFACT_DIR`). Keep fixtures generic and isolated, register sensitive subprocess values for redaction, require exact pass counts with no skips, and clean only exact-owned labeled resources. Pending scenarios are not coverage and must fail when selected.
