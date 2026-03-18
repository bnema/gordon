# GitHub Actions Deployment

Two approaches for deploying with GitHub Actions: the `gordon push` CLI (recommended) or a Docker-based workflow using Gordon's official action.

## Prerequisites

1. Gordon server running with registry authentication enabled
2. Deployment token generated with the required scopes
3. GitHub repository secrets configured

## Recommended: gordon push

Use the `gordon push` CLI for a lightweight, single-step build and deploy.

### 1. Generate Token

On your Gordon server:

```bash
gordon auth token generate \
  --subject github-actions \
  --scopes "push,pull,admin:routes:read,admin:config:write" \
  --expiry 0
```

Save the token output.

### 2. Add GitHub Secrets

In your repository: Settings → Secrets → Actions → New repository secret

| Secret | Value |
|--------|-------|
| `GORDON_TOKEN` | The generated token |
| `GORDON_REMOTE` | `https://gordon.example.com` |

### 3. Install Gordon Binary

Add this step to your workflow to download the Gordon binary:

```yaml
- name: Install Gordon
  run: |
    curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
    chmod +x /usr/local/bin/gordon
```

### 4. Workflow Examples

#### Deploy on Tag Push

Gordon auto-detects the version from `$GITHUB_REF` (e.g., `refs/tags/v1.2.0` → `v1.2.0`).

```yaml
name: Deploy to Gordon

on:
  push:
    tags: ['v*']

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install Gordon
        run: |
          curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
          chmod +x /usr/local/bin/gordon

      - name: Build and Deploy
        env:
          GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
        run: |
          gordon push --build \
            --remote ${{ secrets.GORDON_REMOTE }} \
            --no-confirm
```

#### Continuous Deploy on Main

Deploy on every push to main. The version defaults to `latest` or the output of `git describe`.

```yaml
name: Deploy to Gordon

on:
  push:
    branches:
      - main

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Install Gordon
        run: |
          curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
          chmod +x /usr/local/bin/gordon

      - name: Build and Deploy
        env:
          GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
        run: |
          gordon push --build \
            --remote ${{ secrets.GORDON_REMOTE }} \
            --tag latest \
            --no-confirm
```

#### Manual Dispatch

Allow manual deployments with an optional tag input:

```yaml
name: Deploy to Gordon

on:
  workflow_dispatch:
    inputs:
      tag:
        description: 'Tag to deploy (leave empty to use git describe)'
        required: false

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.inputs.tag || github.ref }}
          fetch-depth: 0

      - name: Install Gordon
        run: |
          curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
          chmod +x /usr/local/bin/gordon

      - name: Build and Deploy
        env:
          GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
        run: |
          gordon push --build \
            --remote ${{ secrets.GORDON_REMOTE }} \
            ${{ github.event.inputs.tag && format('--tag {0}', github.event.inputs.tag) || '' }} \
            --no-confirm
```

#### Monorepo

Deploy multiple services with separate `gordon push` calls:

```yaml
name: Deploy Services

on:
  push:
    tags: ['v*']

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install Gordon
        run: |
          curl -fsSL https://github.com/bnema/gordon/releases/latest/download/gordon_linux_amd64 -o /usr/local/bin/gordon
          chmod +x /usr/local/bin/gordon

      - name: Deploy API
        env:
          GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
        run: |
          gordon push --build \
            --remote ${{ secrets.GORDON_REMOTE }} \
            --file ./services/api/Dockerfile \
            myapp-api \
            --no-confirm

      - name: Deploy Web
        env:
          GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
        run: |
          gordon push --build \
            --remote ${{ secrets.GORDON_REMOTE }} \
            --file ./services/web/Dockerfile \
            myapp-web \
            --no-confirm
```

#### With Build Args

Pass build arguments to the Docker build:

```yaml
- name: Build and Deploy
  env:
    GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
  run: |
    gordon push --build \
      --remote ${{ secrets.GORDON_REMOTE }} \
      --build-arg NODE_ENV=production \
      --build-arg API_URL=https://api.example.com \
      --build-arg BUILD_DATE=${{ github.event.head_commit.timestamp }} \
      --no-confirm
```

## Alternative: Docker-based Workflow

For environments where installing the Gordon binary is not desired, use Docker directly to build and push to the Gordon registry. Gordon auto-deploys when it receives the image — no explicit deploy step is needed.

### Setup

Add these secrets to your repository:

| Secret | Value |
|--------|-------|
| `GORDON_REGISTRY` | `registry.gordon.example.com` |
| `GORDON_USERNAME` | `github-actions` |
| `GORDON_TOKEN` | The generated token |

### Workflow

