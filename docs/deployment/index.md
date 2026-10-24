# Deployment Overview

Gordon deploys apps in two explicit steps: push the image to its built-in registry, then activate it. Push transfers OCI content only — it never deploys, creates routes, or modifies manifests.

## Recommended for CI: OIDC Set-image

The CI job uses its OIDC ID token on the [CI host](../config/server.md#ci-host), pushes the image, and runs exactly that digest in one service of an existing app. No long-lived Gordon token is stored in CI; a [set-image policy](../config/set-image.md) limits each identity to one app, service, and repository.

```bash
gordon images push web --build --tag v1.2.0 --remote https://ci.example.com --json >push.json
gordon apps set-image web --service web --image "$(jq -r .image push.json)" --remote https://ci.example.com
```

See [Generic CI](./generic-ci.md) for setup, the Docker + curl flow, and HTTP results.

## Stored Token: gordon images push + apps deploy

`gordon images push --build --remote` builds, pushes, and stores the image from CI/CD pipelines. Activation is a separate explicit step with the Gordon CLI. Use this when the runner has no OIDC identity or the app manifest itself changes.

- Single secret (`GORDON_TOKEN`): auto-exchanges for a short-lived registry token
- Auto-detects version from CI environment (`$GITHUB_REF`, `$CI_COMMIT_TAG`, `$BUILD_SOURCEBRANCH`, or `git describe`)
- Chunked uploads (50MB chunks) — works behind Cloudflare and restrictive proxies

```bash
gordon images push --build --remote https://gordon.example.com
```

Then activate (from CI with the Gordon binary, or from your machine):

```bash
gordon apps apply --file blog.toml --remote https://gordon.example.com
gordon apps deploy blog --remote https://gordon.example.com
```

## All Deployment Methods

| Method | Best For | Secrets Needed | Registry Access | Deploy Control |
|--------|----------|----------------|-----------------|----------------|
| `gordon images push` + `apps set-image` (OIDC) | CI/CD pipelines with an OIDC identity | None (job ID token) | Via CI host (HTTPS, push only) | One digest in one service |
| `gordon images push` + `apps deploy` | CI/CD pipelines without OIDC | 1 (`GORDON_TOKEN`) | Via gordon domain (HTTPS) | Explicit (CLI-triggered) |
| `docker push` + `apps deploy` | Simple setups, existing Docker workflows | 2 (`username` + `token`) | Via gordon domain (HTTPS) | Explicit (CLI-triggered) |

### Method 1: gordon images push + apps deploy

The Gordon CLI handles authentication, image building, and registry upload in a single step. Deploy stays explicit.

- Single token handles everything: admin API access + registry auth via automatic token exchange
- Version tag auto-detected from `$GITHUB_REF`, `$CI_COMMIT_TAG`, `$BUILD_SOURCEBRANCH`, or `git describe`
- Requires the Gordon binary on the CI runner

```bash
# Build and push (OCI transfer only)
gordon images push --build --remote https://gordon.example.com

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

Clients use the main Gordon domain over HTTPS, normally on port 443. Gordon binds `server.registry_port` to `127.0.0.1`; the public edge proxies authenticated registry requests to that loopback listener. Do not publish or forward the loopback registry port. With `auth.enabled=false`, registry access is loopback-only and the public edge refuses registry-domain requests.

### Network Topologies

| Setup | Configuration | Use Case |
|-------|--------------|----------|
| Public (default) | `auth.enabled = true` | Hosted CI runners (GitHub Actions, GitLab CI) |
| Tailscale only | `registry_allowed_ips = ["100.64.0.0/10"]` | Self-hosted runners in Tailnet |
| Localhost only | `auth.enabled = false` | Local development, single-machine deploys |

## Token Setup

See the examples below for the right scopes for each workflow.

```bash
# docker push only — registry scopes
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull" \
  --expiry 90d

# gordon images push only — the CLI also needs one admin scope
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull,admin:apps:read" \
  --expiry 90d

# Push + apply/deploy — adds app mutation scopes
gordon auth token generate \
  --subject ci-bot \
  --scopes "push,pull,admin:apps:read,admin:apps:write" \
  --expiry 90d

# Repository-scoped registry token for docker push only
gordon auth token generate \
  --subject ci-bot \
  --repo myapp \
  --scopes "push,pull" \
  --expiry 90d
```

Set the generated token as `GORDON_TOKEN` in your CI environment. `gordon images push` calls the admin API before it exchanges the token for registry access, so its token needs at least one `admin:*` scope; `admin:apps:read` is the smallest. `--repo` limits registry push/pull to that repository; it does not constrain `admin:*` scopes. Use a separate least-privilege token for app administration when repository and deployment duties must be isolated.

## Version Strategies

### Latest Tag

Always deploy the most recent build:

```bash
docker tag myapp gordon.example.com/myapp:latest
docker push gordon.example.com/myapp:latest
```

Reference it from the app manifest:

```toml
[services.web]
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

Gordon deploys an app's services one at a time in sorted order. A stateless TCP or HTTP service (no volume, bind, device, or UDP interface) is replaced without downtime; a service that owns a volume, bind, or device is replaced sequentially, because Gordon never runs two Gordon-managed generations of such a service at the same time. Gordon holds the service's traffic during the handover, so clients wait instead of failing, within a deadline. A UDP service is also replaced sequentially: UDP sessions stay pinned to the backend they started on, so they could not follow the switch. `gordon apps restart` follows the same sequence for every service.

Stateless service (no volume, bind, device, or UDP interface):

```
Preflight ─► start new ─► readiness ─► publish ACTIVE ─► route traffic ─► SIGTERM old ─► stop_grace ─► kill + remove old
```

Service with a volume, bind, device, or UDP interface:

```
Preflight ─► create new ─► hold traffic ─► stop + remove old ─► start new ─► readiness ─► publish ACTIVE ─► release held traffic
```

The new container is created first, without a restart policy until it has started. While traffic is held, HTTP requests to the service's hosts wait with their body unread, and TCP connections on its entrypoint are accepted and wait (they count against `traffic.tcp.max_connections`). UDP routes are withdrawn, as sessions cannot be held. The hold lasts at most the old container's `stop_grace` plus the readiness timeout plus 15 seconds, capped at `traffic.handover.max_hold` (default 90s; `"0s"` disables holding and withdraws traffic instead). When the deadline passes or the handover fails, held HTTP requests receive `503` with `Retry-After: 5` and held TCP connections are closed. A known app host with no available backend answers `503` with `Retry-After`; unknown hosts answer `404`. See [Handover](../config/traffic.md#handover).

Preflight (image resolution and pull, secrets, volumes, networks, bind policy, reservations) completes before anything is disrupted, so a preflight failure leaves the running service untouched. A failure while preparing the new container (image, create) also leaves the old container serving.

For a stateless service the old container serves until the new one is ready and routed, then receives SIGTERM and is killed after its `stop_grace` (default 30s). During the overlap both containers share the service's network alias, so other services of the app may reach either one. Database migrations must stay backward compatible with the previous version of the service. A new container that fails readiness is removed and the old one is not touched. An old container that cannot be removed is reported as a cleanup warning without failing the deploy; it stays durably marked for retirement, and the periodic recovery pass retries it with its own `stop_grace` once that pass has verified the new container, even after later deploys. A retirement is only complete once the old container's loopback port claims are released; until then it is retried. `gordon apps stop` and `gordon apps remove` also stop such an old container, and fail, keeping the mark, if they cannot.

For a sequential replacement, a failure after the old container was removed is explicit: Gordon does not recreate the old container during that operation and does not promise automatic data rollback. A stateful service's recovery inhibition stays in place until its never-published candidate is confirmed removed; a later boot/start/restart then clears it and rebuilds the generation `ACTIVE` records from its pinned digest, while a candidate that cannot be removed fails recovery closed. Deployment stops at the first service failure: services already deployed in that run are kept and are not rolled back.

If a deploy is interrupted between creating the replacement container and publishing it, that candidate is recorded durably and Gordon removes it before creating or rebuilding any generation of that app — at boot and before every mutation; a stateless service's old container keeps serving. If it is interrupted after the switch but before the old container was stopped, the periodic recovery pass verifies and routes the published container and then stops the old one. An interrupted operation is finalized instead of staying in flight: as a failure, or as the success it was when every step had already completed. If a recorded candidate cannot be removed, reconciliation fails closed: the operation is not finalized, stays the latest journal, and every later mutation is refused so a newer operation can never mask the orphan. A failed final traffic publication is republished by the next recovery pass, within 15 seconds by default.

## Related

- [GitHub Actions](./github-actions.md)
- [GitLab CI](./gitlab-ci.md)
- [Generic CI](./generic-ci.md)
- [Apps CLI](../cli/apps.md)
- [Authentication](../config/auth.md)
- [Traffic Plane Configuration](../config/traffic.md#handover)
