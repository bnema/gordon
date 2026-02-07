# Deploy Configuration

Controls how Gordon pulls images during deployment.

## Example

```toml
[deploy]
pull_policy = "if-tag-changed"
readiness_mode = "auto"
health_timeout = "90s"
readiness_delay = "5s"
drain_mode = "auto"
drain_timeout = "30s"
drain_delay = "2s"
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

### `deploy.readiness_mode`

How Gordon determines that a newly started container is ready for traffic.

- `auto`: use Docker health status if the container has a healthcheck; otherwise
  fall back to `deploy.readiness_delay`.
- `docker-health`: require a Docker healthcheck and wait for `healthy`.
  Deploy fails with `no healthcheck detected` if absent.
- `delay`: always use `deploy.readiness_delay`.

Default: `"auto"`

### `deploy.health_timeout`

Maximum time Gordon waits for health-based readiness in `auto`/`docker-health`
mode before failing the deploy.

- Uses Go duration format (examples: `"30s"`, `"90s"`, `"2m"`).
- Default `"90s"` balances typical app startup time with fast failure feedback.
- Increase for slow cold-starts, migrations, or heavy warmup workloads.

Default: `"90s"`

### `deploy.drain_delay`

How long Gordon waits after synchronous proxy cache invalidation before stopping
the previous container during zero-downtime replacement.

- Uses Go duration format (examples: `"2s"`, `"10s"`, `"1m"`).
- Applied only when a previous container exists and cache invalidation was
  triggered for the deployed domain.

Default: `"2s"`

### `deploy.drain_mode`

How Gordon decides when it is safe to stop the previous container after routing
traffic to the new one.

- `auto`: wait for in-flight proxy requests to drain when available; otherwise
  fall back to `deploy.drain_delay`.
- `inflight`: wait for in-flight proxy requests on the previous container to
  reach zero (bounded by `deploy.drain_timeout`).
- `delay`: always use `deploy.drain_delay`.

Default: `"auto"`

### `deploy.drain_timeout`

Maximum time Gordon waits for in-flight request drain before continuing with old
container shutdown.

- Uses Go duration format (examples: `"10s"`, `"30s"`, `"1m"`).
- Default `"30s"` is a practical upper bound for most HTTP workloads.
- Increase for long-lived requests (large uploads, SSE, long polling).

Default: `"30s"`

## Notes

- Changes to `deploy.pull_policy` require a restart to take effect.
- Changes to `deploy.readiness_mode` require a restart to take effect.
- Changes to `deploy.health_timeout` require a restart to take effect.
- Changes to `deploy.readiness_delay` require a restart to take effect.
- Changes to `deploy.drain_mode` require a restart to take effect.
- Changes to `deploy.drain_timeout` require a restart to take effect.
- Changes to `deploy.drain_delay` require a restart to take effect.
