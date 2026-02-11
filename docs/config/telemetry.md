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
| `logs` | bool | `true` | Bridge zerolog output to OTLP logs |
| `trace_sample_rate` | float | `1.0` | Fraction of traces to sample (`0.0` = none, `1.0` = all) |

## How It Works

Gordon initializes an OTel provider at startup. When telemetry is enabled:

1. **Traces** -- Spans wrap critical operations: container deploy, image pull, registry manifest push, and proxy target resolution. The `otelhttp` middleware adds a span to every HTTP request on both the proxy and registry servers.
2. **Metrics** -- Gordon records custom counters and histograms for deploys, container restarts, crash loops, managed container count, registry pushes, and event bus throughput.
3. **Logs** -- A zerowrap/otel hook bridges all structured log output to the OTLP log pipeline. Every log line automatically carries `trace_id` and `span_id` when emitted inside a traced request.

When telemetry is disabled (the default), all OTel instruments are noop -- zero overhead.

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
