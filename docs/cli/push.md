# Push Command

Tag and push an image to the Gordon registry. Push transfers OCI content
only: it never deploys. Deploy separately with `gordon apps deploy` after
applying the manifest that references the pushed tag.

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
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Description

`gordon push` tags the selected image for the Gordon registry and pushes it.

If you do not pass `--remote` and no active remote is configured, Gordon tries to
infer the correct saved remote by probing your saved remotes for the image.
It auto-selects only when exactly one remote matches. If multiple remotes match,
or a saved remote cannot be probed safely, the command stops and asks you to use
`--remote` explicitly.

Domain-style push targets are retired: push an image name, then
`gordon apps apply` the manifest that references the pushed tag.

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

- **Admin API** (tag listing for inference): uses `--token` or `$GORDON_TOKEN` as Bearer token
- **Registry push**: automatically exchanges the token for a short-lived (5 min) registry access token via `/auth/token` -- no `docker login` required

This means CI/CD pipelines only need a single secret (`GORDON_TOKEN`).

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
# Build, push (auto-detect image name)
gordon push --build --remote https://gordon.example.com

# Push an image
gordon push myapp --remote https://gordon.example.com

# Push a fully qualified ref
gordon push registry.example.com/myapp:v1.2.3 --build --remote https://gordon.example.com

# Push existing local image with an explicit tag
gordon push myapp --tag v1.2.0 --remote https://gordon.example.com

# Build for ARM and pass build args
gordon push myapp --build --platform linux/arm64 --build-arg CGO_ENABLED=0 --remote https://gordon.example.com

# Build from a custom Dockerfile path
gordon push myapp --build -f docker/app/Dockerfile --remote https://gordon.example.com

# CI/CD usage (single env var, no docker login needed)
export GORDON_TOKEN="your-token"
gordon push myapp --build --remote https://gordon.example.com
```

### Notes

- Remote mode required. See [CLI Overview](./index.md) for targeting options.
- `--build` requires Docker with Buildx. Docker Desktop includes it; on Linux,
  install the `docker-buildx-plugin` package.
- Gordon uses native registry uploads instead of shelling out to `docker push`.
  Image layers are sent in 50MB chunks, which stays under Cloudflare's 100MB
  per-request limit so proxied pushes keep working. Keep the server's
  `max_blob_chunk_size` larger than the client chunk size; the default `95MB`
  works out of the box.

## Related

- [CLI Overview](./index.md)
- [Authentication](../config/auth.md)
