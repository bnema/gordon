# Generic CI/CD Deployment

Deploy with Gordon from any CI/CD system.

## Requirements

- Docker available on CI runner (for building images)
- Network access to your Gordon server (HTTPS)
- Gordon binary (optional but recommended)

## Recommended: gordon push

### 1. Generate Deployment Token

On your Gordon server:

```bash
gordon auth token generate \
  --subject ci-deploy \
  --scopes "push,pull,admin:routes:read,admin:config:write" \
  --expiry 0
```

### 2. Configure CI Secrets

Add these secrets to your CI system:

| Secret | Value |
|--------|-------|
| `GORDON_TOKEN` | The generated token |
| `GORDON_REMOTE` | `https://gordon.example.com` |

### 3. Pipeline Steps

```bash
# 1. Install Gordon binary
curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 \
  -o /usr/local/bin/gordon
chmod +x /usr/local/bin/gordon

# 2. Build, push, and deploy
export GORDON_TOKEN="$GORDON_TOKEN"
gordon push --build \
  --remote "$GORDON_REMOTE" \
  --no-confirm
```

Gordon auto-detects the version from CI environment variables:

| CI System | Variable | Example |
|-----------|----------|---------|
| GitHub Actions | `$GITHUB_REF` | `refs/tags/v1.2.0` |
| GitLab CI | `$CI_COMMIT_TAG` | `v1.2.0` |
| Azure DevOps | `$BUILD_SOURCEBRANCH` | `refs/tags/v1.2.0` |
| Other | `--tag` flag | `gordon push --tag v1.2.0` |

## Alternative: docker push

If installing the Gordon binary is not possible:

```bash
# 1. Login to Gordon registry
echo "$GORDON_TOKEN" | docker login gordon.example.com \
  -u ci-deploy --password-stdin

# 2. Build and push
docker build -t gordon.example.com/myapp:v1.0.0 .
docker push gordon.example.com/myapp:v1.0.0
```

This requires the token subject (`ci-deploy`) as the username.
Gordon auto-deploys when it receives the image.

## CI System Examples

### Jenkins

```groovy
pipeline {
    agent any
    environment {
        GORDON_TOKEN = credentials('gordon-token')
        GORDON_REMOTE = 'https://gordon.example.com'
    }
    stages {
        stage('Deploy') {
            steps {
                sh '''
                    curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 \
                      -o /usr/local/bin/gordon
                    chmod +x /usr/local/bin/gordon
                    gordon push --build --remote "$GORDON_REMOTE" --no-confirm
                '''
            }
        }
    }
}
```

### CircleCI

```yaml
version: 2.1

jobs:
  deploy:
    docker:
      - image: docker:24
    steps:
      - checkout
      - setup_remote_docker
      - run:
          name: Install Gordon
          command: |
            apk add --no-cache curl
            curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 \
              -o /usr/local/bin/gordon
            chmod +x /usr/local/bin/gordon
      - run:
          name: Deploy
          command: |
            gordon push --build \
              --remote "$GORDON_REMOTE" \
              --no-confirm

workflows:
  deploy:
    jobs:
      - deploy:
          filters:
            tags:
              only: /^v.*/
            branches:
              ignore: /.*/
```

### Drone

```yaml
kind: pipeline
type: docker
name: deploy

steps:
  - name: deploy
    image: docker:24
    environment:
      GORDON_TOKEN:
        from_secret: gordon_token
      GORDON_REMOTE:
        from_secret: gordon_remote
    commands:
      - apk add --no-cache curl
      - curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64
          -o /usr/local/bin/gordon
      - chmod +x /usr/local/bin/gordon
      - gordon push --build --remote "$GORDON_REMOTE" --no-confirm

trigger:
  event:
    - tag
```

## Token Scopes Reference

| Workflow | Required Scopes |
|----------|----------------|
| Build + push + deploy | `push,pull,admin:routes:read,admin:config:write` |
| Push only (no deploy) | `push,pull,admin:routes:read` |
| docker push (auto-deploy) | `push,pull` |

## Troubleshooting

### "failed to verify token"

The token is invalid or has been revoked. Generate a new one.

### "no route configured for image"

The route must exist before pushing. Create it with:

```bash
gordon routes add myapp.example.com myapp
# or for first deploy:
gordon bootstrap myapp.example.com myapp
```

### Version shows "latest"

Gordon could not detect a version tag. Either:
- Tag your git repo: `git tag v1.0.0 && git push --tags`
- Pass explicitly: `gordon push --tag v1.0.0`

### Large images time out

Gordon uploads in 50MB chunks. For very large images (> 1GB), the push may take several minutes. Ensure your CI runner timeout is sufficient.

## Related

- [GitHub Actions](./github-actions.md)
- [GitLab CI](./gitlab-ci.md)
- [Deployment Overview](./index.md)
- [Authentication](../config/auth.md)
- [Push Command](../cli/push.md)
