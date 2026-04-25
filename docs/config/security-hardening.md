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

- `/admin/logs` and deploy failure logs require `admin:logs:read`.
- `admin:status:read` does not grant log access.
- Common secret patterns are redacted before logs are returned.

## Volume pruning scope

Volume pruning only removes unused Docker volumes explicitly managed by Gordon (`gordon.managed=true`). It ignores unrelated Docker volumes even if they are unused.

Use dedicated admin scopes:

- `admin:volumes:read` for listing volumes.
- `admin:volumes:write` for prune operations.

## Pass migration plaintext handling

When Gordon migrates legacy plaintext `.env` files into `pass`, it removes the plaintext source after a successful migration and does not leave `.env.migrated` copies by default. If pass entries already exist, migration fails closed and leaves the plaintext file in place for manual operator review rather than deleting potentially unique values.

## External image registries

Gordon's configured registry is always allowed. Explicit external registries must be allowlisted:

```toml
[images]
allowed_registries = ["docker.io", "ghcr.io", "registry.example.com:5000"]
require_digest = true
```

- Empty `allowed_registries` rejects explicit external registries.
- `localhost`, loopback, private, link-local, unspecified, and metadata-style registries are rejected.
- `require_digest = true` requires allowlisted external images to use `@sha256:<64 hex chars>`.
- Include ports in allowlist entries when the registry uses a non-default port.

## Docker network isolation

Per-app Docker networks are enabled by default. To block direct external egress from isolated networks, opt into Docker internal networks:

```toml
[network_isolation]
enabled = true
internal = true
```

`internal = false` remains the compatibility default because some applications and attachments need direct egress during startup.

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
