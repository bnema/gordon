# GitLab CI/CD Deployment

Automated deployment with GitLab CI/CD using Gordon's push command.

## Prerequisites

1. Gordon server running with registry authentication enabled
2. Generated deployment token
3. GitLab CI/CD variables configured

## Quick Setup

### 1. Generate Deployment Token

On your Gordon server:

```bash
gordon auth token generate \
  --subject gitlab-ci \
  --scopes "push,pull,admin:routes:read,admin:config:write" \
  --expiry 0
```

### 2. Add CI/CD Variables

In your project: Settings > CI/CD > Variables

| Variable | Value | Protected | Masked |
|----------|-------|-----------|--------|
| `GORDON_TOKEN` | The generated token | Yes | Yes |
| `GORDON_REMOTE` | `https://gordon.example.com` | Yes | No |

### 3. Create Pipeline

Create `.gitlab-ci.yml`:

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"
        --no-confirm
  only:
    - tags
```

Gordon auto-detects the version from `$CI_COMMIT_TAG`.

## Pipeline Examples

### Deploy on Tag Push

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
    - curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"
        --no-confirm
  rules:
    - if: $CI_COMMIT_TAG
```

### Continuous Deployment

Deploy on every push to main:

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"
        --no-confirm
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
```

### Manual Deployment

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"
        --no-confirm
  when: manual
```

### With Build Arguments

```yaml
deploy:
  stage: deploy
  image: docker:24
  services:
    - docker:24-dind
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    - chmod +x /usr/local/bin/gordon
  script:
    - gordon push --build
        --remote "$GORDON_REMOTE"
        --token "$GORDON_TOKEN"
        --build-arg NODE_ENV=production
        --build-arg API_URL=https://api.example.com
        --no-confirm
  rules:
    - if: $CI_COMMIT_TAG
```

## Alternative: Docker-based Pipeline

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
Gordon auto-deploys when it receives the image.

## Version Detection

Gordon automatically detects the version tag from GitLab CI environment variables:

| Variable | Example | Result |
|----------|---------|--------|
| `$CI_COMMIT_TAG` | `v1.2.0` | Tag `v1.2.0` |
| No tag | - | Falls back to `git describe` or `latest` |

## Troubleshooting

### Authentication Failed

```
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
- [Push Command](../cli/push.md)
