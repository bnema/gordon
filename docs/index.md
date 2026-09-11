# Gordon Documentation

Gordon is a self-hosted container deployment platform: a private container registry, a declarative app runtime, and a reverse proxy.

## What is Gordon?

Gordon runs on your VPS and provides:

- **Private Container Registry** - Push images from your local machine or CI
- **Declarative Apps** - One TOML file per app, explicit apply then deploy
- **HTTP Reverse Proxy** - Routes app hosts to containers
- **Push, Apply, Deploy** - Push stores images; deploy is always explicit
- **HTTP Zero-Downtime Updates** - Old containers serve until replacements pass readiness
- **Single Binary** - ~15MB RAM footprint

## How It Works

```
┌─────────────────┐     push      ┌─────────────────┐
│  Your Machine   │ ────────────> │  Gordon Server  │
│  docker build   │               │                 │
│  docker push    │               │  registry:5000  │
└─────────────────┘               │  proxy:80       │
                                  └────────┬────────┘
                                           │
                                           v
                                  ┌─────────────────┐
                                  │  Your App Live  │
                                  │  app.domain.com │
                                  └─────────────────┘
```

1. Build your container locally where you have computing power
2. Push to your Gordon registry
3. Apply the app manifest and deploy to activate it

## Quick Navigation

### Getting Started

- [Getting Started](./getting-started.md) - Deploy your first app in minutes
- [Installation](./installation.md) - Detailed installation instructions
- [Upgrading](./upgrading.md) - Migration guide for breaking changes
- [Concepts](./concepts.md) - Core concepts and architecture

### Configuration

- [Configuration Overview](./config/index.md) - All configuration options
- [Server Settings](./config/server.md) - Ports, domains, and runtime
- [App Manifest](./config/apps.md) - Declarative app files (services, hosts, secrets, volumes)
- [Migrate to Gordon v2.50](./migrate-to-v2.50.md) - Breaking upgrade and explicit secret migration
- [Traffic Plane](./config/traffic.md) - TCP, UDP, and TLS passthrough entrypoints
- [Authentication](./config/auth.md) - Registry auth plus remote CLI login/token workflows
- [Secrets](./config/secrets.md) - Installation secrets and app secret values
- [Network Isolation](./config/network-isolation.md) - Installation network policy
- [Logging](./config/logging.md) - Log collection and rotation

### CLI Reference

- [CLI Commands](./cli/index.md) - All available commands

### Deployment

- [Deployment Overview](./deployment/index.md) - Deployment strategies and methods
- [GitHub Actions](./deployment/github-actions.md) - CI/CD with GitHub
- [GitLab CI](./deployment/gitlab-ci.md) - CI/CD with GitLab
- [Generic CI](./deployment/generic-ci.md) - Jenkins, CircleCI, Drone, and others
- [Rollback](./deployment/rollback.md) - Version management and rollback

### Reference

- [Docker Labels](./reference/docker-labels.md) - Container and image labels
- [Environment Variables](./reference/env-variables.md) - Environment variable syntax
- [Troubleshooting](./reference/troubleshooting.md) - Common issues and solutions

## Requirements

- Linux VPS (Ubuntu/Debian recommended)
- Docker or Podman runtime
- Domain pointing to your server
- Optional Cloudflare account or other reverse proxy for edge TLS

> **Note:** Gordon can run behind Cloudflare/nginx, or terminate TLS directly on a smart TCP edge entrypoint via static certificates, public ACME certificates, or its internal CA.

## Related

- [Wiki Tutorials](/wiki/tutorials/index.md) - Step-by-step guides
- [Wiki Examples](/wiki/examples/index.md) - Configuration examples
