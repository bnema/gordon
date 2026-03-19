# Deployment Overview

Gordon deploys containers when you push images to its built-in registry. Three deployment methods are available depending on your workflow and infrastructure.

## Recommended: gordon push

`gordon push --build --remote` is the simplest way to build, push, and deploy from CI/CD pipelines.

- Single command: build + push + deploy
- Single secret (`GORDON_TOKEN`): auto-exchanges for a short-lived registry token
- Auto-detects version from CI environment (`$GITHUB_REF`, `$CI_COMMIT_TAG`, `$BUILD_SOURCEBRANCH`, or `git describe`)
- Chunked uploads (50MB chunks) — works behind Cloudflare and restrictive proxies

```bash
gordon push --build --remote https://gordon.example.com --no-confirm
```

## All Deployment Methods

| Method | Best For | Secrets Needed | Registry Access | Deploy Control |
|--------|----------|----------------|-----------------|----------------|
| `gordon push` (Recommended) | CI/CD pipelines | 1 (`GORDON_TOKEN`) | Via gordon domain (HTTPS) | Explicit (CLI-triggered) |
| `docker push` | Simple setups, existing Docker workflows | 2 (`username` + `token`) | Via gordon domain (HTTPS) | Automatic (event-based) |
| Docker labels + auto-route | GitOps, zero-config deploys | 2 (`username` + `token`) | Via gordon domain (HTTPS) | Automatic (label-driven) |

### Method 1: gordon push (Recommended)

The Gordon CLI handles authentication, image building, and deployment in a single step.

- Single token handles everything: admin API access + registry auth via automatic token exchange
- Version tag auto-detected from `$GITHUB_REF`, `$CI_COMMIT_TAG`, `$BUILD_SOURCEBRANCH`, or `git describe`
- Use `--no-deploy` for push-only workflows (useful for staging images without triggering deployment)
- Requires the Gordon binary on the CI runner

```bash
# Build, push, and deploy
gordon push --build --remote https://gordon.example.com --no-confirm

# Push only, no deploy
gordon push --build --remote https://gordon.example.com --no-deploy --no-confirm
```

### Method 2: docker push

Standard Docker workflow — no Gordon binary required on the runner.

- Use `docker login`, `docker build`, and `docker push` as usual
- Gordon auto-deploys when it receives the image (event-based, no explicit trigger needed)
- No Gordon binary needed on the CI runner
- Registry endpoint is `gordon.example.com` (not a separate registry host)

```bash
echo "$GORDON_TOKEN" | docker login -u ci-bot --password-stdin gordon.example.com
docker build -t gordon.example.com/myapp:v1.2.0 .
docker push gordon.example.com/myapp:v1.2.0
```

### Method 3: Docker labels + auto-route

Add a `gordon.domain` label to your image and Gordon creates the route automatically on push.

- Add `gordon.domain=app.example.com` label to your Dockerfile
- Push image — Gordon creates or updates the route without manual config
- Gated by the `auto_route_allowed_domains` allowlist in `gordon.toml`
- Best for GitOps workflows where routes are defined alongside the application

```dockerfile
LABEL gordon.domain="app.example.com"
```

See [Auto-Route](../config/auto-route.md) for configuration details.

## Registry Access

Gordon's registry is served through the main gordon domain over HTTPS on port 443. The internal registry port (5000) is never exposed externally. All push methods use `https://gordon.example.com/v2/...`.

### Network Topologies

| Setup | Configuration | Use Case |
|-------|--------------|----------|
| Public (default) | `auth.enabled = true` | Hosted CI runners (GitHub Actions, GitLab CI) |
| Tailscale only | `registry_allowed_ips = ["100.64.0.0/10"]` | Self-hosted runners in Tailnet |
| Localhost only | `auth.enabled = false` | Local development, single-machine deploys |

## Token Setup

See the examples below for the right scopes for each workflow.

```bash
# Minimum scope for gordon push — route lookup + registry push
# Server auto-deploys when it receives the image
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull,admin:routes:read" \
  --expiry 90d

# With explicit CLI-managed deploy (adds config:write for deploy control)
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull,admin:routes:read,admin:config:write" \
  --expiry 90d

# Scoped to a specific repository
gordon auth token generate \
  --subject ci-bot \
  --repo myapp \
  --scopes "push,pull,admin:routes:read" \
  --expiry 90d

# For docker push — registry scopes only
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull" \
  --expiry 90d
```

Set the generated token as `GORDON_TOKEN` in your CI environment.

## Version Strategies

### Latest Tag

Always deploy the most recent build:

```bash
docker tag myapp gordon.example.com/myapp:latest
docker push gordon.example.com/myapp:latest
```

```toml
[routes]
"app.example.com" = "myapp:latest"
```

### Semantic Versioning

Pin routes to specific versions and update config to roll forward:

```bash
docker tag myapp gordon.example.com/myapp:v2.1.0
docker push gordon.example.com/myapp:v2.1.0
```

```toml
[routes]
"app.example.com" = "myapp:v2.1.0"
```

To deploy a new version, update the tag in `gordon.toml` and push the new image.

### Git SHA Tags

Tag with commit hash for full traceability:

```bash
VERSION=$(git rev-parse --short HEAD)
docker tag myapp gordon.example.com/myapp:$VERSION
docker push gordon.example.com/myapp:$VERSION
```

## Zero-Downtime Deployment

Gordon performs zero-downtime deployments by default:

1. **New container starts** while the old one is still running
2. **Health check** waits for the new container to become ready
3. **Traffic switches** to the new container
4. **Old container stops** after traffic moves

```
Timeline ─────────────────────────────────────────>

Old Container:  [═══════════════════]
                                    ↓ stop
New Container:           [═════════════════════════>
                         ↑ start    ↑ traffic routed
```

## Related

- [GitHub Actions](./github-actions.md)
- [GitLab CI](./gitlab-ci.md)
- [Generic CI](./generic-ci.md)
- [Rollback](./rollback.md)
- [Routes Configuration](../config/routes.md)
- [Authentication](../config/auth.md)
- [Auto-Route](../config/auto-route.md)
