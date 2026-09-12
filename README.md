# Gordon

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL%203.0-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

Self-hosted container deployment. Push an image, declare an app, deploy it.

- Website: https://bnema.dev/gordon
- Documentation: [Docs](https://gordon.bnema.dev/docs) | [Wiki](https://gordon.bnema.dev/wiki)
- Discuss: [GitHub Discussions](https://github.com/bnema/gordon/discussions)

---

## What is Gordon?

Gordon is a private container registry, an app runtime, and a reverse proxy for your VPS.

The flow is explicit: build and push an image, declare the app in a standalone TOML file, apply it, then deploy. Push transfers OCI content only — it never deploys.

## Quick Start

```bash
# Install the latest stable release to ~/.local/bin
curl -fsSL https://gordon.bnema.dev/install.sh | sh

# Start the server (restart your shell first if the installer updated PATH)
gordon serve
```

The installer uses `~/.local/bin` without `sudo` and can add the effective install directory to Fish, Bash, or Zsh PATH configuration. Set `GORDON_UPDATE_PATH=1` to update PATH without prompting or `GORDON_UPDATE_PATH=0` to leave configuration unchanged. Override the destination with an absolute path such as `GORDON_INSTALL_DIR="$HOME/bin"`, `GORDON_INSTALL_DIR="$HOME/.local/bin"`, or `GORDON_INSTALL_DIR=/usr/local/bin`.

To build the current `next` branch commit locally, use `GORDON_CHANNEL=next`. This is an unverified development source build, not a checksum-verified release, and requires a compatible Go toolchain. See the [installation guide](https://gordon.bnema.dev/docs/installation#choosing-an-install-channel).

Config is created at `~/.config/gordon/gordon.toml`. See the [Getting Started guide](https://gordon.bnema.dev/docs/getting-started) for full setup.

## Deploy with the CLI

Build locally, push to your Gordon server, declare the app, deploy:

```bash
# Push the image (OCI transfer only, never deploys)
gordon push myapp --build --remote prod

# Declare the app (blog.toml references the pushed tag)
gordon apps apply --file blog.toml --remote prod

# Activate it
gordon apps deploy blog --remote prod

# Check status
gordon status
gordon apps status blog --remote prod
```

Minimal app file (`blog.toml`):

```toml
name = "blog"

[[service]]
name = "web"
image = "gordon.mydomain.com/myapp:v1.2.0"

[[service.http]]
host = "blog.mydomain.com"
port = 3000
```

Manage lifecycle and secrets — all from the command line:

```bash
gordon apps restart blog --remote prod   # Restart from pinned digests
gordon apps stop blog --remote prod      # Stop, preserve all data
gordon apps secrets set blog --service web DATABASE_URL=... --remote prod
```

## Deploy from CI/CD

Push to Gordon's registry from any CI pipeline, then apply and deploy with the CLI. Push never triggers a deploy by itself.

### GitHub Actions

```yaml
- uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: registry.mydomain.com
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}
```

See the [Deploy Action README](.github/actions/deploy/README.md) for multi-platform builds, monorepo support, and all available options.

### Docker CLI + Gordon CLI

```bash
docker login gordon.mydomain.com
docker build -t gordon.mydomain.com/myapp:v1.0.0 .
docker push gordon.mydomain.com/myapp:v1.0.0
# Then, with the Gordon binary:
gordon apps apply --file blog.toml --remote prod
gordon apps deploy blog --remote prod
```

## CLI Commands

### Server

| Command | Description |
|---------|-------------|
| `gordon serve` | Start the Gordon server |
| `gordon status` | Show server and app fleet status |
| `gordon config show` | Display installation configuration |

### Applications

| Command | Description |
|---------|-------------|
| `gordon apps apply --file FILE` | Validate and persist an app manifest |
| `gordon apps deploy APP` | Activate an app revision |
| `gordon apps list` | List applications |
| `gordon apps show APP` | Show desired and active state |
| `gordon apps status APP` | Show effective vs observed state |
| `gordon apps logs APP` | Show logs for an app service |
| `gordon apps restart APP` | Restart from pinned digests |
| `gordon apps stop APP` | Stop, preserve all data |
| `gordon apps start APP` | Start a stopped app |
| `gordon apps remove APP` | Remove workloads (volumes/secrets retained) |
| `gordon push [image]` | Tag and push an image (never deploys) |

### Images & Registry

| Command | Description |
|---------|-------------|
| `gordon images list` | List runtime and registry images |
| `gordon images prune` | Clean up dangling images and old tags |
| `gordon images tags <repo>` | List registry tags for a repository |

### Secrets & Config

| Command | Description |
|---------|-------------|
| `gordon apps secrets set APP --service SVC KEY=VAL` | Set app secret values |
| `gordon secrets list <domain>` | List installation secret keys |
| `gordon secrets set <domain> --from-file PATH` | Set installation secrets |

### Remotes & Auth

| Command | Description |
|---------|-------------|
| `gordon remotes list` | List remote Gordon endpoints |
| `gordon remotes add <name> <url>` | Add a remote |
| `gordon remotes use <name>` | Set the active remote |
| `gordon auth login` | Authenticate to a remote |
| `gordon auth token generate` | Generate a JWT token |

## Features

- Private Docker/Podman registry on your VPS
- Declarative apps: one TOML file per app, explicit apply then deploy
- Domain-to-container routing through a smart TCP edge reverse proxy
- HTTP zero-downtime updates (old container serves until replacement passes readiness)
- Remote CLI management (daemon is the sole writer)
- Declarative per-service volumes, retained across lifecycle operations
- Per-service secrets in pass, app-wide public env in the manifest
- Per-app private networks plus opt-in shared networks
- Single binary

> [!NOTE]
> Gordon exposes public traffic through entrypoints such as `[entrypoints.edge]` with `protocol = "smart_tcp"`. It can terminate TLS via static certificates, public ACME certificates, or its internal CA. Cloudflare and upstream reverse proxies are optional deployment choices, not requirements. Use Gordon's ownership-aware volume commands; runtime commands such as `docker volume prune` bypass Gordon's retention checks.

## Documentation

Full documentation at **[gordon.bnema.dev](https://bnema.dev/gordon)**

- [Docs](https://bnema.dev/gordon/docs) — Installation, configuration, CLI reference
- [Wiki](https://bnema.dev/gordon/wiki) — Tutorials, guides, and examples

## Community

- [Report bugs](https://github.com/bnema/gordon/issues)
- [Discussions](https://github.com/bnema/gordon/discussions)
- [Submit PRs](https://github.com/bnema/gordon/pulls)

## License

GPL-3.0 — Use freely, contribute back.