```yaml
name: Deploy to Gordon

on:
  push:
    tags: ['v*']

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Login to Gordon Registry
        run: echo "${{ secrets.GORDON_TOKEN }}" | docker login ${{ secrets.GORDON_REGISTRY }} -u ${{ secrets.GORDON_USERNAME }} --password-stdin

      - name: Build and Push
        run: |
          docker build -t ${{ secrets.GORDON_REGISTRY }}/myapp:${{ github.ref_name }} .
          docker push ${{ secrets.GORDON_REGISTRY }}/myapp:${{ github.ref_name }}
```

## Alternative: Gordon GitHub Action

Use the official `bnema/gordon` action for a fully declarative workflow. This action handles login, build, and push in a single step.

### Setup

Requires the same 3 secrets as the Docker-based workflow: `GORDON_REGISTRY`, `GORDON_USERNAME`, `GORDON_TOKEN`.

### Workflow Examples

#### Deploy on Tag Push

```yaml
name: Deploy to Gordon

on:
  push:
    tags:
      - 'v*'

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Deploy to Gordon
        uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: ${{ secrets.GORDON_REGISTRY }}
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
```

#### Continuous Deployment

Deploy on every push to main:

```yaml
name: Deploy to Gordon

on:
  push:
    branches:
      - main

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Deploy to Gordon
        uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: ${{ secrets.GORDON_REGISTRY }}
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          push-latest: 'true'
```

#### Manual Deployment

Allow manual deployments with a custom tag:

```yaml
name: Deploy to Gordon

on:
  workflow_dispatch:
    inputs:
      tag:
        description: 'Tag to deploy'
        required: false

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.inputs.tag || github.ref }}

      - name: Deploy to Gordon
        uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: ${{ secrets.GORDON_REGISTRY }}
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          tag: ${{ github.event.inputs.tag }}
```

#### Monorepo Deployment

Deploy multiple services:

```yaml
name: Deploy Services

on:
  push:
    tags:
      - 'v*'

jobs:
  deploy-api:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: ${{ secrets.GORDON_REGISTRY }}
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          image: myapp-api
          dockerfile: ./services/api/Dockerfile
          context: ./services/api

  deploy-web:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: ${{ secrets.GORDON_REGISTRY }}
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          image: myapp-web
          dockerfile: ./services/web/Dockerfile
          context: ./services/web
```

#### With Build Arguments

```yaml
- uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: ${{ secrets.GORDON_REGISTRY }}
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}
    build-args: |
      NODE_ENV=production
      API_URL=https://api.example.com
      BUILD_DATE=${{ github.event.head_commit.timestamp }}
```

#### Multi-Platform Build

```yaml
- uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: ${{ secrets.GORDON_REGISTRY }}
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}
    platforms: linux/amd64,linux/arm64
```

### Action Reference

#### Inputs

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `registry` | Yes | - | Gordon registry URL |
| `username` | Yes | - | Registry username |
| `password` | Yes | - | Registry token |
| `image` | No | repo name | Image name |
| `tag` | No | git tag/SHA | Image tag |
| `dockerfile` | No | `./Dockerfile` | Dockerfile path |
| `context` | No | `.` | Build context |
| `push-latest` | No | `false` | Also push :latest tag |
| `platforms` | No | `linux/amd64` | Target platforms |
| `build-args` | No | - | Build arguments |

#### Outputs

| Output | Description |
|--------|-------------|
| `image` | Full image name |
| `tag` | Image tag |

#### Deployment Summary

Add a deployment summary to your workflow:

```yaml
- name: Deploy to Gordon
  id: deploy
  uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: ${{ secrets.GORDON_REGISTRY }}
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}

- name: Deployment Summary
  run: |
    echo "## Deployment Complete" >> $GITHUB_STEP_SUMMARY
    echo "" >> $GITHUB_STEP_SUMMARY
    echo "**Image:** \`${{ steps.deploy.outputs.image }}\`" >> $GITHUB_STEP_SUMMARY
    echo "**Tag:** \`${{ steps.deploy.outputs.tag }}\`" >> $GITHUB_STEP_SUMMARY
```

## Troubleshooting

### Authentication Failed

```
Error: unauthorized: authentication required
```

**Solution:** Verify secrets are correctly set and token has `push` scope.

### Image Not Found After Push

**Solution:** Ensure Gordon config has a route for the pushed image.

### Build Fails

**Solution:** Check Dockerfile path and build context settings.

## Related

- [GitLab CI](./gitlab-ci.md)
- [Generic CI](./generic-ci.md)
- [Deployment Overview](./index.md)
- [Authentication](../config/auth.md)
- [Push Command](../cli/push.md)
- [Rollback](./rollback.md)
