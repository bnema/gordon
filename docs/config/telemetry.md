# Telemetry Configuration

Export traces, metrics, and logs from Gordon via OpenTelemetry (OTLP/HTTP).

When enabled, Gordon ships all three observability signals to an OTLP-compatible backend such as OpenObserve, Jaeger, or Grafana Cloud. Existing zerolog output continues unchanged; the OTel log bridge runs alongside it.

## Configuration

```toml
[telemetry]
enabled = false
endpoint = ""
auth_token = ""
traces = true
metrics = true
logs = true
trace_sample_rate = 1.0
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `false` | Enable OTLP telemetry export |
| `endpoint` | string | `""` | OTLP HTTP endpoint URL (e.g. `http://localhost:4318/api/default`) |
| `auth_token` | string | `""` | Base64-encoded `user:password` for Basic auth |
| `traces` | bool | `true` | Export distributed traces |
| `metrics` | bool | `true` | Export metrics (deploy counters, container lifecycle, registry, events) |
| `logs` | bool | `true` | Export Gordon, proxy access, and app container logs to OTLP |
| `trace_sample_rate` | float | `1.0` | Fraction of traces to sample (`0.0` = none, `1.0` = all) |

## How It Works

Gordon initializes an OTel provider at startup. When telemetry is enabled:

