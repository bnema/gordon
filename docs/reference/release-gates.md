# Split release gates

Run from a clean checkout. Runtime-backed gates intentionally fail when their required engine is unavailable.

```bash
go test ./...
golangci-lint run ./...
make proto-check
make gitleaks
make release-check
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
make release-smoke
```

Old/new release gates use immutable baseline `8f4a170d141b3e6f9ced7632dd5ac76cf7f9f842`; local diagnosis may explicitly override it with `COMPAT_BASELINE_REF=<commit> make compat-harness-config`. Reports record the resolved baseline and candidate commits and reject self-comparison.

`make release-smoke` is non-publishing and must build the exact GoReleaser archives and release-target image, then run all five roles before the write-capable publishing job.

Before release, verify `gordon --help`, `gordon serve --help`, and every `gordon migrate <operation> --help`; parse `gordon.toml.example`; scan docs for engine sockets outside runtime and split registry targets using loopback; inspect generated role manifests/environment permissions; and require a clean `git status --short`.

Rootless migration acceptance requires a real old-to-split Podman run, application and OCI probes, final listener ownership, preserved volumes/networks, and a successful resume check from a fresh process.
