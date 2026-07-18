# Monolith-to-split migration

The supported production migration target is **rootless Podman**. Migration is checkpointed and resumable; it does not provide a `rollback` subcommand.

## Requirements

- A healthy monolith using the target config and data directory.
- Rootless Podman with its user socket active and API reachable.
- The candidate Gordon image available or pullable by that Podman user.
- Writable config, data, registry, environment, and credential storage.
- Enough disk space and no ambiguous/unmanaged Gordon resources.
- Public ports still owned by the current Gordon deployment until switching. Set `server.port` to the intended generated split-edge listen port even though monolith public traffic uses `[entrypoints]`.
- A private runtime handoff seed, preferably via environment:

```toml
[runtime]
token_env = "GORDON_RUNTIME_HANDOFF_TOKEN"
```

```bash
export GORDON_RUNTIME_HANDOFF_TOKEN="$(openssl rand -hex 32)"
export GORDON_MIGRATION_IMAGE="ghcr.io/example/gordon:<target-version>"
```

Do not put real token values in TOML, shell history, issue reports, or logs.

## Commands

Run each command against the same local config. Add `--json` for machine-readable output.

```bash
gordon migrate plan --config ~/.config/gordon/gordon.toml --json
gordon migrate prepare --config ~/.config/gordon/gordon.toml --json
gordon migrate status --config ~/.config/gordon/gordon.toml --json
gordon migrate switch --config ~/.config/gordon/gordon.toml --json
```

`plan` is read-only and must report `"ready": true`. `prepare` writes private role manifests/environment, creates the private component network, and starts prepared components while the old serving path remains authoritative. `switch` verifies old and new application/registry paths, requires authenticated edge state, transfers runtime authority, and activates final listeners.

A switch can stop the old monolith before its in-container CLI prints success. Check from a fresh host shell:

```bash
gordon migrate status --config ~/.config/gordon/gordon.toml --json
gordon migrate resume --config ~/.config/gordon/gordon.toml --json
```

After runtime handoff, `resume` reads the durable checkpoint and the generated `runtime.env`, then connects only to the replacement Gordon runtime Unix RPC. It cannot be redirected to Docker or Podman.

## Failure and rollback boundary

Before a successful switch, Gordon retains the old serving path and records the retry phase/attempt. Fix the reported category and rerun `switch` or `resume`; do not delete migration state.

There is no automatic reverse migration after phase `switched`. Recovery after a completed switch means repairing/resuming the split deployment. A manual restoration to monolith is an operator disaster-recovery action: stop split public listeners first, preserve all data volumes and checkpoint evidence, restore a verified backup, then start exactly one monolith owner. Never run monolith and split runtime as simultaneous owners.

## Acceptance

Confirm:

```bash
gordon migrate status --config ~/.config/gordon/gordon.toml --json
podman ps --filter label=gordon.component=true
curl -fsS https://gordon.example.com/v2/ >/dev/null
```

The registry probe may return `401`; that still proves edge-to-registry reachability when auth is enabled. Also deploy a test image, verify app traffic, restart control and registry, then confirm queued push events replay without duplicate deployment intent.

## Related

- [Split mode](./split-mode.md)
- [Rootless Podman](../../wiki/guides/podman-rootless.md)
- [Release gates](../reference/release-gates.md)
