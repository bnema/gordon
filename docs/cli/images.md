# Images Command

List and prune runtime/registry images.

## gordon images

```bash
gordon images <subcommand>
```

Subcommands:

- `list` - List runtime images and registry tags.
- `prune` - Prune dangling runtime images and old registry tags.
- `tags` - List registry tags for a specific repository.

> **Note:** Images commands require remote mode (`--remote` + `--token`, or configured remotes).

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
