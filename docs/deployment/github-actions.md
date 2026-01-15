# GitHub Actions Deployment

Automated deployment with GitHub Actions using Gordon's official action.

## Prerequisites

1. Gordon server running with registry authentication enabled
2. Generated deployment token
3. GitHub repository secrets configured

## Quick Setup

### 1. Generate Deployment Token

On your Gordon server:

```bash
gordon auth token generate --subject github-actions --scopes push,pull --expiry 0
```

Save the token output.

### 2. Add GitHub Secrets

In your repository: Settings → Secrets → Actions → New repository secret

| Secret | Value |
|--------|-------|
| `GORDON_REGISTRY` | `registry.mydomain.com` |
| `GORDON_USERNAME` | `github-actions` |
| `GORDON_TOKEN` | The generated token |

### 3. Create Workflow

Create `.github/workflows/deploy.yml`:

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

### 4. Deploy

```bash
git tag v1.0.0
git push origin v1.0.0
```

## Action Reference

### Inputs

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

### Outputs

| Output | Description |
|--------|-------------|
| `image` | Full image name |
| `tag` | Image tag |

## Workflow Examples

### Deploy on Tag Push

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

### Continuous Deployment

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

### Manual Deployment

Allow manual deployments with custom tag:

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

### Monorepo Deployment

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

### With Build Arguments

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

### Multi-Platform Build

```yaml
- uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: ${{ secrets.GORDON_REGISTRY }}
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}
    platforms: linux/amd64,linux/arm64
```

## Deployment Summary

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

- [Registry Authentication](../config/registry-auth.md)
- [Deployment Overview](./index.md)
- [Rollback](./rollback.md)
