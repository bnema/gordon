# Gordon

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

Self-hosted container deployment. Push an image, declare an app, deploy it.

- Website: https://bnema.dev/gordon
- Documentation: [Docs](https://bnema.dev/gordon/docs) | [Wiki](https://bnema.dev/gordon/wiki)
- Discuss: [GitHub Discussions](https://github.com/bnema/gordon/discussions)

---

## What is Gordon?

Gordon is a private image container registry, an app runtime, and a reverse proxy for your VPS.

The flow is explicit: you build an image locally, push it to your registry then declare the app in a standalone TOML file, apply it, deploy and it's live.

## Quick Start

```bash
# Install the latest stable release to ~/.local/bin
curl -fsSL https://bnema.dev/gordon/install | sh

# Start the server (restart your shell first if the installer updated PATH)
gordon serve
```
See the [installation guide](https://bnema.dev/gordon/docs/installation#choosing-an-install-channel) for more.

Config is created at `~/.config/gordon/gordon.toml`. See the [Getting Started guide](https://bnema.dev/gordon/docs/getting-started) for full setup.

## Deploy with the CLI

Example of the flow :

```bash
# Push the image (OCI transfer only, never deploys)
gordon images push myapp --build --remote prod

# Declare the app (blog.toml references the pushed tag)
gordon apps apply --file blog.toml --remote prod

# Activate it
gordon apps deploy blog --remote prod

# Check status
gordon daemon status
gordon apps status blog --remote prod
```

Minimal app file (`blog.toml`):

```toml
name = "blog"

[services.web]
image = "gordon.mydomain.com/myapp:v1.2.0"

[[services.web.http]]
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

Push never triggers a deploy by itself. The recommended CI path is OIDC digest promotion: the job authenticates with its OIDC ID token on a dedicated CI host, pushes the image, and runs exactly that digest in one service. No long-lived Gordon token is stored in CI.

```bash
gordon images push web --build --tag v1.2.0 --remote https://ci.example.com --json >push.json
gordon apps promote web --service web --image "$(jq -r .image push.json)" --remote https://ci.example.com
```

See [Generic CI](docs/deployment/generic-ci.md) for the server-side trusted issuer and promotion policy, and a Docker + curl variant.

Runners without OIDC can push with a stored token, then apply and deploy with the CLI:

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
| `gordon daemon status` | Show server and app fleet status |
| `gordon daemon logs` | Show Gordon process logs |
| `gordon daemon reload` | Reload installation configuration |
| `gordon daemon config show` | Display installation configuration |

### Applications

| Command | Description |
|---------|-------------|
| `gordon apps apply --file FILE` | Validate and persist an app manifest |
| `gordon apps deploy APP` | Activate an app revision |
| `gordon apps promote APP --service SVC --image REPO@DIGEST` | Run one pushed digest in one service |
| `gordon apps list` | List applications |
| `gordon apps show APP` | Show desired and active state |
| `gordon apps status APP` | Show effective vs observed state |
| `gordon apps logs APP` | Show logs for an app service |
| `gordon apps restart APP` | Restart from pinned digests |
| `gordon apps stop APP` | Stop, preserve all data |
| `gordon apps start APP` | Start a stopped app |
| `gordon apps remove APP` | Remove workloads (volumes/secrets retained) |

### Images & Registry

| Command | Description |
|---------|-------------|
| `gordon images push [image]` | Tag and push an image (never deploys) |
| `gordon images list` | List runtime and registry images |
| `gordon images prune` | Clean up dangling images and old tags |
| `gordon images tags <repo>` | List registry tags for a repository |

### Secrets

| Command | Description |
|---------|-------------|
| `gordon apps secrets list APP` | List app secret names |
| `gordon apps secrets set APP --service SVC KEY=VAL` | Set app secret values |

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
- Zero-downtime replacement of stateless TCP/HTTP services (start, verify readiness, switch traffic, stop the old one); services with volumes, binds, devices, or UDP interfaces are replaced sequentially
- Remote CLI management (daemon is the sole writer)
- Declarative per-service volumes, retained across lifecycle operations
- Per-service secrets in pass, app-wide public env in the manifest
- Per-app private networks plus opt-in shared networks
- Single binary

> [!NOTE]
> Gordon exposes public traffic through entrypoints such as `[entrypoints.edge]` with `protocol = "smart_tcp"`. It can terminate TLS via static certificates, public ACME certificates, or its internal CA. Cloudflare and upstream reverse proxies are optional deployment choices, not requirements. Use Gordon's ownership-aware volume commands; runtime commands such as `docker volume prune` bypass Gordon's retention checks.

## Documentation

Full documentation at **[bnema.dev/gordon](https://bnema.dev/gordon)**

- [Docs](https://bnema.dev/gordon/docs) — Installation, configuration, CLI reference
- [Wiki](https://bnema.dev/gordon/wiki) — Tutorials, guides, and examples

## Community

- [Report bugs](https://github.com/bnema/gordon/issues)
- [Discussions](https://github.com/bnema/gordon/discussions)
- [Submit PRs](https://github.com/bnema/gordon/pulls)

## License

[AGPL-3.0](LICENSE). If you run a modified Gordon as a network service, you must offer its source code to its users.
