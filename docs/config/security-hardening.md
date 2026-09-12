# Security Hardening Controls

This page summarizes Gordon's security-related configuration knobs and the defaults operators should know before exposing an instance.

## Registry upload quotas

Registry blob uploads are bounded by two limits under `[server]`:

```toml
[server]
max_blob_chunk_size = "95MB"
max_blob_size = "1GB"
```

- `max_blob_chunk_size` limits a single upload chunk.
- `max_blob_size` limits the cumulative blob/layer upload size.
- Exceeding either limit returns an OCI-compatible size error and the failed upload is cleaned up.

## Admin logs permission

Container and deploy logs can include environment-derived output. Gordon gates log access behind a dedicated scope:

```bash
gordon auth token generate --subject ops --scopes "admin:logs:read" --expiry 30d
```

- `/admin/logs`, app failure diagnostics, and deploy failure logs require `admin:logs:read`.
- `admin:status:read` does not grant log access.
- Operation errors returned to `admin:apps:read` callers are stable and log-free. Application diagnostics are a separate field, redacted of the affected service's resolved secret values before storage, and returned only to callers holding `admin:logs:read`. If a declared secret cannot be resolved for redaction, Gordon drops the diagnostics instead of storing unredacted output. Mutation responses never include application output.
- Process and container log sources remain operator-owned data. Gordon redacts common credential patterns when serving logs through its API, but this is defense in depth rather than proof that arbitrary application output is secret-free. Restrict filesystem, journal, runtime, backup, and `admin:logs:read` access accordingly.

## Volume pruning scope

Volume pruning removes a volume only when Gordon has a durable `released` ownership record, the runtime app/incarnation/service labels agree with that record, and no container mounts it. A `gordon.managed=true` label by itself is not deletion authority; retained, unknown, contradictory, and unrelated volumes survive.

Use `gordon volumes prune --dry-run` before deletion. Do not substitute `docker volume prune` or an equivalent runtime command: runtime-native pruning bypasses Gordon's ownership and retention checks.

Use dedicated admin scopes:

- `admin:volumes:read` for listing volumes.
- `admin:volumes:write` for prune operations.

## Pass import plaintext handling

With the `pass` backend, Gordon imports eligible plaintext `.env` files at startup. It removes a source file only after every entry is stored successfully. If a destination entry already exists or an import fails, Gordon fails closed and leaves the plaintext source in place for operator review.

## External image registries

Docker Hub, `ghcr.io`, `quay.io`, and Gordon's configured registry are always allowed. Add every other registry hostname and non-default port explicitly:

```toml
[images]
allowed_registries = ["registry.internal:5000"]
require_digest = true
```

- `docker.io` and Docker Hub's canonical pull host `registry-1.docker.io` are equivalent.
- Other registries are accepted only when their exact hostname+port is configured. Allowlisting does not configure credentials; external resolution and pulls are anonymous unless the runtime already has suitable access.
- `require_digest = true` requires every image, including Gordon registry images, to use `@sha256:<64 hex chars>`.
- The allowlist restricts hostnames, not resolved IPs. It cannot prove a DNS hostname is non-private and does not replace firewall or runtime egress controls.

## Smart TCP Raw Fallback

Raw TCP fallback on a `smart_tcp` edge entrypoint is disabled by default. Unknown non-HTTP/non-TLS bytes reach a backend only when the entrypoint explicitly sets `raw_fallback` to a TCP router and the fallback source policy allows the peer.

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
raw_fallback = "ssh-fallback"
raw_fallback_trusted_cidrs = ["100.64.0.0/10"]
```

Use `raw_fallback_trusted_cidrs` for private raw fallback. Public raw fallback requires an explicit acknowledgement:

```toml
[entrypoints.edge]
address = ":443"
protocol = "smart_tcp"
raw_fallback = "public-raw"
allow_public_raw_fallback = true
```

Security behavior:

- PROXY protocol v1 and v2 prefixes are rejected; Gordon does not trust or parse PROXY headers on smart TCP entrypoints.
- `trusted_cidrs` and `raw_fallback_trusted_cidrs` use the peer socket IP, not `X-Forwarded-For`.
- HTTP-looking or TLS-looking malformed traffic is rejected and never bypasses to raw fallback.
- Entry-point-wide `trusted_cidrs` applies before sniffing to all protocols; raw fallback has its own narrower policy for private fallback exposure.

## Docker network isolation

Per-app Docker networks are enabled by default. To block direct external egress from isolated networks, opt into Docker internal networks:

```toml
[network_isolation]
enabled = true
internal = true
```

`internal = false` remains the compatibility default because some applications need direct egress during startup.

Every app container joins an incarnation-owned private network derived from the app's internal UUID; shared memberships come only from explicit `[[network.shared]]` declarations, and memory/CPU/PID limits apply on every create and recovery path.

## Registry exposure

- With `auth.enabled = true`, registry requests are authenticated and repository-scoped; the public proxy forwards registry domains to the internal registry.
- With `auth.enabled = false`, the registry is local-only: the public proxy refuses registry-domain requests, and the registry handler accepts only direct loopback connections carrying the instance credentials.
- Registry requests are parsed once into a validated operation used by both authorization and dispatch, so the repository a token is checked against is exactly the repository served.
- Blob and upload access is repository-scoped: an upload UUID is usable only by the repository that started it, and a blob is served only to a repository that completed an upload of that digest.

## Container runtime profile

```toml
[containers]
security_profile = "compat" # or "strict"
```

- `compat` preserves broad image compatibility while retaining `no-new-privileges` and default capability restrictions.
- `strict` enables a read-only root filesystem, drops all capabilities, and only adds `NET_BIND_SERVICE`.

Use `strict` for images designed to write only to mounted volumes and run without extra Linux capabilities.

## Related

- [Auth](./auth.md)
- [Images](./images.md)
- [Network Isolation](./network-isolation.md)
- [Deploy](./deploy.md)
- [Volumes](./volumes.md)
- [Reference](./reference.md)
