# Gordon

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL%203.0-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/bnema/gordon)](https://goreportcard.com/report/github.com/bnema/gordon)

Self-hosted web app deployments. Push to your registry, Gordon does the rest.

- Website: https://gordon.bnema.dev
- Documentation: [Docs](https://gordon.bnema.dev/docs) | [Wiki](https://gordon.bnema.dev/wiki)
- Discuss: [GitHub Discussions](https://github.com/bnema/gordon/discussions)

---

## What is Gordon?

Gordon is a private container registry + HTTP reverse proxy for your VPS. Push a container image exposing a web port, it deploys automatically with zero downtime.

```bash
docker build -t myapp .
docker push registry.your-server.com/myapp:latest
# → Live at https://app.your-server.com
```

Build on your machine, push to deploy. Works from your laptop or CI.

**Features:**
- Private Docker registry on your VPS
- Domain-to-container routing via HTTP reverse proxy
- Automatic deployment on image push
- Zero downtime updates
- Persistent volumes from Dockerfile VOLUME directives
- Environment variable management with secrets support
- Network isolation per application
- Single binary, ~15MB RAM

## Documentation

Full documentation is available at **[gordon.bnema.dev](https://gordon.bnema.dev)**

- [Documentation](https://gordon.bnema.dev/docs) - Installation, configuration, CLI reference
- [Wiki](https://gordon.bnema.dev/wiki) - Tutorials, guides, and examples

## Quick Start

```bash
# Download and install
wget https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64.tar.gz
tar -xzf gordon_linux_amd64.tar.gz
chmod +x gordon
sudo mv gordon /usr/local/bin/

# Start Gordon (generates config on first run)
gordon start
```

Config is created at `~/.config/gordon/gordon.toml`. See the [Getting Started guide](https://gordon.bnema.dev/docs/getting-started) for complete setup instructions.

## Community

- [Report bugs](https://github.com/bnema/gordon/issues)
- [Discussions](https://github.com/bnema/gordon/discussions)
- [Submit PRs](https://github.com/bnema/gordon/pulls)

## License

GPL-3.0 - Use freely, contribute back.
