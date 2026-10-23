# GitLab CI/CD Deployment

## Recommended: OIDC Promotion

Configure Gordon with your GitLab issuer and the job's exact subject:

```toml
[[trusted_issuers]]
issuer = "https://gitlab.com"
audience = "https://ci.example.com"

[[promotion_policies]]
issuer = "https://gitlab.com"
subjects = ["project_path:acme/web:ref_type:branch:ref:main"]
app = "web"
service = "web"
repository = "web"
```

For a self-managed GitLab, use its exact issuer URL. The runner needs Gordon,
Docker, and `jq`; Docker-in-Docker runners also need their usual daemon setup.
Request an ID token with the configured audience and pass it directly to Gordon:

```yaml
promote:
  stage: deploy
  id_tokens:
    GORDON_OIDC_TOKEN:
      aud: https://ci.example.com
  variables:
    GORDON_REMOTE: https://ci.example.com
  script:
    - gordon images push web --build --tag "$CI_COMMIT_SHA" --remote "$GORDON_REMOTE" --json >push.json
    - gordon apps promote web --service web --image "$(jq -r .image push.json)" --remote "$GORDON_REMOTE" --json
  rules:
    - if: '$CI_COMMIT_BRANCH == "main"'
```

`GORDON_OIDC_TOKEN` selects OIDC mode; no `GORDON_TOKEN` is needed.
Promotion polls by key; exit 0 means success, exit 1 means failure or refusal.
GitLab ID tokens expire at the job timeout, and Gordon rejects tokens whose
lifetime exceeds `max_token_lifetime` (default `1h`): set `max_token_lifetime`
on the trusted issuer to at least the job timeout.

### Docker and curl

Use the same `id_tokens` declaration with the
[Docker and curl flow](./generic-ci.md#docker-and-curl): Docker login uses
username `oidc` and `GORDON_OIDC_TOKEN` as password. Push, read the manifest
digest, exchange Basic credentials at `/auth/token`, POST
`/-/ci/apps/web/promote` with `Idempotency-Key`, then poll
`/-/ci/apps/web/operations/by-key/{key}`. See
[HTTP Results and Retries](./generic-ci.md#http-results-and-retries) for
202 versus 200, 409 `app-busy`/`Retry-After`, and the allowlist.
The CI host does not serve image pulls or admin calls; the main host may stay private.

## Stored-Token Workflows

For runners without OIDC, the following workflows transfer images through
the main host with a stored token. Apply and deploy separately after push;
prefer OIDC promotion for CI.

### Prerequisites

1. Gordon server running with registry authentication enabled; CI reaches the public Gordon HTTPS domain, not the loopback `server.registry_port`
2. A least-privilege token: `push,pull` for image transfer, plus `admin:apps:read,admin:apps:write` only when the pipeline applies or deploys apps
3. GitLab CI/CD variables configured

### Quick Setup

#### 1. Generate Deployment Token

On your Gordon server:

```bash
gordon auth token generate \
  --subject gitlab-ci \
  --scopes "push,pull,admin:apps:read,admin:apps:write" \
  --expiry 0
```

#### 2. Add CI/CD Variables

In your project: Settings > CI/CD > Variables

| Variable | Value | Protected | Masked |
|----------|-------|-----------|--------|
| `GORDON_TOKEN` | The generated token | Yes | Yes |
| `GORDON_REMOTE` | `https://gordon.example.com` | Yes | No |

#### 3. Create Pipeline

Create `.gitlab-ci.yml`:

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://git.bnema.dev/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon images push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"

  only:
    - tags
```

Gordon auto-detects the version from `$CI_COMMIT_TAG`.

### Pipeline Examples

#### Deploy on Tag Push

```yaml
stages:
  - deploy

deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://git.bnema.dev/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon images push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"

  rules:
    - if: $CI_COMMIT_TAG
```

#### Continuous Deployment

Deploy on every push to main:

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://git.bnema.dev/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon images push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"

  rules:
    - if: $CI_COMMIT_BRANCH == "main"
```

#### Manual Deployment

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://git.bnema.dev/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon images push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"

  when: manual
```

#### With Build Arguments

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://git.bnema.dev/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon images push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"
        --build-arg NODE_ENV=production
        --build-arg API_URL=https://api.example.com

  rules:
    - if: $CI_COMMIT_TAG
```

### Alternative: Docker-based Pipeline

For environments where installing the Gordon binary is not desired:

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  script:
    - echo "$GORDON_TOKEN" | docker login "$GORDON_REGISTRY" -u "$GORDON_USERNAME" --password-stdin
    - docker build -t "$GORDON_REGISTRY/myapp:$CI_COMMIT_TAG" .
    - docker push "$GORDON_REGISTRY/myapp:$CI_COMMIT_TAG"
  rules:
    - if: $CI_COMMIT_TAG
```

This requires additional CI/CD variables: `GORDON_REGISTRY` and `GORDON_USERNAME`.
Pushing only stores the image; deploy explicitly with `gordon apps deploy` afterwards.

### Version Detection

Gordon automatically detects the version tag from GitLab CI environment variables:

| Variable | Example | Result |
|----------|---------|--------|
| `$CI_COMMIT_TAG` | `v1.2.0` | Tag `v1.2.0` |
| No tag | - | Falls back to `git describe` or `latest` |

## Troubleshooting

### Authentication Failed

```text
Error: failed to verify token
```

Verify `GORDON_TOKEN` is correctly set and the token has not been revoked.

### Docker-in-Docker Issues

If using `docker:dind`, ensure the service is properly configured:

```yaml
services:
  - docker:24-dind
variables:
  DOCKER_TLS_CERTDIR: "/certs"
```

### Build Context

GitLab CI clones your repository automatically. The build context defaults to the repository root.

## Related

- [GitHub Actions](./github-actions.md)
- [Generic CI](./generic-ci.md)
- [Deployment Overview](./index.md)
- [Authentication](../config/auth.md)
- [Images Commands](../cli/images.md#gordon-images-push)
