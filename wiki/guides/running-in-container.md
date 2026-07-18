# Running Gordon in containers

Use the same Gordon image for every role. The supported split deployment is generated and launched by `gordon migrate`; hand-written examples are useful only for understanding ownership.

## Monolith

A monolith owns control, runtime, edge, and registry responsibilities, so it needs persistent data and engine access. Bind only the configured edge/registry ports. Treat the engine socket as root-equivalent authority.

## Split ownership

| Container | Persistent/private mounts | Public surface |
| --- | --- | --- |
| control | generated role TOML, scoped env, control data, migration attestation | admin endpoint as configured |
| runtime | generated role TOML/env, runtime data, **engine socket** | none |
| edge | generated role TOML/env | public edge listener |
| registry | generated role TOML/env, registry data | reached through edge |

Never mount `/var/run/docker.sock`, a Podman socket, or pass `DOCKER_HOST`/`PODMAN_HOST`/`CONTAINER_HOST` to control, edge, or registry. Control's Gordon runtime Unix socket is a private authenticated migration transport, not an engine socket.

All four roles join Gordon's private component network. Edge forwards `/v2/` registry traffic to the `gordon-registry` alias and port from generated control state. `localhost:5000` inside edge points back to edge and is invalid.

Generated split edge configuration uses external TLS mode. Publish the edge listener behind the operator-owned TLS/network boundary; do not copy monolith TLS secrets into edge role TOML.

## Rootless Podman

Enable the user API before starting the monolith that performs migration:

```bash
systemctl --user enable --now podman.socket
export DOCKER_HOST="unix:///run/user/$(id -u)/podman/podman.sock"
podman info --format '{{.Host.Security.Rootless}}'
```

Only the replacement runtime inherits the endpoint. Follow [Rootless Podman](./podman-rootless.md) and the [migration runbook](/docs/operations/migration.md).

## Verify isolation

```bash
podman inspect gordon-runtime-<migration>-g1 --format '{{json .Mounts}}'
podman inspect gordon-edge-<migration>-g1 --format '{{json .Mounts}}'
podman inspect gordon-registry-<migration>-g1 --format '{{json .Mounts}}'
```

The engine socket must appear only for runtime. Preserve generated `0600` environment files and migration checkpoint state; do not print their contents.
