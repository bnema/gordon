# Gordon

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL%203.0-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/bnema/gordon)](https://goreportcard.com/report/github.com/bnema/gordon)

Self-hosted container deployment. Push an image, Gordon routes it to the web.

- Website: https://gordon.bnema.dev
- Documentation: [Docs](https://gordon.bnema.dev/docs) | [Wiki](https://gordon.bnema.dev/wiki)
- Discuss: [GitHub Discussions](https://github.com/bnema/gordon/discussions)

---

## What is Gordon?

Gordon is a private container registry and HTTP reverse proxy for your VPS. Push a container image that exposes a web port — Gordon deploys it with zero downtime.

## Quick Start

```bash
# Install
curl -fsSL https://gordon.bnema.dev/install.sh | sh

# Start the server
gordon serve
```

Config is created at `~/.config/gordon/gordon.toml`. See the [Getting Started guide](https://gordon.bnema.dev/docs/getting-started) for full setup.

## Deploy with the CLI

Build locally, push directly to your Gordon server:

```bash
# Push using an explicit domain override
gordon push --domain app.example.com

# Or add a route manually, then deploy
gordon routes add app.example.com myapp:latest
gordon routes deploy app.example.com

# Check status
gordon status
```

Roll back, restart, or manage secrets — all from the command line:

```bash
gordon rollback app.example.com    # Revert to a previous image tag
gordon restart app.example.com     # Restart the container
gordon secrets set app.example.com DB_HOST=db.internal API_KEY=secret123
```

## Deploy from CI/CD

Push to Gordon's registry from any CI pipeline. Gordon deploys automatically on image push.

### GitHub Actions

```yaml
- uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: registry.mydomain.com
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}
```

### Docker CLI

```bash
docker login registry.mydomain.com
docker build -t registry.mydomain.com/myapp:v1.0.0 .
docker push registry.mydomain.com/myapp:v1.0.0
# -> Deployed automatically
```

See the [Deploy Action README](.github/actions/deploy/README.md) for multi-platform builds, monorepo support, and all available options.

## CLI Commands

### Server

| Command | Description |
|---------|-------------|
| `gordon serve` | Start the Gordon server |
| `gordon status` | Show server and route health |
| `gordon config show` | Display server configuration |

### Deployment

| Command | Description |
|---------|-------------|
| `gordon push [image]` | Tag, push, and optionally deploy an image |
| `gordon routes list` | List all routes |
| `gordon routes add <domain> <image>` | Create or update a route |
| `gordon routes remove <domain>` | Remove a route |
| `gordon routes deploy <domain>` | Redeploy a route |
| `gordon rollback <domain>` | Roll back to a previous image |
| `gordon restart <domain>` | Restart a route container |

### Images & Registry

| Command | Description |
|---------|-------------|
| `gordon images list` | List runtime and registry images |
| `gordon images prune` | Clean up dangling images and old tags |
| `gordon images tags <repo>` | List registry tags for a repository |

### Secrets & Config

| Command | Description |
|---------|-------------|
| `gordon secrets list <domain>` | List secrets for a route |
| `gordon secrets set <domain> KEY=VAL` | Set secrets |
| `gordon secrets remove <domain> <key>` | Remove a secret |

### Remotes & Auth

| Command | Description |
|---------|-------------|
| `gordon remotes list` | List remote Gordon endpoints |
| `gordon remotes add <name> <url>` | Add a remote |
| `gordon remotes use <name>` | Set the active remote |
| `gordon auth login` | Authenticate to a remote |
| `gordon auth token generate` | Generate a JWT token |

## Features

- Private Docker registry on your VPS
- Domain-to-container routing via HTTP reverse proxy
- Automatic deployment on image push
- Auto-routing from image labels
- Remote CLI management
- Zero downtime updates
- Persistent volumes from Dockerfile VOLUME directives
- Environment variable management with secrets support
- Network isolation per application
- Single binary, ~15MB RAM

> [!WARNING]
> Gordon does not handle TLS termination. Place it behind Cloudflare Proxy or any upstream reverse proxy that manages HTTPS certificates.

## Documentation

Full documentation at **[gordon.bnema.dev](https://gordon.bnema.dev)**

- [Docs](https://gordon.bnema.dev/docs) — Installation, configuration, CLI reference
- [Wiki](https://gordon.bnema.dev/wiki) — Tutorials, guides, and examples

## Community

- [Report bugs](https://github.com/bnema/gordon/issues)
- [Discussions](https://github.com/bnema/gordon/discussions)
- [Submit PRs](https://github.com/bnema/gordon/pulls)

## License

GPL-3.0 — Use freely, contribute back.
