# Deploy Configuration

Controls how Gordon pulls images during deployment.

## Example

```toml
[deploy]
pull_policy = "if-tag-changed"
```

## Settings

### `deploy.pull_policy`

How to decide when to pull an image:

- `always`: always pull, even if the tag exists locally.
- `if-not-present`: only pull when the tag is missing locally.
- `if-tag-changed`: pull for tag references to check for updates; skip pulls for digest references (`@sha256:...`).

Default: `if-tag-changed`

## Notes

- Changes to `deploy.pull_policy` require a restart to take effect.
