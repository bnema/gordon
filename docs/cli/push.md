# Push Command

Tag, push, and optionally deploy an image to the Gordon registry.

## gordon push

### Synopsis

```bash
gordon push <domain> [options]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The route domain to resolve the registry and repository from |

### Options

| Option | Description |
|--------|-------------|
| `--build` | Build the image first using `docker buildx` |
| `--platform` | Target platform for buildx (default: `linux/amd64`) |
| `--build-arg` | Additional build args (repeatable, `KEY=VALUE`) |
| `--tag` | Override version tag (default: `git describe --tags --abbrev=0`) |
| `--no-confirm` | Skip deploy confirmation prompt |
| `--no-deploy` | Push only; skip deployment prompt |
| `--remote` | Remote Gordon URL |
| `--token` | Authentication token for remote |

### Description

`gordon push` resolves the route image for `<domain>`, tags the image for the
Gordon registry, pushes it, and optionally deploys it.

- The registry and repository are derived from the route image on the server.
- The version tag defaults to the latest git tag. If no tag is found, `latest`
  is used.
- When `--build` is set, the command uses `docker buildx build --push` and
  injects `VERSION=<tag>` plus any `--build-arg` values.
- The version tag and `latest` are both pushed (unless the version is `latest`).

### Examples

```bash
# Build, push, and deploy
gordon push myapp.example.com --build

# Push existing local image, skip deploy
gordon push myapp.example.com --tag v1.2.0 --no-deploy

# Push and deploy without confirmation
gordon push myapp.example.com --no-confirm

# Build for ARM and pass build args
gordon push myapp.example.com --build --platform linux/arm64 --build-arg CGO_ENABLED=0
```

### Notes

- Remote mode required. See [CLI Overview](./index.md) for targeting options.
- `--build` requires Docker with Buildx available. Docker Desktop includes Buildx;
  on Linux, install the `docker-buildx-plugin` package.
- `docker buildx` must be available when using `--build`.

## Related

- [CLI Overview](./index.md)
- [Routes Command](./routes.md)
- [Serve Command](./serve.md)
