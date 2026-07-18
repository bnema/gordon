# Upgrading Gordon

## Safe binary upgrade

1. Back up the config and `server.data_dir` with Gordon stopped or using a storage-consistent snapshot.
2. Read the target release notes and validate the config with the target binary in staging.
3. Keep the previous binary and backup until application and registry probes pass.
4. Upgrade, restart the current deployment mode, then check `gordon status` and logs.

Do not start two runtime owners against the same engine and data directory.

## Moving from monolith to split mode

The supported production path is the checkpointed rootless-Podman migration. Do not manually create four services from the monolith TOML. Gordon generates strict role manifests and scoped environment files.

```bash
gordon migrate plan --config ~/.config/gordon/gordon.toml --json
gordon migrate prepare --config ~/.config/gordon/gordon.toml --json
gordon migrate switch --config ~/.config/gordon/gordon.toml --json
```

If the invoking monolith exits during runtime transfer, use a fresh shell:

```bash
gordon migrate status --config ~/.config/gordon/gordon.toml --json
gordon migrate resume --config ~/.config/gordon/gordon.toml --json
```

See the [migration runbook](./operations/migration.md) for requirements, failure handling, and the rollback boundary.

## Configuration checks

Current public application listeners use `[entrypoints.<name>]`; `server.gordon_domain` is the registry/admin host. `server.registry_port` remains the monolith/registry listen port. Verify current output with:

```bash
gordon config show --json
gordon serve --help
gordon migrate --help
```

Route keys must be hostnames, for example:

```toml
[routes]
"app.example.com" = { image = "myapp:latest" }
```

Password authentication is not supported. Use scoped tokens and a production secret backend. Keep `legacy_registry_domains` only while clients move to `gordon_domain`.

## Recovery

Before split switch succeeds, repair the failed preflight/probe and rerun `switch` or `resume`; the old serving path remains retained. After `switched`, there is no automatic reverse-migration command. Restore monolith only as disaster recovery from a verified backup after stopping split public and runtime owners.

## Related

- [Migration](./operations/migration.md)
- [Split mode](./operations/split-mode.md)
- [Release gates](./reference/release-gates.md)
