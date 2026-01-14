# Gordon Deploy Action

Build and push container images to a Gordon registry, with automatic deployment on push.

## Features

- Automatic image building from Dockerfile
- Tag-based versioning (uses git tag as image tag)
- Optional `:latest` tag pushing
- Multi-platform builds support
- Build caching with GitHub Actions cache
- Works with Gordon's token and password authentication

## Usage

### Basic Usage (Tag Push)

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
      - uses: actions/checkout@v6

      - name: Deploy to Gordon
        uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: registry.mydomain.com
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
```

### Full Example with All Options

```yaml
name: Deploy to Gordon

on:
  push:
    tags:
      - 'v*'
      - 'release-*'

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      - name: Deploy to Gordon
        id: deploy
        uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: registry.mydomain.com
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          image: myapp                    # Custom image name
          dockerfile: ./docker/Dockerfile # Custom Dockerfile path
          context: .                      # Build context
          build-args: |                   # Build arguments
            NODE_ENV=production
            API_URL=https://api.example.com
          platforms: linux/amd64,linux/arm64  # Multi-platform
          push-latest: 'true'             # Also push :latest

      - name: Print outputs
        run: |
          echo "Image: ${{ steps.deploy.outputs.image }}"
          echo "Tag: ${{ steps.deploy.outputs.tag }}"
          echo "Digest: ${{ steps.deploy.outputs.digest }}"
```

## Inputs

| Input | Description | Required | Default |
|-------|-------------|----------|---------|
| `registry` | Gordon registry URL (e.g., `registry.mydomain.com`) | Yes | - |
| `username` | Registry username (token subject for token auth) | Yes | - |
| `password` | Registry password or token | Yes | - |
| `image` | Image name | No | Repository name |
| `tag` | Override image tag | No | Git tag or short SHA |
| `dockerfile` | Path to Dockerfile | No | `./Dockerfile` |
| `context` | Build context path | No | `.` |
| `build-args` | Build arguments (one per line) | No | - |
| `platforms` | Target platforms | No | - |
| `push-latest` | Also push with `:latest` tag | No | `true` |
| `cache-from` | Cache source | No | `type=gha` |
| `cache-to` | Cache destination | No | `type=gha,mode=max` |

## Outputs

| Output | Description |
|--------|-------------|
| `image` | Full image reference that was pushed |
| `tag` | Tag that was pushed |
| `digest` | Image digest |

## Authentication Setup

### 1. Generate a Gordon Token

On your Gordon server:

```bash
# Generate a never-expiring token for CI
gordon auth token generate --subject github-actions --scopes push,pull --expiry 0
```

### 2. Add GitHub Secrets

In your repository settings, add:

- `GORDON_USERNAME`: The token subject (e.g., `github-actions`)
- `GORDON_TOKEN`: The generated JWT token

### 3. Configure Gordon

Ensure your Gordon config uses token authentication:

```toml
[secrets]
backend = "pass"  # or "sops"

[registry_auth]
enabled = true
type = "token"
token_secret = "gordon/registry/token_secret"
```

## Examples

### Deploy on Tag + Manual Trigger

```yaml
name: Deploy

on:
  push:
    tags:
      - 'v*'
  workflow_dispatch:
    inputs:
      tag:
        description: 'Tag to deploy'
        required: true

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          ref: ${{ github.event.inputs.tag || github.ref }}

      - uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: registry.mydomain.com
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          tag: ${{ github.event.inputs.tag }}
```

### Monorepo with Multiple Services

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
      - uses: actions/checkout@v6
      - uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: registry.mydomain.com
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          image: myapp-api
          dockerfile: ./services/api/Dockerfile
          context: ./services/api

  deploy-web:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: bnema/gordon/.github/actions/deploy@main
        with:
          registry: registry.mydomain.com
          username: ${{ secrets.GORDON_USERNAME }}
          password: ${{ secrets.GORDON_TOKEN }}
          image: myapp-web
          dockerfile: ./services/web/Dockerfile
          context: ./services/web
```

### Build with Secrets (BuildKit)

```yaml
- uses: bnema/gordon/.github/actions/deploy@main
  with:
    registry: registry.mydomain.com
    username: ${{ secrets.GORDON_USERNAME }}
    password: ${{ secrets.GORDON_TOKEN }}
    build-args: |
      NPM_TOKEN=${{ secrets.NPM_TOKEN }}
```

## Troubleshooting

### Authentication Failed

- Verify your token hasn't been revoked: `gordon auth token list`
- Ensure the token has `push` scope
- Check that `token_secret` matches between token generation and validation

### Dockerfile Not Found

- Verify the `dockerfile` input path is correct
- Paths are relative to the repository root

### Push Failed

- Ensure your Gordon server is accessible from GitHub Actions
- Check firewall rules allow connections from GitHub's IP ranges
- Verify the registry domain resolves correctly

## Security Notes

- Never commit tokens or passwords to your repository
- Use GitHub Secrets for all sensitive values
- Consider using short-lived tokens for production
- Each Gordon instance has unique tokens (tokens from one instance won't work on another)
