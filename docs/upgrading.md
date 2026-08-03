# Upgrading Gordon

## Safe binary upgrade

1. Back up the config and `server.data_dir` with Gordon stopped or using a storage-consistent snapshot.
2. Read the target release notes and validate the config with the target binary in staging.
3. Keep the previous binary and backup until application and registry probes pass.
4. Upgrade, restart the current deployment mode, then check `gordon status` and logs.

Do not start two runtime owners against the same engine and data directory.

## Moving from monolith to split mode

There is no in-place monolith-to-split conversion command. Set up a fresh v3 split
deployment and move workloads onto it: see [Split bootstrap](./operations/split-bootstrap.md).

Do not hand-write four role services from the monolith TOML; follow the bootstrap
guide so role manifests and scoped environment files are generated correctly.

## Configuration checks

Current public application listeners use `[entrypoints.<name>]`; `server.gordon_domain` is the registry/admin host. `server.registry_port` remains the monolith/registry listen port. Verify current output with:

```bash
gordon config show --json
gordon serve --help
```

Route keys must be hostnames, for example:

```toml
[routes]
"app.example.com" = { image = "myapp:latest" }
```

Password authentication is not supported. Use scoped tokens and a production secret backend. Keep `legacy_registry_domains` only while clients move to `gordon_domain`.

## Recovery

Restore monolith only as disaster recovery from a verified backup, after stopping
any split public and runtime owners.

## Related

- [Split bootstrap](./operations/split-bootstrap.md)
- [Split mode](./operations/split-mode.md)
- [Release gates](./reference/release-gates.md)