1. **Traces** -- Spans wrap critical operations: container deploy, image pull, registry manifest push, and proxy target resolution. The `otelhttp` middleware adds a span to every HTTP request on both the proxy and registry servers.
2. **Metrics** -- Gordon records custom counters and histograms for deploys, container restarts, crash loops, managed container count, registry pushes, and event bus throughput.
3. **Logs** -- Gordon exports three log families (see [Logs](#logs)).

When telemetry is disabled (the default), all OTel instruments are noop -- zero overhead.

## Logs

With `logs = true`, Gordon exports:

| Family | Source | `service.name` |
|--------|--------|----------------|
| Gordon process logs | zerowrap output, with `trace_id`/`span_id` inside traced requests | `gordon` |
| Proxy access logs | Every proxied request, attributed to the app service owning the host | `<app>.<service>` (`gordon` for registry and unknown hosts) |
| App container logs | stdout and stderr of every running app service container | `<app>.<service>` |

Every app record carries these resource attributes:

| Attribute | Example | Use |
|-----------|---------|-----|
| `service.name` | `blog.web` | One exact service; unique across apps |
| `service.namespace` | `blog` | All services of one app |
| `gordon.app` | `blog` | Filter by app |
| `gordon.service` | `web` | Filter by service name across apps |

Record attributes:

- `log.type`: `access` or `container`
- `log.iostream`: `stdout` or `stderr` (container logs)
- Access logs: `http.request.method`, `http.response.status_code`, `server.address`, `url.path`, `client.address`, `request.id`, and more. Severity follows the status: `5xx` is `ERROR`, `4xx` is `WARN`, otherwise `INFO`.

Loki indexes `service.name` and `service.namespace` as stream labels by default; the other attributes are structured metadata, filtered after the stream selector. Example queries in Grafana:

```text
{service_namespace="blog"}                          # every log of the blog app
{service_name="blog.web"} | log_type="access"       # access logs of one service
{service_namespace=~".+"} | gordon_service="web"    # every "web" service, all apps
```

Container output is exported from the moment Gordon starts, including the first lines of every container deployed afterwards. Output emitted while Gordon is stopped is not exported. Lines longer than 64 KiB are truncated. Export is buffered and never slows the proxy or the app: on overload, the oldest records are dropped.

### Per-app opt-out

App container logs are exported by default. Opt out in the [app manifest](./apps.md#telemetry), per app or per service; a service setting overrides the app setting:

```toml
[telemetry]
logs = true          # app default

[services.db.telemetry]
logs = false         # only "db" stops exporting
```

The opt-out covers container output only; access logs of the app's hosts are still exported.

## Endpoint URL

The `endpoint` field accepts a full URL. Gordon parses it to extract:

- **Host and port** for the OTLP exporter
- **Base path** appended with `/v1/traces`, `/v1/metrics`, or `/v1/logs`
- **Scheme** -- `http://` disables TLS; `https://` enables it

For OpenObserve, the endpoint typically includes the organization path:

```text
http://localhost:5080/api/default
```

This produces export URLs like `http://localhost:5080/api/default/v1/traces`.

## Authentication

Set `auth_token` to the Base64-encoded `user:password` string. Gordon sends it as a `Basic` authorization header on every OTLP export request.

For OpenObserve, copy the token from **Ingestion > OTLP** in the web UI.

Since Gordon itself is the platform (not a managed container), it does not use `gordon secrets set`. Store the token with one of these methods:

| Method | How |
|--------|-----|
| Environment variable | `GORDON_TELEMETRY_AUTH_TOKEN=<token> gordon serve` |
| Plain text in config | Set `auth_token = "<token>"` (restrict file permissions) |

## Trace Sampling

| Value | Behavior |
|-------|----------|
| `1.0` | Sample every trace (default) |
| `0.5` | Sample 50% of traces |
| `0.0` | Drop all traces (tracing pipeline stays initialized) |

At low traffic volumes (< 1000 req/s), keep the rate at `1.0`. Reduce it if storage costs or export bandwidth become a concern.

## Metrics Reference

Gordon exports these custom metrics (all prefixed with `gordon.`):

### Deployments

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `gordon.deploy.total` | Counter | - | Total deployments attempted |
| `gordon.deploy.duration_seconds` | Histogram | s | Time from deploy start to completion |
| `gordon.deploy.errors` | Counter | - | Deployments that failed |

Attributes: `domain`, `image`

### Container Lifecycle

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `gordon.container.restarts` | Counter | - | Container restart count |
| `gordon.container.crash_loops` | Counter | - | Crash loop detections |
| `gordon.container.managed` | UpDownCounter | - | Currently tracked containers |

Attributes: `source` (restarts only: `monitor` or `api`); `gordon.container.managed` is a global gauge with no attributes

### Registry

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `gordon.registry.push.total` | Counter | - | Manifest pushes received |
| `gordon.registry.push.bytes` | Counter | By | Total manifest bytes pushed |

Attributes: `name`, `reference`

### Event Bus

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `gordon.events.processed` | Counter | - | Events handled successfully |
| `gordon.events.dropped` | Counter | - | Events dropped (channel full) |

Attributes: `event_type`

### HTTP (via otelhttp)

The `otelhttp` middleware automatically records standard HTTP server metrics on both the proxy and registry servers:

- `http.server.request.duration`
- `http.server.request.body.size`
- `http.server.response.body.size`

## Trace Spans

| Span Name | Package | Description |
|-----------|---------|-------------|
| `container.deploy` | container | Full deploy lifecycle (root span) |
| `container.create_and_start` | container | Container creation, start, and readiness |
| `container.ensure_image` | container | Image pull and tagging |
| `registry.put_manifest` | registry | Manifest storage and event publish |
| `proxy.get_target` | proxy | Proxy target resolution and caching |

The `otelhttp` middleware adds an HTTP-level span to every request on both servers.

## Examples

### OpenObserve (self-hosted)

```toml
[telemetry]
enabled = true
endpoint = "http://localhost:5080/api/default"
auth_token = "YWRtaW5AZXhhbXBsZS5jb206c2VjcmV0"
traces = true
metrics = true
logs = true
trace_sample_rate = 1.0
```

### Grafana Cloud (OTLP)

```toml
[telemetry]
enabled = true
endpoint = "https://otlp-gateway-prod-us-east-0.grafana.net/otlp"
auth_token = "<instance-id>:<api-key>"
traces = true
metrics = true
logs = true
trace_sample_rate = 0.1
```

### Traces Only (minimal overhead)

```toml
[telemetry]
enabled = true
endpoint = "http://localhost:4318"
traces = true
metrics = false
logs = false
trace_sample_rate = 0.5
```

## Environment Variable Override

```bash
GORDON_TELEMETRY_ENABLED=true
GORDON_TELEMETRY_ENDPOINT=http://localhost:5080/api/default
GORDON_TELEMETRY_AUTH_TOKEN=YWRtaW5AZXhhbXBsZS5jb206c2VjcmV0
GORDON_TELEMETRY_TRACES=true
GORDON_TELEMETRY_METRICS=true
GORDON_TELEMETRY_LOGS=true
GORDON_TELEMETRY_TRACE_SAMPLE_RATE=1.0
```

## Related

- [Configuration Overview](./index.md)
- [Logging](./logging.md)
- [Configuration Reference](./reference.md)
