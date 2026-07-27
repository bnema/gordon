# Monolith-to-split migration

> **`gordon migrate` is a transitional v2 → v3 tool.** It exists only to convert a
> **host-operated v2 monolith** into a split component deployment. It is planned for removal
> in a future release once existing v2 installs have converted.
>
> **New v3 installs must not use `migrate`.** There is no monolith to convert, and
> `migrate plan` fails closed with a `split_topology` preflight check when the
> configuration already declares a split control plane. Set up a fresh v3 split
> deployment from scratch instead: see [Split bootstrap](./split-bootstrap.md).

The supported production migration target is **a local rootless Podman user service**. Migration is checkpointed and resumable; it does not provide a `rollback` subcommand.

Rootful split migration is not supported in this phase; it remains pending role-scoped configuration publication. Rootless Docker and remote Docker/Podman daemons are rejected because they cannot prove the local `keep-id` and private mount ownership contract. Rootful Docker and Podman remain supported for ordinary monolith operation, which does not receive split-only user-namespace settings. See the [split-mode compatibility matrix](./split-mode.md#runtime-compatibility).

## Requirements

- A healthy monolith using the target config and data directory before the maintenance window.
- Local rootless Podman with its user socket active and API reachable as the migrating host user. Rootful engines, rootless Docker, and remote daemons are not valid split targets.
- The candidate Gordon image available or pullable by that Podman user.
- Writable config, data, registry, environment, and credential storage.
- Enough disk space and no ambiguous/unmanaged Gordon resources.
- A maintenance window in which the host `gordon.service` is stopped and its public ports remain free through plan, prepare, and switch. Set `server.port` to the intended generated split-edge listen port even though monolith public traffic uses `[entrypoints]`.
- A private runtime handoff seed, preferably via environment:

```toml
[runtime]
token_env = "GORDON_RUNTIME_HANDOFF_TOKEN"
```

```bash
export GORDON_RUNTIME_HANDOFF_TOKEN="$(openssl rand -hex 32)"
export GORDON_MIGRATION_IMAGE="ghcr.io/example/gordon:<target-version>"
```

Do not put real token values in TOML, shell history, issue reports, or logs.

### Authentication signing key

When `auth.enabled=true`, the migration process requires a JWT signing key of at least 32 bytes from the named environment source before `plan` can succeed:

```bash
export GORDON_AUTH_TOKEN_SECRET="$(openssl rand -hex 32)"
gordon migrate plan --config ~/.config/gordon/gordon.toml --json
```

`auth.token_secret` remains operator-owned and is not copied from the monolith or resolved and republished into split role manifests. If `plan` reports `GORDON_AUTH_TOKEN_SECRET` as missing, set that key in the migration command's environment and rerun `plan`; the report contains the key name only, never its value. The private value is scoped to control, runtime, and registry, which perform JWT signing or symmetric verification. Edge never receives it.

When `auth.enabled=false`, no JWT signing key is required or transferred. The generated control, runtime, and registry configurations retain disabled auth, and none of those roles initializes user JWT/token authentication.

## Pass secrets are operator-imported

Split control owns a private `pass`/GPG store in its persistent managed volume. Migration never mounts a host keyring or password store and never imports legacy environment files automatically.

After control is prepared, use a private shell with history disabled to re-enter each application and attachment secret with the authenticated `gordon secrets set` command. For a bulk move, export the old store to an owner-only temporary file and explicitly submit each key through that command; verify the new entries, then securely remove the export. Recreate authentication tokens after the move: tokens and signing material from the monolith are not implicitly transferred and old tokens must not be assumed valid.

### Back up and restore the managed pass volume

The control secret volume is installation-namespaced as `gordon-control-secrets-<installation-id>`; do not assume an unqualified volume name. Treat backup and restore as privileged offline operations. Stop control, prove that it is stopped, and keep it stopped while any other process mounts the volume. This releases the exclusive managed-store lease and prevents concurrent GPG or `pass` writes.

For offline maintenance automation that must prove exclusive ownership without starting control, run `gordon secrets lock --config <path>`. It validates the configured managed `pass` backend, prints only `Managed pass backend lock acquired`, and holds the same exclusive process lease as control until `SIGINT`, `SIGTERM`, or context cancellation. It never reads or prints secret values. Use it only while control is stopped, and stop the lock holder before restarting control; concurrent `secrets doctor` and control startup fail closed while it is active.

Use the exact digest-pinned Gordon control image selected by the deployment (a reference ending in `@sha256:<digest>`), not a tag and not a helper image. Before mounting secrets, resolve that local reference with pulls disabled and compare its immutable image ID with the stopped control container's recorded image ID. Abort on a missing image, a tag-only reference, or any mismatch. Every container invocation in the procedure must use that verified reference with `--pull=never` and `--network none`; no other image or container may mount, extract, inspect, copy, or delete managed secret data.

A backup implementation is acceptable only when it satisfies all of these requirements:

- The verified control artifact mounts the stopped source volume read-only and streams the archive to `age` encryption. The ciphertext is written as an owner-only candidate beside the final backup and atomically renamed only after validation; an existing verified ciphertext is never truncated or removed first.
- Validation streams authenticated `age` decryption over stdin directly into an owner-only container tmpfs. Use an explicit tmpfs mode of `0700`, the artifact user's UID/GID, and a documented size limit at least as large as the uncompressed store plus the operator's chosen safety margin. Check the store size before starting and abort rather than silently exceeding that limit.
- Extraction, rejection of non-regular/non-directory entries and unexpected paths, managed-store structural checks, and the real `/app/gordon secrets doctor --write-check` all run in that same networkless, digest-verified container. The doctor uses the extracted tmpfs tree as `/var/lib/gordon/secrets` and the normal managed `GNUPGHOME` and `PASSWORD_STORE_DIR` paths.
- Plaintext exists only in the pipeline and that tmpfs. Do not use a named volume, bind-mounted host directory, temporary host file, or a second container for decrypted staging or inspection. Destroy the tmpfs by removing the validation container on every success, failure, signal, and timeout.

Size the tmpfs from the stopped source store's uncompressed byte count, round upward, and record the chosen limit in the operator's private runbook. For example, a 180 MiB store with a 25% margin needs at least 225 MiB; choosing 256 MiB leaves explicit headroom. The limit is a safety bound, not a substitute for checking available memory. A backup is valid only after authenticated decryption, safe extraction, structural validation, and the artifact's application-level write/read/delete check all succeed.

Gordon migration does not automate managed-secret restoration and defines no restore-state or atomic-recovery protocol. Restoration remains an operator-owned disaster-recovery operation. Use an audited external restore tool and keep these invariants:

- Keep control stopped and independently prove that it remains stopped for the entire offline operation.
- Use only the immutable digest-pinned control artifact after verifying its local image identity against the stopped control container. Disable pulls and networking for every invocation.
- Keep durable rollback ownership outside the managed root. Do not place rollback generations, phase records, journals, or restoration metadata in `/var/lib/gordon/secrets`.
- Authenticate, extract, and validate the candidate only in an owner-only container tmpfs with an explicit UID/GID, mode, and size bound. Plaintext must not enter a host path, named staging volume, or second container.
- Before startup, publish a managed root that matches the current validator exactly: the root contains only `.control.lock` and `current`; `current` contains exactly `gnupg`, `password-store`, and `.gordon-managed-pass-fingerprint`. The key fingerprint, marker, and `password-store/.gpg-id` must agree, and `/app/gordon secrets doctor --write-check` must pass using the normal managed paths.
- Retain the complete old volume under external durable rollback ownership until control has started, normal health checks pass, secrets have been exercised, and a new encrypted backup has been verified.

Gordon will fail closed on an unexpected managed-root layout; it will not interpret external recovery phases or repair a partially restored store. Never substitute a mutable tag, a network-enabled container, or persistent plaintext staging.

If no valid backup exists, do not edit or delete a partially published `current` directory to trigger regeneration. Provision a new empty installation volume through the normal control lifecycle, then re-enter secrets with authenticated `gordon secrets set` commands. If importing operator-held GPG material, use an offline private shell and `gpg --import` with output redirected to an owner-only audit file; initialize `pass`, re-enter each secret, verify by key name, and securely remove temporary exports. Never paste secret values, private-key output, or command output into logs.

## Maintenance-window procedure

Before stopping the service, take and verify a mutually consistent snapshot of the config, `server.data_dir`, registry storage, migration state, and component volumes. Prepare an executable rollback script that stops any split public listener, restores that snapshot, and starts exactly one host `gordon.service`. Keep the script outside the directories it restores.

Stop the host service, confirm it is inactive, and run every migration command against the same local config. The commands introduce no cold-migration flag; free public ports select the normal path.

```bash
sudo systemctl stop gordon.service
sudo systemctl is-active --quiet gordon.service && exit 1

gordon migrate plan --config ~/.config/gordon/gordon.toml --json
gordon migrate prepare --config ~/.config/gordon/gordon.toml --json
gordon migrate status --config ~/.config/gordon/gordon.toml --json
gordon migrate switch --config ~/.config/gordon/gordon.toml --json
```

`plan` is read-only and must report `"ready": true`. `prepare` writes private role manifests/environment, creates the private component network, and starts the probe-only prepared components while the host service remains stopped. Those four containers use fixed distinct non-root identities (runtime `21001:21001`, control `21002:21002`, edge `21003:21003`, registry `21004:21004`), exact Podman `keep-id` mappings, dropped capabilities, and `no-new-privileges`; only runtime receives the engine socket. `switch` requires authenticated prepared-edge state, transfers runtime authority, and activates final listeners.

If any command fails, leave `gordon.service` stopped while inspecting status. A failed switch removes a partial final edge and proves the prepared edge. Retry only when the reported outcome is retryable; otherwise run the prepared rollback script.

A switch can complete after its initiating CLI loses the runtime connection. Check from a fresh host shell:

```bash
gordon migrate status --config ~/.config/gordon/gordon.toml --json
gordon migrate resume --config ~/.config/gordon/gordon.toml --json
```

After runtime handoff, `resume` reads the durable checkpoint and the generated `runtime.env`, then connects only to the replacement Gordon runtime Unix RPC. It cannot be redirected to Docker or Podman.

## Failure and rollback boundary

Before a successful switch, the host monolith remains stopped. Gordon records the retry phase/attempt and restores the probe-only prepared edge after a failed cold cutover. Fix the reported category and rerun `switch` or `resume` only when status marks the failure retryable; otherwise preserve evidence and run the prepared rollback script.

There is no automatic reverse migration after phase `switched`. Recovery after a completed switch means repairing/resuming the split deployment. A manual restoration to monolith is an operator disaster-recovery action: stop split public listeners first, preserve all data volumes and checkpoint evidence, restore a verified backup, then start exactly one monolith owner. Never run monolith and split runtime as simultaneous owners.

## Acceptance

Confirm:

```bash
gordon migrate status --config ~/.config/gordon/gordon.toml --json
podman ps --filter label=gordon.component=true
status="$(curl -sS -o /dev/null -w '%{http_code}' https://gordon.example.com/v2/)"
case "$status" in 200|401) ;; *) echo "registry probe failed: HTTP $status" >&2; exit 1;; esac
```

The registry probe accepts **only** `200` or `401`; `401` proves edge-to-registry reachability when auth is enabled. Any other status is a failed acceptance probe. Also deploy a test image, verify app traffic, restart control and registry, then confirm queued push events replay without duplicate deployment intent.

## Related

- [Split mode](./split-mode.md)
- [Rootless Podman](../../wiki/guides/podman-rootless.md)
- [Release gates](../reference/release-gates.md)
