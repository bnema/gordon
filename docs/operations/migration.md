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

The control secret volume is installation-namespaced as `gordon-control-secrets-<installation-id>`; do not assume an unqualified volume name. Discover the exact source from the control container without printing environment or secret contents:

```bash
control_id="$(podman ps -aq --filter label=gordon.component.role=control)"
secret_volume="$(podman inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/gordon/secrets"}}{{.Name}}{{end}}{{end}}' "$control_id")"
test -n "$secret_volume"
```

For a consistency-safe backup, stop control and verify it is stopped before reading the volume. This releases the exclusive store lease and prevents GPG or `pass` writes during the archive. Stream the archive directly into authenticated encryption; no plaintext archive is created. Replace the recipient placeholder with an operator-held age recipient, keep the encrypted file owner-only, and never list archive contents in shared logs.

```bash
podman stop "$control_id"
test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = false
umask 077
backup_file="$PWD/gordon-control-secrets.tar.gz.age"
age_recipient='age1exampleoperatorrecipient000000000000000000000000000000000'
podman run --rm -v "$secret_volume:/source:ro" docker.io/library/alpine:latest \
  tar -C /source -czf - . | age --encrypt --recipient "$age_recipient" >"$backup_file"
test -s "$backup_file"
```

Restore only while control remains stopped. Decrypt directly into the extractor for the **same discovered namespaced volume** (a stopped container still references it); no plaintext archive is written. Supply the age identity through a private operator-controlled path, then start one control process. Gordon validates the atomically published `current` store and fails closed rather than replacing invalid key material.

```bash
age_identity='/path/to/operator-age-identity'
test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = false
set -o pipefail
age --decrypt --identity "$age_identity" "$backup_file" | \
  podman run --rm -i -v "$secret_volume:/restore" docker.io/library/alpine:latest \
    sh -ec 'find /restore -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; tar -C /restore -xzf -'
podman start "$control_id"
```

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
