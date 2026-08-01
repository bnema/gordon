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

## Rootless split deployment

Split mode runs `control`, `runtime`, `edge`, and `registry` as separate containers on
rootless Podman; only runtime receives the engine socket. Set one up from scratch with
the [split bootstrap guide](./docs/operations/split-bootstrap.md).

## Main commands

| Area | Commands |
| --- | --- |
| Server | `gordon serve`, `gordon status`, `gordon config show` |
| Deploy | `gordon bootstrap`, `gordon push`, `gordon deploy`, `gordon restart`, `gordon pin` |
| Manage | `gordon routes`, `gordon attachments`, `gordon secrets`, `gordon networks`, `gordon volumes`, `gordon images` |
| Remote | `gordon remotes`, `gordon auth` |

Use `gordon <command> --help` as the command/options source of truth.

## Documentation

- [Installation](docs/installation.md)
- [Split mode](docs/operations/split-mode.md)
- [Configuration](docs/config/index.md)
- [Security hardening](docs/config/security-hardening.md)
- [Troubleshooting](docs/reference/troubleshooting.md)

Website: https://gordon.bnema.dev · Discussions: https://github.com/bnema/gordon/discussions

GPL-3.0
