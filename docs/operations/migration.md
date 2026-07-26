# Monolith-to-split migration

> **`gordon migrate` is a transitional v2 → v3 tool.** It exists only to convert a
> **host-operated v2 monolith** into a split component deployment. It is planned for removal
> in a future release once existing v2 installs have converted.
>
> **New v3 installs must not use `migrate`.** There is no monolith to convert, and
> `migrate plan` fails closed with a `split_topology` preflight check when the
> configuration already declares a split control plane. Set up a fresh v3 split
> deployment from scratch instead: see [Split bootstrap](./split-bootstrap.md).

The supported production migration target is **rootless Podman**. Migration is checkpointed and resumable; it does not provide a `rollback` subcommand.

## Requirements

- A healthy monolith using the target config and data directory before the maintenance window.
- Rootless Podman with its user socket active and API reachable.
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

## Pass secrets are operator-imported

Split control owns a private `pass`/GPG store in its persistent managed volume. Migration never mounts a host keyring or password store and never imports legacy environment files automatically.

After control is prepared, use a private shell with history disabled to re-enter each application and attachment secret with the authenticated `gordon secrets set` command. For a bulk move, export the old store to an owner-only temporary file and explicitly submit each key through that command; verify the new entries, then securely remove the export. Recreate authentication tokens after the move: tokens and signing material from the monolith are not implicitly transferred and old tokens must not be assumed valid.

### Back up and restore the managed pass volume

The control secret volume is installation-namespaced as `gordon-control-secrets-<installation-id>`; do not assume an unqualified volume name. Treat backup and restore as privileged offline operations. Stop control, prove that it is stopped, and keep it stopped while any other process mounts the volume. This releases the exclusive managed-store lease and prevents concurrent GPG or `pass` writes.

Use the exact digest-pinned Gordon control image selected by the deployment (a reference ending in `@sha256:<digest>`), not a tag and not a helper image. Before mounting secrets, resolve that local reference with pulls disabled and compare its immutable image ID with the stopped control container's recorded image ID. Abort on a missing image, a tag-only reference, or any mismatch. Every container invocation in the procedure must use that verified reference with `--pull=never` and `--network none`; no other image or container may mount, extract, inspect, copy, or delete managed secret data.

A backup implementation is acceptable only when it satisfies all of these requirements:

- The verified control artifact mounts the stopped source volume read-only and streams the archive to `age` encryption. The ciphertext is written as an owner-only candidate beside the final backup and atomically renamed only after validation; an existing verified ciphertext is never truncated or removed first.
- Validation streams authenticated `age` decryption over stdin directly into an owner-only container tmpfs. Use an explicit tmpfs mode of `0700`, the artifact user's UID/GID, and a documented size limit at least as large as the uncompressed store plus the operator's chosen safety margin. Check the store size before starting and abort rather than silently exceeding that limit.
- Extraction, rejection of non-regular/non-directory entries and unexpected paths, managed-store structural checks, and the real `/app/gordon secrets doctor --write-check` all run in that same networkless, digest-verified container. The doctor uses the extracted tmpfs tree as `/var/lib/gordon/secrets` and the normal managed `GNUPGHOME` and `PASSWORD_STORE_DIR` paths.
- Plaintext exists only in the pipeline and that tmpfs. Do not use a named volume, bind-mounted host directory, temporary host file, or a second container for decrypted staging or inspection. Destroy the tmpfs by removing the validation container on every success, failure, signal, and timeout.

Size the tmpfs from the stopped source store's uncompressed byte count, round upward, and record the chosen limit in the operator's private runbook. For example, a 180 MiB store with a 25% margin needs at least 225 MiB; choosing 256 MiB leaves explicit headroom. The limit is a safety bound, not a substitute for checking available memory. A backup is valid only after authenticated decryption, safe extraction, structural validation, and the artifact's application-level write/read/delete check all succeed.

Restore is a transaction on the stopped live volume, not a copy from a staging volume. A restore implementation is acceptable only when it satisfies all of these requirements:

1. Reverify the same digest-pinned control artifact before it mounts the live volume. Use `--pull=never` and `--network none` for every invocation and keep control stopped.
2. Stream authenticated `age` decryption over stdin into that artifact. Within the live volume, safely extract the candidate as a uniquely named, owner-only `.restore-new-*` generation; never decrypt to the host or a named staging volume. Reject absolute paths, `..` traversal, links, devices, sockets, FIFOs, duplicate required roots, and unexpected top-level entries before they can affect another generation.
3. Flush the candidate's files and directories. In one volume-local transaction, rename the stopped live `current` to a uniquely named `.restore-old-*` rollback generation, rename the candidate to `current`, and flush the parent directory. Never copy over, empty, or delete the old `current` in place.
4. While the old generation remains intact and control remains stopped, run `/app/gordon secrets doctor --write-check` against the replacement using the same verified networkless artifact. On any extraction, rename, flush, or doctor failure, restore the old generation to `current` and retain the failed candidate for private diagnosis or remove it only after rollback is proven.
5. Start control and complete the normal health checks before marking the transaction committed. Retain `.restore-old-*` as the rollback generation until an explicit later cleanup after validation and a new verified encrypted backup. No automatic cleanup or failure trap may delete it early.

The restore mechanism must durably record its phase outside the directories being renamed and fsync both phase changes and volume directory renames. Recovery after a host failure is deterministic: keep control stopped, reverify the exact artifact, then inspect the durable phase and generation names. Before the `current`→`.restore-old-*` phase, discard only an incomplete `.restore-new-*`. After the old rename but before a recorded successful doctor and health check, move any replacement aside and rename `.restore-old-*` back to `current`. After recorded validation, keep the replacement as `current` and retain `.restore-old-*` until explicit cleanup. If the phase record and directory layout disagree, make no changes, preserve every generation, and escalate for offline recovery.

These are requirements, not a copy-and-paste shell recipe. Podman shell pipelines cannot make signal handling, archive path validation, fsync ordering, and host-crash recovery concise enough to publish as pseudo-transactional safety. Use a reviewed restore tool or operator runbook that implements and tests every invariant above. Never substitute a distribution helper, a mutable tag, a network-enabled container, or persistent plaintext staging.

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

`plan` is read-only and must report `"ready": true`. `prepare` writes private role manifests/environment, creates the private component network, and starts the probe-only prepared components while the host service remains stopped. `switch` requires authenticated prepared-edge state, transfers runtime authority, and activates final listeners.

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
