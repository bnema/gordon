# Security Hardening Regression Matrix

This matrix lists the behaviors that should be tested while implementing the security-hardening fixes from the audit. It is intentionally focused on regressions: normal Gordon workflows must continue to work while exploit paths are blocked.

## Registry uploads

### Normal behavior to preserve
- Docker push of a small image succeeds.
- Docker push with multiple layers succeeds when every layer is below the configured cumulative blob limit.
- Chunked blob upload succeeds when the cumulative size remains below the new cumulative limit setting, tentatively named `max_blob_size` unless implementation chooses a clearer config name.
- A final `PUT` with a valid digest finalizes the blob and returns OCI-compatible headers.
- Existing `max_blob_chunk_size` behavior still rejects a single oversized chunk; this remains distinct from the cumulative per-blob/layer limit.

### Security behavior to verify
- Repeated `PATCH` chunks cannot exceed the cumulative upload limit.
- `Content-Length` larger than the remaining allowed size is rejected before writing when possible.
- A rejected upload does not leave unbounded stale data on disk.
- Cancel/cleanup paths remove upload temp files and upload locks.
- Error response remains Docker Registry compatible (`413` / `SIZE_INVALID`).

### Suggested tests
- Unit tests for filesystem blob append under and over cumulative size.
- Handler tests for `PATCH` and final `PUT` over limit.
- Integration/manual: push image with a large layer and confirm failure is clean.

## Deploy errors and logs

### Normal behavior to preserve
- Successful deploy response still includes status, domain, and container ID.
- Failed deploy still returns useful high-level error/cause/hint.
- Operators with the new logs permission can still retrieve logs through `/admin/logs`.
- CLI deploy displays helpful failure information without leaking raw secrets.

### Security behavior to verify
- A token with only `admin:config:write` cannot receive raw container logs in `/admin/deploy` error responses.
- Logs returned through log endpoints are redacted for common secret patterns.
- `admin:status:read` no longer grants log read access.
- `admin:*:*` remains compatible and can access logs.

### Suggested tests
- Handler tests for deploy error response with and without `logs:read`.
- Scope tests for `logs:read`, wildcard, and old `status:read` behavior.
- Redactor table tests for `PASSWORD=`, `TOKEN=`, `SECRET=`, `API_KEY=`, `DATABASE_URL=`, and simple JSON fields.

## Admin scopes and compatibility

### Normal behavior to preserve
- Existing `admin:*:*` tokens continue to work everywhere.
- Existing `routes`, `config`, `secrets`, and `status` scopes keep their intended behavior.
- Token exchange through `/auth/token` still works for admin scopes.

### Security behavior to verify
- New sensitive resources such as `logs` and possibly `volumes` are parsed and enforced.
- Unknown or malformed scopes remain ignored/denied, not accidentally allowed.

### Suggested tests
- Domain auth scope parsing tests.
- Admin middleware/handler tests for each sensitive endpoint.

## Auto-route env-file extraction

### Normal behavior to preserve
- Valid `gordon.env-file` imports default env values for a newly created auto-route.
- Existing env values still override image-provided defaults.
- Secret reference rejection remains intact.
- Auto-route without env-file remains unchanged.

### Security behavior to verify
- Oversized env files are rejected before full memory read.
- Disallowed paths (`/proc`, `/sys`, `/dev`, `/run`, `/var/run`, `/`, traversal) are rejected.
- Failed env-file import does not prevent route/deploy behavior beyond the intended warning path unless explicitly designed.

### Suggested tests
- Runtime extraction tests using a controlled reader larger than the limit.
- Auto-route usecase tests for allowed/disallowed paths.
- Tests confirming `${pass:...}` / `${sops:...}` refs remain rejected.

## Domain canonicalization and proxy routing

### Normal behavior to preserve
- Existing lowercase route domains continue to resolve.
- Registry domain still routes to registry.
- External routes still resolve and retain SSRF protections.
- Hostnames with expected public domains work through proxy.

### Security behavior to verify
- `App.Example.com` and `app.example.com` map to the same canonical route.
- Duplicate case-variant routes cannot be created.
- Host with invalid port/trailing dot/case is handled consistently.
- HTTP-to-HTTPS redirect does not reflect arbitrary unconfigured Host values.

### Suggested tests
- Config service tests for add/update/list canonical route domains.
- Auto-route tests for case variants.
- Proxy handler tests for Host normalization.
- Middleware tests for redirect target host validation.

## Volumes

