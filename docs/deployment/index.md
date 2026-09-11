# Deployment Overview

Gordon deploys apps in two explicit steps: push the image to its built-in registry, then apply the app manifest and deploy. Push transfers OCI content only — it never deploys, creates routes, or modifies manifests.

## Recommended: gordon push + apps deploy

`gordon push --build --remote` builds, pushes, and stores the image from CI/CD pipelines. Activation is a separate explicit step with the Gordon CLI.

- Single secret (`GORDON_TOKEN`): auto-exchanges for a short-lived registry token
- Auto-detects version from CI environment (`$GITHUB_REF`, `$CI_COMMIT_TAG`, `$BUILD_SOURCEBRANCH`, or `git describe`)
- Chunked uploads (50MB chunks) — works behind Cloudflare and restrictive proxies

```bash
gordon push --build --remote https://gordon.example.com
```

Then activate (from CI with the Gordon binary, or from your machine):

```bash
gordon apps apply --file blog.toml --remote https://gordon.example.com
gordon apps deploy blog --remote https://gordon.example.com
```

## All Deployment Methods

| Method | Best For | Secrets Needed | Registry Access | Deploy Control |
|--------|----------|----------------|-----------------|----------------|
| `gordon push` + `apps deploy` (Recommended) | CI/CD pipelines | 1 (`GORDON_TOKEN`) | Via gordon domain (HTTPS) | Explicit (CLI-triggered) |
| `docker push` + `apps deploy` | Simple setups, existing Docker workflows | 2 (`username` + `token`) | Via gordon domain (HTTPS) | Explicit (CLI-triggered) |

### Method 1: gordon push + apps deploy (Recommended)

The Gordon CLI handles authentication, image building, and registry upload in a single step. Deploy stays explicit.

- Single token handles everything: admin API access + registry auth via automatic token exchange
- Version tag auto-detected from `$GITHUB_REF`, `$CI_COMMIT_TAG`, `$BUILD_SOURCEBRANCH`, or `git describe`
- Requires the Gordon binary on the CI runner

```bash
# Build and push (OCI transfer only)
gordon push --build --remote https://gordon.example.com

# Apply the manifest that references the pushed tag, then deploy
gordon apps apply --file blog.toml --remote https://gordon.example.com
gordon apps deploy blog --remote https://gordon.example.com
```

### Method 2: docker push + apps deploy

Standard Docker workflow — no Gordon binary required on the runner for the push itself.

- Use `docker login`, `docker build`, and `docker push` as usual
- Pushing only stores the image; deploy explicitly with the Gordon CLI afterwards
- Registry endpoint is `gordon.example.com` (not a separate registry host)

```bash
echo "$GORDON_TOKEN" | docker login -u ci-bot --password-stdin gordon.example.com
docker build -t gordon.example.com/myapp:v1.2.0 .
docker push gordon.example.com/myapp:v1.2.0
# Then: gordon apps apply --file blog.toml + gordon apps deploy blog
```

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
# Push only — registry scopes
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull" \
  --expiry 90d

# Push + apply/deploy — adds app mutation scopes
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull,admin:apps:read,admin:apps:write" \
  --expiry 90d

# Scoped to a specific repository
gordon auth token generate \
  --subject ci-bot \
  --repo myapp \
  --scopes "push,pull,admin:apps:read,admin:apps:write" \
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

Reference it from the app manifest:

```toml
[[service]]
name = "web"
image = "gordon.example.com/myapp:latest"
```

### Semantic Versioning

Pin services to specific versions and apply a new manifest to roll forward:

```bash
docker tag myapp gordon.example.com/myapp:v2.1.0
docker push gordon.example.com/myapp:v2.1.0
```

To deploy a new version, update the tag in the app file, apply, and deploy.

### Git SHA Tags

Tag with commit hash for full traceability:

```bash
VERSION=$(git rev-parse --short HEAD)
docker tag myapp gordon.example.com/myapp:$VERSION
docker push gordon.example.com/myapp:$VERSION
```

## Updates

For HTTP services without volumes, Gordon keeps the old container serving until the replacement passes readiness, then switches traffic, drains, and retires the old container. Deployment stops at the first service failure: already successful services are preserved, later services stay unchanged.

```
Timeline ─────────────────────────────────────────>

Old Container:  [═══════════════════]
                                    ↓ retire
New Container:           [═════════════════════════>
                         ↑ start    ↑ traffic routed
```

## Related

- [GitHub Actions](./github-actions.md)
- [GitLab CI](./gitlab-ci.md)
- [Generic CI](./generic-ci.md)
- [Apps CLI](../cli/apps.md)
- [Authentication](../config/auth.md)
