# Gordon

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL%203.0-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

Self-hosted container deployment: private OCI registry, reverse proxy, and push-to-deploy in one Go binary.

## Deployment modes

- **Monolith:** `gordon serve` runs all responsibilities in one process.
- **Split:** the same binary runs `control`, `runtime`, `edge`, and `registry` roles on a private component network. Gordon's checkpointed migration generates strict role configuration.

Only runtime accesses Docker or Podman. Edge forwards registry traffic to the private `gordon-registry` alias; control, edge, and registry never receive an engine socket.

## Quick start

```bash
curl -fsSL https://gordon.bnema.dev/install | bash
gordon serve
```

The first run creates `~/.config/gordon/gordon.toml`. Configure `server.gordon_domain`, an `[entrypoints.edge]` smart-TCP listener, authentication, and routes. See [Getting started](docs/getting-started.md).

```bash
gordon bootstrap app.example.com app:latest
gordon push app:latest --domain app.example.com --build --no-confirm
gordon status
```

## Rootless split migration

Production migration targets rootless Podman and is resumable:

```bash
gordon migrate plan --config ~/.config/gordon/gordon.toml --json
gordon migrate prepare --config ~/.config/gordon/gordon.toml --json
gordon migrate switch --config ~/.config/gordon/gordon.toml --json
gordon migrate status --config ~/.config/gordon/gordon.toml --json
```

If the old monolith exits while transferring runtime authority, run `gordon migrate resume` from a fresh host process. There is no migration `rollback` command; before switch the old serving path is retained, while post-switch restoration is a backup-based disaster-recovery operation.

## Main commands

| Area | Commands |
| --- | --- |
| Server | `gordon serve`, `gordon status`, `gordon config show` |
| Deploy | `gordon bootstrap`, `gordon push`, `gordon deploy`, `gordon restart`, `gordon pin` |
| Manage | `gordon routes`, `gordon attachments`, `gordon secrets`, `gordon networks`, `gordon volumes`, `gordon images` |
| Migration | `gordon migrate plan|prepare|status|switch|resume` |
| Remote | `gordon remotes`, `gordon auth` |

Use `gordon <command> --help` as the command/options source of truth.

## Documentation

- [Installation](docs/installation.md)
- [Split mode](docs/operations/split-mode.md)
- [Migration runbook](docs/operations/migration.md)
- [Configuration](docs/config/index.md)
- [Security hardening](docs/config/security-hardening.md)
- [Troubleshooting](docs/reference/troubleshooting.md)

Website: https://gordon.bnema.dev · Discussions: https://github.com/bnema/gordon/discussions

GPL-3.0
