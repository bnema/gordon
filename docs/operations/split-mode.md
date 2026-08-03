# Split mode

Gordon uses one binary with five `serve` roles: `monolith`, `control`, `runtime`, `edge`, and `registry`. A production split deployment runs the last four as containers on one private component network. Use the migration commands to generate and launch their role-specific configuration; do not copy the monolith configuration into each role.

## Runtime compatibility

| Deployment | Engine mode | Support |
| --- | --- | --- |
| Split | Rootless Podman, local user service | **Supported in this phase** |
| Split | Rootful Podman or Docker | Pending role-scoped configuration publication; do not deploy |
| Split | Rootless Docker or a remote daemon | Rejected |
| Monolith | Rootful Podman or Docker | Supported |

Split support is deliberately limited to a local rootless Podman user service. The generated four-role lifecycle relies on Podman's exact `keep-id:uid=<role-uid>,gid=<role-gid>` mapping and local private bind/socket ownership. A Docker-compatible API alone does not satisfy that contract. Monolith remains an ordinary image invocation and does not receive split-only `keep-id` settings.

## Responsibilities

| Role | Responsibility | Container-runtime access |
| --- | --- | --- |
| control | Admin API, desired state, authenticated snapshots and commands | No |
| runtime | Containers, images, networks, volumes, backups | **Yes** |
| edge | Public application traffic and registry forwarding | No |
| registry | OCI storage and durable push-event delivery | No |

Only runtime receives `DOCKER_HOST`, `PODMAN_HOST`, or `CONTAINER_HOST` and the engine socket. Control talks to runtime over Gordon's authenticated, migration-private Unix RPC socket. This is not the Docker or Podman socket.

Each split role has a fixed, distinct non-root UID/GID: runtime `21001:21001`, control `21002:21002`, edge `21003:21003`, and registry `21004:21004`. Generated containers drop all capabilities, add none, and set `no-new-privileges`. Do not override these identities or user-namespace settings. They bind private files, persistent volumes, and the runtime-only engine socket to the role that owns them.

## Network and TLS boundaries

- Components share a private runtime network and use controlled aliases such as `gordon-control`, `gordon-edge`, and `gordon-registry`.
- Edge forwards registry requests to the `gordon-registry` network alias, never `localhost` or `127.0.0.1`.
- Generated edge manifests use `edge.tls.mode = "external"`. The external listener or upstream terminator owns public TLS during split migration.
- Plaintext component gRPC is generated only for the private component network. Do not publish those listeners.
- Runtime command transport is a Unix socket under Gordon's private component state. It is authenticated and must not be replaced with an engine endpoint.

## Role configuration

Split bootstrap writes strict role manifests under the configured `server.data_dir` component directory and private `0600` role environment files. The generated contracts include:

- control: private gRPC listener, runtime Unix endpoint, non-secret route/traffic inputs;
- runtime: engine endpoint, private runtime listener, volume policy;
- edge: control stream, public edge listener, external TLS boundary, registry-forward credential;
- registry: storage/listen limits, authenticated control event endpoint, durable outbox limits.

Start a generated manifest only through the orchestrated migration. `gordon serve --role <role> --config <generated-role.toml>` is the underlying process interface, not a reason to hand-author credentials.

## Retry behavior

Runtime commands carry idempotency keys. Successful and policy-denied terminal results are persisted and replayed without repeating work; failed/retryable commands may run again. Registry push events are persisted to a bounded outbox and replayed after control outages/restarts. Edge snapshot subscriptions retry with bounded backoff and only reset backoff after a valid newer snapshot is published. Operators should repair the dependency and resume rather than delete checkpoint or outbox state.

## Related

- [Migration runbook](./migration.md)
- [Security hardening](../config/security-hardening.md)
- [Environment reference](../reference/env-variables.md)
- [Troubleshooting](../reference/troubleshooting.md)
