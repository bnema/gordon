# Migrate to Gordon v2.50

Gordon v2.50 replaces domain-based route workloads with declarative apps. The upgrade is intentionally explicit: Gordon does not infer app ownership from domains, does not adopt existing volumes, and does not copy domain secrets into app secrets automatically.

Use this guide before starting the v2.50 daemon with production traffic.

## Before the upgrade

1. Stop deployments and automatic image pruning.
2. Back up Gordon's configuration, state, registry, databases, volumes, and secret store using your existing procedures.
3. Record every current domain, image tag or digest, attachment, network relationship, volume, and secret key.
4. Keep the previous signed Gordon binary and its matching configuration and state backup available for rollback.

Do not delete old containers, volumes, pass entries, or registry tags during the migration. Gordon's ownership-aware reconciliation and prune paths preserve resources whose ownership cannot be proven. Runtime-native cleanup commands and manual deletion do not provide that guarantee.

## 1. Update the installation configuration

Remove workload declarations that v2.50 no longer accepts from `gordon.toml`:

- `[routes]`;
- `[attachments]`;
- `[network_groups]`;
- app-like `[[services]]` and `[service_routes]`;
- `[auto_route]` and `[auto_route_allowed_domains]`;
- `[previews]`.

Keep installation-level settings such as entrypoints, TLS, limits, registry policy, external routes, backup destinations, logging, and authentication.

Review every app image registry before applying manifests. v2.50 allows Docker Hub (`docker.io`, including its canonical pull host `registry-1.docker.io`), `ghcr.io`, `quay.io`, and Gordon's configured registry by default. Add each other registry, including private registries, as an exact hostname and optional non-default port:

```toml
[images]
allowed_registries = ["registry.internal:5000"]
```

Hostname matching is case-insensitive, ignores one trailing dot, and treats port `443` as the default. The allowlist is checked during manifest apply, deployment preflight, and immediately before pull. It controls registry names only: it does not prove that DNS resolves to a public IP or enforce runtime egress. Use firewall or runtime network policy when destination-level restrictions are required.

Create one app manifest per workload. See [App Manifest](./config/apps.md).

```toml
name = "example-app"

[[service]]
name = "web"
image = "registry.example.com/example-app:v2.50.0"

[service.secrets]
DATABASE_URL = "database-url"

[[service.http]]
host = "app.example.com"
port = 8080
```

The manifest contains secret **names**, never secret values.

## 2. Migrate domain secrets

Gordon does not convert domain-scoped secrets into app secrets. Inventory the old key names, declare them under `[service.secrets]`, then apply the manifest before setting values:

```bash
gordon apps apply --file ./example-app.toml --remote production
printf 'DATABASE_URL=%s\n' "$(pass show gordon/env/app_example_com/DATABASE_URL)" \
  | gordon apps secrets set example-app --service web --stdin --remote production
```

Pass values through standard input. Do not put them in manifests, command arguments, logs, shell history, or temporary files. Repeat explicitly when several services need the same value.

Deploy and restart the app to verify that it uses the new app-scoped entries. Keep the old `gordon/env/...` entries throughout the rollback window; Gordon never uses them as a fallback. Refer to the `pass` documentation for password-store and GPG backup or restore procedures.

## 3. Transfer existing volume data

Gordon's reconciliation and prune paths leave existing unowned volumes untouched, but Gordon does not adopt them. After applying the manifest, let Gordon create the new app-owned volumes, then transfer the data with your container runtime's tools while the workload is stopped. Do not run runtime-wide volume prune, edit `state.db`, rename volumes, or alter ownership labels.

Use a database-native backup and restore for databases instead of copying live database files. Keep the old volumes unchanged until the new app has passed data, deploy, restart, and rollback-readiness checks. Refer to the Docker or Podman documentation for runtime-specific copy and inspection commands.

## 4. Apply and deploy apps

For every manifest:

```bash
gordon apps apply --file ./example-app.toml --remote production
gordon apps diff example-app --remote production
gordon apps deploy example-app --remote production
gordon apps status example-app --json --remote production
```

Push only uploads OCI content in v2.50; it does not deploy. Apply and deploy explicitly.

## 5. Replace tokens and automation

Replace removed route scopes with app scopes:

- `admin:apps:read` for list, show, diff, and status;
- `admin:apps:write` for apply, deploy, lifecycle, and app secrets.

Generate replacement CI and operator tokens with the minimum required scopes. Update automation that expected push-to-deploy, route mutation commands, previews, attachments, bootstrap, autoroute, pin, or rollback commands.

To roll back an application release in v2.50, apply a manifest containing the previous image tag and deploy it.

## 6. Validate before opening traffic

Verify all of the following:

- each app is converged on the expected pinned digest;
- HTTP, TCP, and UDP entrypoints work as applicable;
- app secrets survive a restart and no value appears in output or logs;
- named volumes contain the expected data after deploy and restart;
- stopped intent survives a daemon restart;
- retained and unknown resources remain untouched;
- `gordon images prune --dry-run` reports protected app content correctly before any real prune;
- the previous binary and matching data backup remain available.

Run destructive prune commands only after their dry-run candidate list matches the expected ownership and retention policy.

## Security guarantees to rely on after the upgrade

- **Containment first.** Keep `auth.enabled = true` and automatic registry prune disabled (`images.prune.enabled = false`) until the upgraded build is running and validated. A disabled-auth registry is local-only: public ingress cannot reach it through the proxy.
- **Network isolation.** Every app container joins an app-owned private network; shared memberships come only from explicit manifest declarations, and container memory/CPU/PID limits are applied on create and recovery.
- **Registry content is repository-scoped.** Pulls require the repository to have completed the upload of a blob, and manifests may only reference content that repository owns. Content uploaded by an older build has no durable ownership marker, so a repository whose blobs predate this build must re-push its images before they can be pulled again.
- **Runtime image prune needs released ownership.** An app's pinned images are recorded as attached while deployed and released when the app is removed. Dangling images with no durable ownership record are never deleted, and image labels alone never authorize deletion.
- **Durable roots keep their closure.** A digest pinned by active/desired state, an apply intent, an operation, or ownership retains its manifest, config, and layers even if the tag moves away.
- **Plaintext TLS policy.** Hosts declared `tls = always` never receive plaintext traffic: they redirect when an HTTPS endpoint exists and are refused with `421` when none does.
- **Failure output is log-free.** Mutation responses and operation lookup for `admin:apps:read` carry stable errors only; application diagnostics require `admin:logs:read` and are redacted before storage.

## Rollback

A binary downgrade against v2.50 app state is unsupported. To roll back:

1. stop the v2.50 daemon;
2. restore the previous signed binary;
3. restore its matching configuration and state backup as one set;
4. restore secret-store entries if any were removed;
5. restart with automatic pruning disabled;
6. verify workloads and data before reopening traffic.

Do not attempt rollback by adopting unknown containers or volumes into v2.50 state.

## Related

- [Upgrading Gordon](./upgrading.md)
- [App Manifest](./config/apps.md)
- [Apps CLI](./cli/apps.md)
- [Secrets](./config/secrets.md)
- [Authentication](./config/auth.md)