### Normal behavior to preserve
- Listing volumes still shows Gordon-managed volumes.
- Dry-run prune still reports what would be removed.
- Actual prune still removes unused Gordon-managed volumes.
- In-use Gordon volumes are not removed.

### Security behavior to verify
- Unused non-Gordon Docker volumes are not removed.
- Volumes without explicit Gordon ownership metadata, preferably `gordon.managed=true`, are ignored; prefix-only matching is unsafe except as a constrained legacy fallback with dedicated tests.
- Any new `volumes:read/write` scope is enforced correctly.

### Suggested tests
- Volume service tests with mixed Gordon/non-Gordon, used/unused volumes.
- Admin handler tests for permissions.
- Manual: create an unrelated Docker volume, run Gordon prune dry-run/real, confirm it remains.

## Backups

### Normal behavior to preserve
- Manual backup completes and appears in list.
- Scheduled backup path format remains readable/listable.
- Backup detect still finds supported DB attachments.
- Existing backups can still be listed after filename format changes.

### Security behavior to verify
- Two backups for same domain/db/schedule in the same second do not share tmp/final filenames.
- Concurrent backups do not truncate each other.
- Store uses unique tmp files and atomic rename.
- Path traversal protections remain intact.

### Suggested tests
- Filesystem storage tests with identical timestamps.
- Concurrent store test with two readers and same domain/db/schedule.
- List tests for old and new filename formats if compatibility is needed.

## Secrets and pass migration

### Normal behavior to preserve
- `pass` backend still migrates existing env keys correctly.
- Attachment env migration still works.
- Unsafe backend still loads valid files under `dataDir/secrets` for development.
- Existing env file parsing remains compatible.

### Security behavior to verify
- Unsafe backend rejects `../` and absolute `auth.token_secret` paths.
- Migration does not leave plaintext `.env.migrated` by default.
- If an explicit keep/quarantine option exists, files are stored with restrictive permissions and loud warnings.

### Suggested tests
- `loadSecret` path traversal tests.
- Migration tests verifying plaintext source file removal/quarantine.
- Permission tests where practical (`0600` files, `0700` dirs).

## Config snapshot

### Normal behavior to preserve
- `/admin/config` still returns enough information for CLI/admin use.
- Routes and network isolation status remain visible to authorized callers.

### Security behavior to verify
- `data_dir` is hidden by default or only available under a deliberately stronger detailed mode.
- External route targets are hidden/redacted by default or gated.
- `config:read` behavior remains documented.

### Suggested tests
- Admin config response tests for default and detailed modes.
- CLI remote config tests if output shape changes.

## External image registries

### Normal behavior to preserve
- Images in the Gordon registry deploy normally.
- Explicitly allowlisted external registries deploy if configured.
- Attachments still deploy from allowed sources.

### Security behavior to verify
- `localhost`, loopback, private IP, link-local, metadata IP, and DNS resolving to private ranges are rejected unless a deliberate unsafe override exists.
- Non-allowlisted external registries are rejected before Docker pull.
- Error messages do not leak sensitive network details.

### Suggested tests
- Image reference validation table tests.
- Config tests for `allowed_registries`.
- Container service tests ensuring rejected images do not call `PullImage`.

## Docker networks and runtime hardening

### Normal behavior to preserve
- Default/compat profile keeps common apps and attachments working.
- Attachments remain reachable by service alias where intended.
- Network groups still work when configured.

### Security behavior to verify
- `internal` network option is applied when configured.
- Strict profile applies readonly rootfs/user/caps as designed.
- `no-new-privileges:true` and cap dropping are preserved.
- Compat profile behavior is documented and tested.

### Suggested tests
- Docker adapter config construction tests.
- Container service tests for network option propagation.
- Manual smoke: deploy app + postgres attachment in compat profile.
- Manual smoke: deploy simple read-only app in strict profile.

## Install, Dockerfile, and supply-chain

### Normal behavior to preserve
- `install.sh` installs the binary on Linux/macOS.
- GitHub composite action installs Gordon in CI.
- Dockerfile builds successfully.

### Security behavior to verify
- Tar extraction only extracts `gordon`.
- Installed binary must be a regular file, not symlink/directory.
- Permissions are explicit (`0755`).
- Checksums/signatures are verified consistently.
- Gitleaks output is clean or only contains allowlisted examples.

### Suggested tests
- Shellcheck/manual review for install script.
- Action script dry-run if practical.
- Docker build smoke after version alignment.
- Gitleaks on `git archive HEAD`, not local `.worktrees`.
