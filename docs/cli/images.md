# Images Command

Push, list, and prune runtime/registry images.

## gordon images

```bash
gordon images <subcommand>
```

Subcommands:

- `push` - Tag and push an image to the Gordon registry (never deploys).
- `list` - List runtime images and registry tags.
- `prune` - Prune dangling runtime images and old registry tags.
- `tags` - List registry tags for a specific repository.

> **Note:** Images commands require remote mode (`--remote` + `--token`, or configured remotes).

## gordon images push

Tag and push an image to the Gordon registry. Push transfers OCI content
only: it never deploys. Deploy separately with `gordon apps deploy` after
applying the manifest that references the pushed tag.

### Synopsis

```bash
gordon images push [image] [options]
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

`gordon images push` tags the selected image for the Gordon registry and pushes it.

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

When used with `--remote`, gordon images push authenticates in two ways:

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
gordon images push --build --remote https://gordon.example.com

# Push an image
gordon images push myapp --remote https://gordon.example.com

# Push a fully qualified ref
gordon images push registry.example.com/myapp:v1.2.3 --build --remote https://gordon.example.com

# Push existing local image with an explicit tag
gordon images push myapp --tag v1.2.0 --remote https://gordon.example.com

# Build for ARM and pass build args
gordon images push myapp --build --platform linux/arm64 --build-arg CGO_ENABLED=0 --remote https://gordon.example.com

# Build from a custom Dockerfile path
gordon images push myapp --build -f docker/app/Dockerfile --remote https://gordon.example.com

# CI/CD usage (single env var, no docker login needed)
export GORDON_TOKEN="your-token"
gordon images push myapp --build --remote https://gordon.example.com
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

## gordon images list

```bash
gordon images list
gordon images list --json
```

Shows image rows with repository, tag, size, creation time, image id, and dangling status.
Rows that exist only in the registry (not currently present in the runtime cache) are included with unavailable runtime fields shown as `-`.

Flags:

| Flag | Description |
|------|-------------|
| `--json` | Output images as JSON |

### JSON Output

```bash
gordon images list --json
```

```json
[
  {
    "repository": "myapp",
    "tag": "latest",
    "size": "148MB",
    "created_at": "2026-03-13T10:15:00Z",
    "image_id": "sha256:abc123def456",
    "dangling": false
  }
]
```

## gordon images prune

```bash
gordon images prune [--dry-run] [--keep-releases <n>] [--dangling] [--registry] [--no-confirm]
```

By default, prune removes dangling runtime images **and** applies registry tag retention (keeping `latest` + 3 previous non-`latest` tags per repository). A confirmation prompt is shown before destructive operations.

A prune only ever deletes resources it can positively prove safe. Every other candidate is reported as `protected` or `unknown` and left in place; the command still succeeds. See [Prune safety](#prune-safety).

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Run the full inventory and planning, then report; nothing is deleted |
| `--keep-releases` | `3` | Number of previous non-`latest` tags to keep per repository (`latest` is always preserved) |
| `--dangling` | `false` | Restrict scope to dangling runtime images only |
| `--registry` | `false` | Restrict scope to registry tag retention only |
| `--no-confirm` | `false` | Skip the confirmation prompt |

### Scope resolution

- **No scope flags** (default): both runtime and registry cleanup run.
- **`--dangling`**: only runtime dangling images are pruned; registry is skipped.
- **`--registry`**: only registry tag retention runs; runtime is skipped.
- **`--dangling --registry`**: both scopes run (same as default, but explicit).

### Retention semantics

- `latest` is always preserved when present.
- `--keep-releases` counts non-`latest` tags, ordered by most recent first.
- A tag named by durable app state survives beyond the retention window.
- `--keep-releases=0` skips registry tag and blob cleanup entirely; dangling runtime prune still runs.

## Prune safety

Gordon prunes by ownership, not by name or age. Each candidate gets one verdict:

| Verdict | Meaning |
|---------|---------|
| `eligible` | Every fact needed to prove the resource safe was read completely, and no durable record claims it. It is deleted. |
| `protected` | A durable fact claims it: a desired or active app service (including stopped apps), a recovery inhibition, a staged or committed apply intent, an unfinished operation journal entry, container use, a pending upload, an ownership record, or the OCI closure of any of those. It survives. |
| `unknown` | A fact needed to prove it safe was missing, unreadable, or unsupported. It survives. |

Runtime images used by any container, running or stopped, always survive. Registry blobs reachable from a retained or protected manifest survive, including blobs shared with another tag. When a retained manifest cannot be read, the blobs whose safety depended on it become `unknown` rather than eligible.

Both `--dry-run` and the executed prune return the same report: the verdict counts, the deleted identities, any deletion failures, every inventory gap, and the skipped candidates with their reason codes. A prune that deletes nothing is a success.

## gordon images tags

```bash
gordon images tags <repository>
gordon images tags <repository> --json
```

Lists all tags in the Gordon registry for the specified repository.

Flags:

| Flag | Description |
|------|-------------|
| `--json` | Output repository and tags as JSON |

### JSON Output

```bash
gordon images tags myapp --json
```

```json
{
  "repository": "myapp",
  "tags": ["latest", "v1.2.0", "v1.1.0"]
}
```

## Examples

```bash
# List images
gordon images list

# Prune everything with defaults (dangling + registry, keep latest + 3)
gordon images prune

# List tags for a repository
gordon images tags myapp

# Prune dangling runtime images only
gordon images prune --dangling

# Prune registry tags only, keeping latest + 5 previous
gordon images prune --registry --keep-releases 5

# Inspect what prune would remove without applying
# (also lists every skipped candidate with its reason)
gordon images prune --dry-run

# Skip confirmation prompt
gordon images prune --no-confirm
```

## Required Permissions

- The `list` subcommand requires `admin:status:read`.
- The `prune` subcommand requires `admin:config:write`.
- The `tags` subcommand requires `admin:status:read`.

## Related

- [CLI Commands](./index.md)
- [Images Configuration](../config/images.md)
