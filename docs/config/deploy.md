# Deploy Configuration

Controls how Gordon pulls images during deployment.

## Example

```toml
[deploy]
pull_policy = "if-tag-changed"
readiness_delay = "5s"
```

## Settings

### `deploy.pull_policy`

How to decide when to pull an image:

- `always`: always pull, even if the tag exists locally.
- `if-not-present`: only pull when the tag is missing locally.
- `if-tag-changed`: pull for tag references to check for updates; skip pulls for digest references (`@sha256:...`).

Default: `if-tag-changed`

### `deploy.readiness_delay`

How long Gordon waits after a container first reports `running` before it is
considered ready for traffic.

- Uses Go duration format (examples: `"5s"`, `"15s"`, `"1m"`).
- If the container briefly exits right after this delay, Gordon now waits up to
  30 additional seconds for it to recover before failing the deploy.

Default: `"5s"`

## Notes

- Changes to `deploy.pull_policy` require a restart to take effect.
- Changes to `deploy.readiness_delay` require a restart to take effect.
