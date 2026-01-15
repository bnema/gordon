# Gordon Documentation

Gordon is a self-hosted container deployment platform that combines a private Docker registry with automatic container deployment.

## What is Gordon?

Gordon runs on your VPS and provides:

- **Private Container Registry** - Push images from your local machine or CI
- **HTTP Reverse Proxy** - Routes domains to containers automatically
- **Push-to-Deploy** - Containers deploy when you push new images
- **Zero-Downtime Updates** - New containers start before old ones stop
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
3. Gordon automatically deploys and routes traffic to your container

## Quick Navigation

### Getting Started

- [Getting Started](./getting-started.md) - Deploy your first app in minutes
- [Installation](./installation.md) - Detailed installation instructions
- [Concepts](./concepts.md) - Core concepts and architecture

### Configuration

- [Configuration Overview](./config/index.md) - All configuration options
- [Server Settings](./config/server.md) - Ports, domains, and runtime
- [Routes](./config/routes.md) - Domain to container mapping
- [Registry Auth](./config/registry-auth.md) - Password and token authentication
- [Secrets](./config/secrets.md) - Secure credential storage
- [Network Isolation](./config/network-isolation.md) - Per-app network isolation
- [Attachments](./config/attachments.md) - Service dependencies
- [Logging](./config/logging.md) - Log collection and rotation
- [Environment Variables](./config/env.md) - Per-route environment configuration

### CLI Reference

- [CLI Overview](./cli/index.md) - Available commands
- [gordon start](./cli/start.md) - Start the server
- [gordon auth](./cli/auth.md) - Manage authentication

### Deployment

- [Deployment Overview](./deployment/index.md) - Deployment strategies
- [GitHub Actions](./deployment/github-actions.md) - CI/CD with GitHub
- [Rollback](./deployment/rollback.md) - Version management and rollback

### Reference

- [Docker Labels](./reference/docker-labels.md) - Container and image labels
- [Environment Variables](./reference/env-variables.md) - Environment variable syntax
- [Troubleshooting](./reference/troubleshooting.md) - Common issues and solutions

## Requirements

- Linux VPS (Ubuntu/Debian recommended)
- Docker or Podman runtime
- Domain pointing to your server
- Cloudflare account for HTTPS (free tier works)

> **Note:** Gordon does not handle TLS certificates directly. HTTPS is terminated by Cloudflare (or similar proxy) which forwards HTTP traffic to Gordon internally.

## Related

- [Wiki Tutorials](/wiki/tutorials/index.md) - Step-by-step guides
- [Wiki Examples](/wiki/examples/index.md) - Configuration examples
