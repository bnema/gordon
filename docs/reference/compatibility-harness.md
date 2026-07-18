# Compatibility harness

The harness compares `origin/main` with the current checkout where old/new parity applies and runs current-only split security/migration contracts where baseline parity is not meaningful.

## Make targets

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

`config` and `cli` do not require an engine. API/proxy/traffic/registry/security targets perform Docker preflight and parse JSON test output so required scenarios cannot pass by skipping. Migration always runs deterministic protocol checks; `GORDON_COMPAT_PODMAN=1` additionally makes the authentic rootless-Podman old-to-split scenario mandatory. `count2` repeats the complete migration gate.

Override the comparison baseline only deliberately:

```bash
GORDON_COMPAT_BASELINE_REF=<ref> make compat-harness-config
```

Reports default below ignored `artifacts/compat/`. They are private diagnostic data, redacted by the harness, and must still be reviewed before sharing. Never commit raw artifacts.

## Covered split contracts

- CLI/config/admin compatibility;
- managed, external, zero-downtime, and distributed-drain traffic;
- TCP/UDP/TLS listener ownership and authenticated traffic streams;
- runtime adapter behavior;
- OCI old/new behavior, split registry push events, durable outbox replay, and control deduplication;
- component credential scope and absence of engine sockets from edge/registry;
- deterministic and authentic rootless Podman migration, interruption/resume, missing environment, and fail-closed switching.

The exact selected tests and skip assertions live in `Makefile`; treat it as the executable source of truth.

## Fixture policy

Use generic domains, credentials, and paths. Isolate state. Declare runtime requirements. A pending scenario is not coverage and must fail if selected. Every real scenario needs an exact rerun command, bounded cleanup, redacted artifacts, and a no-skip assertion.

## Related

- [Release gates](./release-gates.md)
- [Migration](../operations/migration.md)
