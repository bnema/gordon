# Split release gates

Run the single fail-closed acceptance target from a clean checkout:

```bash
make pre-release-acceptance
```

It executes and enforces every release check: full tests and lint, generated protobuf cleanliness, secret scanning, immutable workflow/action validation, operation-level migration help (`plan`, `prepare`, `resume`, `status`, `switch`), example TOML parsing, generated role-manifest/environment minimization, documented engine-socket and registry-loopback ownership assertions, every Docker compatibility gate, two separate rootless-Podman migration invocations, and the exact non-publishing GoReleaser smoke.

The acceptance target checks a clean working tree before and after the gate. Runtime-backed checks intentionally fail when Docker, rootless Podman, QEMU, `actionlint`, or their required capabilities are unavailable.

`make release-smoke` builds fresh GoReleaser snapshot artifacts and reads both architecture-specific image references only from `dist/artifacts.json`. It verifies `linux/amd64` and `linux/arm64` under Docker/QEMU, then starts and probes monolith, control, runtime, edge, and registry; command help alone is never accepted as role verification.

Old/new compatibility uses immutable baseline `8f4a170d141b3e6f9ced7632dd5ac76cf7f9f842`; local diagnosis may explicitly override it with `COMPAT_BASELINE_REF=<commit>` for an individual harness command. Release acceptance does not override that baseline.

Rootless migration acceptance emits one sanitized report per invocation at `artifacts/compat/migration-rootless/invocation-{1,2}.json`. `manifest.json` names exactly those two reports; each must be non-skipped and contain passing application, registry, listener, and fresh-process resume probes. CI uploads only these explicit files, never a broad artifact directory.
