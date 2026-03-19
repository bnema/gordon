# Preview Environments

Push a branch, get a URL. Preview environments give every branch or pull request its own isolated deployment — automatically torn down when it expires.

Gordon provisions a preview environment when a matching image tag is pushed to the registry. Each preview gets its own subdomain derived from the base route, lives for a configurable TTL, and is cleaned up automatically when the TTL expires.

## Configuration

```toml
[auto.preview]
enabled = true
ttl = "48h"
separator = "--"
tag_patterns = ["preview-*", "pr-*"]
data_copy = true
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `false` | Enable automatic preview environment creation |
| `ttl` | duration string | `"48h"` | How long a preview environment lives before automatic teardown |
| `separator` | string | `"--"` | String inserted between the base domain and the branch slug |
| `tag_patterns` | string array | `[]` | Glob patterns matched against image tags to trigger preview creation |
| `data_copy` | bool | `true` | Clone the production route's volumes into the preview environment on creation |

### `ttl` Format

The `ttl` field accepts Go duration strings:

| Value | Meaning |
|-------|---------|
| `"24h"` | 24 hours |
| `"48h"` | 48 hours (default) |
| `"7d"` | 7 days |
| `"0"` | Never expire (manual deletion only) |

### `tag_patterns` Matching

Patterns use standard glob syntax. A tag must match at least one pattern in the list for a preview environment to be created.

| Pattern | Matches |
|---------|---------|
| `"preview-*"` | `preview-my-feature`, `preview-fix-123` |
| `"pr-*"` | `pr-42`, `pr-123` |
| `"feat/*"` | `feat/new-api`, `feat/dark-mode` |

## Naming Scheme

Preview domains are derived from the base route for the deployed image. Gordon combines the base domain, the separator, and a URL-safe slug of the image tag.

```
<base-domain><separator><tag-slug>
```

For example, with `separator = "--"` and base route `app.example.com`:

| Image Tag | Preview Domain |
|-----------|----------------|
| `preview-my-feature` | `my-feature--app.example.com` |
| `pr-42` | `pr-42--app.example.com` |
| `preview-fix-login` | `fix-login--app.example.com` |

The tag prefix matched by `tag_patterns` is stripped to keep domains short. Slashes in tags are replaced with hyphens.

## CLI Usage

### List Preview Environments

```bash
gordon preview list
```

Shows all active preview environments, their domains, TTL remaining, and source route.

### Create or Refresh a Preview

Previews are created automatically on push. To manually create or reset the TTL of an existing preview:

```bash
gordon preview create app.example.com --tag preview-my-feature
```

### Extend a Preview

Reset the TTL on an existing preview without redeploying:

```bash
gordon preview extend my-feature--app.example.com
gordon preview extend my-feature--app.example.com --ttl 24h
```

Without `--ttl`, the configured default TTL is applied from the current time.

### Delete a Preview

```bash
gordon preview delete my-feature--app.example.com
```

Stops the container, removes the route, and (unless `volumes.preserve = true`) removes any cloned volumes.

## CI Usage

### GitHub Actions

Use tag-based deploys to trigger preview environments automatically.

```yaml
name: Preview Environment

on:
  pull_request:
    types: [opened, synchronize]

jobs:
  deploy-preview:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Log in to Gordon registry
        run: echo "${{ secrets.GORDON_TOKEN }}" | docker login ${{ vars.GORDON_REGISTRY }} -u deploy --password-stdin

      - name: Build and push preview image
        env:
          TAG: pr-${{ github.event.pull_request.number }}
        run: |
          docker build -t ${{ vars.GORDON_REGISTRY }}/myapp:$TAG .
          docker push ${{ vars.GORDON_REGISTRY }}/myapp:$TAG

      - name: Comment preview URL
        uses: actions/github-script@v7
        with:
          script: |
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner,
              repo: context.repo.repo,
              body: `Preview deployed: https://pr-${{ github.event.pull_request.number }}--app.example.com`
            })
```

Configure Gordon to match the `pr-*` tag convention:

```toml
[auto.preview]
enabled = true
ttl = "72h"
tag_patterns = ["pr-*"]
```

### Cleanup on PR Close

```yaml
name: Teardown Preview

on:
  pull_request:
    types: [closed]

jobs:
  teardown:
    runs-on: ubuntu-latest
    steps:
      - name: Delete preview environment
        env:
          TAG: pr-${{ github.event.pull_request.number }}
        run: |
          gordon --server ${{ vars.GORDON_SERVER }} preview delete ${TAG}--app.example.com
```

## Lifecycle

### Creation

When a push matches a `tag_patterns` entry:

1. Gordon creates a new route: `<slug>--<base-domain>` → pushed image
2. If `data_copy = true`, volumes from the base route are cloned into the preview
3. The preview TTL timer starts
4. The container is deployed with zero-downtime rules disabled (previews are always cold-starts)

### TTL and Automatic Teardown

Gordon checks preview TTLs on a background schedule. When a preview expires:

1. In-flight connections are drained
2. The container is stopped and removed
3. The route is removed from the proxy
4. Cloned volumes are removed (unless `volumes.preserve = true`)

Use `gordon preview extend` to reset the timer without redeploying.

### Volume Cloning

When `data_copy = true`, Gordon copies the named volumes attached to the base route into fresh volumes for the preview. This gives the preview a realistic dataset without sharing state with production.

- Cloned volumes are prefixed with the preview slug
- Cloned volumes are removed on preview teardown unless `volumes.preserve = true`
- Set `data_copy = false` to start previews with empty volumes (faster creation, no production data)

## Example Config

```toml
[server]
gordon_domain = "registry.example.com"

[auto_route]
enabled = true

[auto.preview]
enabled = true
ttl = "48h"
separator = "--"
tag_patterns = ["preview-*", "pr-*"]
data_copy = true

[routes]
"app.example.com" = "myapp:latest"
"api.example.com" = "myapi:latest"

[volumes]
auto_create = true
preserve = false
```

With this config:
- Pushing `myapp:pr-99` creates `pr-99--app.example.com` with cloned volumes
- Pushing `myapi:preview-auth-refactor` creates `preview-auth-refactor--api.example.com`
- Both previews expire after 48 hours and volumes are removed on teardown

## Related

- [Routes](./routes.md)
- [Auto Route](./auto-route.md)
- [Volumes](./volumes.md)
- [Configuration Overview](./index.md)
