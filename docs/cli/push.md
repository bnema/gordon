# Push Command

Tag, push, and optionally deploy an image to the Gordon registry.

## gordon push

### Synopsis

```bash
gordon push [image] [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `[image]` | Image name to push (optional). If omitted, auto-detected from Dockerfile labels or current directory name |

### Options

| Option | Description |
|--------|-------------|
| `--build` | Build the image first using `docker buildx` |
| `-f, --file` | Path to Dockerfile (default: `./Dockerfile`, used with `--build`) |
| `--platform` | Target platform for buildx (default: `linux/amd64`) |
| `--build-arg` | Additional build args (repeatable, `KEY=VALUE`) |
| `--tag` | Override pushed version tag (default: tag ref from CI, then `git describe --tags --dirty`) |
| `--no-confirm` | Skip deploy confirmation prompt |
| `--no-deploy` | Push only; skip deployment prompt |
| `--domain` | Explicit deploy target override |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Description

`gordon push` tags the selected image for the Gordon registry, pushes it, and
optionally deploys matching routes.

Image resolution order:
1. `--domain` is the explicit deploy target override for legacy workflows
2. Positional refs resolve routes by image name; tagged refs still use the image name for lookup
3. Dotted positional refs probe image routes first, then fall back to legacy domain lookup
4. No-arg mode auto-detects from the Dockerfile label or current directory

The pushed version still comes from `--tag`, CI tag refs, or `git describe --tags --dirty`.

To push attachment images (databases, caches, etc.), use `gordon attachments push`.

- For first deploys, run `gordon bootstrap` first to create or update the route, attachments, and secrets, then run `gordon push` to upload and deploy the image.
- The registry and repository are derived from the route image on the server.
- The version tag defaults to a CI tag ref (like `refs/tags/v1.2.3`) when available,
  then falls back to `git describe --tags --dirty` (for example
  `v1.2.3-4-gabc1234` or `v1.2.3-dirty`). If no tag is found, `latest` is used.
- When `--build` is set, the command builds with `docker buildx build --load`
  and injects `VERSION`, `GIT_TAG`, `GIT_SHA`, and `BUILD_TIME` into the build
  environment plus any `--build-arg` values. To use these in your Dockerfile,
  declare them with `ARG` (e.g., `ARG VERSION`) then reference via `ENV` or
  in build steps.
- Use `-f/--file` to build from a Dockerfile outside the current directory root.
- The version tag and `latest` are both pushed (unless the version is `latest`).

### Authentication

When used with `--remote`, gordon push authenticates in two ways:

- **Admin API** (route resolution, deploy): uses `--token` or `$GORDON_TOKEN` as Bearer token
- **Registry push**: automatically exchanges the token for a short-lived (5 min) registry access token via `/auth/token` -- no `docker login` required

This means CI/CD pipelines only need a single secret (`GORDON_TOKEN`).

### Deploy Modes

When the token has `admin:config:write` scope, the CLI manages deployment explicitly
(DeployIntent → push → Deploy). This gives the CLI control over deploy timing.

When the token lacks `admin:config:write`, the CLI pushes the image and the server
auto-deploys when it receives it via its event listener. The CLI logs:
`info: deploy intent skipped (insufficient scope), server will auto-deploy on image receive`

### Version Auto-Detection

Gordon reads version tags from CI environment variables (in priority order):

| CI System | Variable | Example |
|-----------|----------|---------|
| GitHub Actions | `$GITHUB_REF` | `refs/tags/v1.2.0` |
| GitHub Actions | `$GITHUB_REF_TYPE` + `$GITHUB_REF_NAME` | `tag` + `v1.2.0` |
| GitLab CI | `$CI_COMMIT_TAG` | `v1.2.0` |
| Azure DevOps | `$BUILD_SOURCEBRANCH` | `refs/tags/v1.2.0` |
| Any | `git describe --tags` | `v1.2.3-4-gabc1234` |
| Fallback | - | `latest` |

### Examples

```bash
# Build, push, and deploy (auto-detect image name)
gordon push --build --remote https://gordon.example.com --no-confirm

# Push an image and deploy it
gordon push myapp --remote https://gordon.example.com --no-confirm

# Push and deploy to an explicit domain
gordon push --domain app.example.com --remote https://gordon.example.com --no-confirm

# Tagged refs still resolve routes by image name
gordon push myapp:v1.2.3 --tag v1.2.3 --no-deploy

# Push existing local image, skip deploy
gordon push myapp --tag v1.2.0 --no-deploy

# Build for ARM and pass build args
gordon push myapp --build --platform linux/arm64 --build-arg CGO_ENABLED=0

# Build from a custom Dockerfile path
gordon push myapp --build -f docker/app/Dockerfile

# Legacy compatibility: domain-looking positional target
gordon push app.example.com --no-confirm

# CI/CD usage (single env var, no docker login needed)
export GORDON_TOKEN="your-token"
gordon push myapp --build --remote https://gordon.example.com --no-confirm
```

### Notes

- Remote mode required. See [CLI Overview](./index.md) for targeting options.
- `gordon push` requires the target route to already exist so it can resolve the deploy target.
  Use `gordon bootstrap` for first deploys.
- `--build` requires Docker with Buildx. Docker Desktop includes it; on Linux,
  install the `docker-buildx-plugin` package.
- Gordon uses native registry uploads instead of shelling out to `docker push`.
  Image layers are sent in 50MB chunks, which stays under Cloudflare's 100MB
  per-request limit so proxied pushes keep working. Keep the server's
  `max_blob_chunk_size` larger than the client chunk size; the default `95MB`
  works out of the box.

## Related

- [CLI Overview](./index.md)
- [Deployment Overview](../deployment/index.md)
- [GitHub Actions](../deployment/github-actions.md)
- [Attachments Commands](./attachments.md)
- [Routes Command](./routes.md)
- [Authentication](../config/auth.md)
