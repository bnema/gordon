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
set -o pipefail
podman stop "$control_id"
test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = false
umask 077
backup_file="$PWD/gordon-control-secrets.tar.gz.age"
age_recipient='age1exampleoperatorrecipient000000000000000000000000000000000'
age_identity='/private/path/to/age-identity'
verify_volume="gordon-secrets-backup-verify-$(date +%s)"
control_image="$(podman inspect --format '{{.ImageName}}' "$control_id")"
doctor_config="$(mktemp)"
printf '%s\n' '[auth]' 'enabled = false' 'secrets_backend = "pass"' \
  '[runtime]' 'endpoint = "192.0.2.1:9444"' 'insecure = true' \
  'token = "backup-validation-only-not-a-runtime-credential"' >"$doctor_config"
trap 'rm -f "$doctor_config"; podman volume rm -f "$verify_volume" >/dev/null 2>&1 || true' EXIT HUP INT TERM
podman run --rm -v "$secret_volume:/source:ro" docker.io/library/alpine:latest \
  tar -C /source -czf - . | age --encrypt --recipient "$age_recipient" >"$backup_file"
test -s "$backup_file"
podman volume create "$verify_volume" >/dev/null
age --decrypt --identity "$age_identity" "$backup_file" | \
  podman run --rm -i -v "$verify_volume:/verify" docker.io/library/alpine:latest \
    sh -ec 'umask 077; chmod 700 /verify; tar -C /verify -xzf -'
podman run --rm -v "$verify_volume:/verify:ro" docker.io/library/alpine:latest sh -ec '
  test -d /verify/current/gnupg
  test -d /verify/current/password-store
  test -f /verify/current/.gordon-managed-pass-fingerprint
  test -f /verify/current/password-store/.gpg-id
  test -z "$(find /verify -mindepth 1 ! -type d ! -type f -print -quit)"
  test -z "$(find /verify -mindepth 1 -maxdepth 1 ! -name current ! -name .control.lock -print -quit)"
  test -z "$(find /verify/current -mindepth 1 -maxdepth 1 ! -name gnupg ! -name password-store ! -name .gordon-managed-pass-fingerprint -print -quit)"
'
podman run --rm -v "$verify_volume:/var/lib/gordon/secrets" \
  -v "$doctor_config:/tmp/gordon-doctor.toml:ro" \
  -e GNUPGHOME=/var/lib/gordon/secrets/current/gnupg \
  -e PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store \
  "$control_image" secrets doctor --config /tmp/gordon-doctor.toml --write-check >/dev/null
podman volume rm "$verify_volume" >/dev/null
rm -f "$doctor_config"
trap - EXIT HUP INT TERM
```

Do not declare or rotate to this backup unless decryption, authenticated archive extraction, structural checks, and the actual control artifact's `secrets doctor --write-check` all succeed. The doctor runs as the image's default user and performs managed-pass write/read/delete validation in the temporary verification volume. Cleanup is trapped on every exit; no plaintext archive is written to persistent storage.

Restore only while control remains stopped. First decrypt and authenticate the complete archive into a new owner-only staging volume. The live volume is not mounted during this phase, so a wrong identity, truncated ciphertext, malformed archive, or failed validation cannot damage the valid store. Supply the age identity through a private operator-controlled path.

```bash
age_identity='/private/path/to/age-identity'
staging_volume="gordon-secrets-restore-stage-$(date +%s)"
control_image="$(podman inspect --format '{{.ImageName}}' "$control_id")"
doctor_config="$(mktemp)"
test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = false
umask 077
printf '%s\n' '[auth]' 'enabled = false' 'secrets_backend = "pass"' \
  '[runtime]' 'endpoint = "192.0.2.1:9444"' 'insecure = true' \
  'token = "restore-validation-only-not-a-runtime-credential"' >"$doctor_config"
podman volume create "$staging_volume" >/dev/null
trap 'rm -f "$doctor_config"; podman volume rm -f "$staging_volume" >/dev/null 2>&1 || true' EXIT HUP INT TERM
set -o pipefail
age --decrypt --identity "$age_identity" "$backup_file" | \
  podman run --rm -i -v "$staging_volume:/staging" docker.io/library/alpine:latest \
    sh -ec 'umask 077; chmod 700 /staging; tar -C /staging -xzf -'
```

Validate the staged tree before the stopped live volume is mounted. Only directories and regular files are accepted: symlinks, devices, sockets, FIFOs, unexpected root entries, and an incomplete managed-store layout all fail closed.

```bash
podman run --rm -v "$staging_volume:/staging:ro" docker.io/library/alpine:latest sh -ec '
  test -d /staging/current
  test -d /staging/current/gnupg
  test -d /staging/current/password-store
  test -f /staging/current/.gordon-managed-pass-fingerprint
  test -f /staging/current/password-store/.gpg-id
  test -z "$(find /staging -mindepth 1 ! -type d ! -type f -print -quit)"
  test -z "$(find /staging -mindepth 1 -maxdepth 1 ! -name current ! -name .control.lock -print -quit)"
  test -z "$(find /staging/current -mindepth 1 -maxdepth 1 ! -name gnupg ! -name password-store ! -name .gordon-managed-pass-fingerprint -print -quit)"
'
podman run --rm -v "$staging_volume:/var/lib/gordon/secrets" \
  -v "$doctor_config:/tmp/gordon-doctor.toml:ro" \
  -e GNUPGHOME=/var/lib/gordon/secrets/current/gnupg \
  -e PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store \
  "$control_image" secrets doctor --config /tmp/gordon-doctor.toml --write-check >/dev/null
```

The `doctor --write-check` invocation uses the actual control image and its default user. It validates the GPG identity and performs an application-level write/read/delete against staging without printing values.

Only after authenticated extraction and validation succeed, copy the staged `current` tree beside the live tree and publish it with a rename. Keep `.restore-old` until the replacement has started and passed a fresh write/read/delete check. Every failure path below stops control and automatically restores the old tree.

```bash
restore_old() {
  podman stop "$control_id" >/dev/null 2>&1 || true
  podman run --rm -v "$secret_volume:/live" docker.io/library/alpine:latest sh -ec '
    if test -d /live/.restore-old; then
      rm -rf /live/current /live/.restore-new
      mv /live/.restore-old /live/current
    else
      rm -rf /live/.restore-new
    fi
  ' >/dev/null 2>&1 || true
}
trap 'restore_old; rm -f "$doctor_config"; podman volume rm -f "$staging_volume" >/dev/null 2>&1 || true' EXIT HUP INT TERM
podman run --rm -v "$staging_volume:/staging:ro" -v "$secret_volume:/live" docker.io/library/alpine:latest sh -ec '
  umask 077
  test -d /live/current
  test ! -e /live/.restore-new
  test ! -e /live/.restore-old
  cp -a /staging/current /live/.restore-new
  rollback() {
    rm -rf /live/.restore-new
    test -e /live/current || { test ! -d /live/.restore-old || mv /live/.restore-old /live/current; }
  }
  trap rollback EXIT HUP INT TERM
  mv /live/current /live/.restore-old
  mv /live/.restore-new /live/current
  trap - EXIT HUP INT TERM
'
podman start "$control_id" >/dev/null
for attempt in $(seq 1 30); do
  test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = true && break
  sleep 1
done
test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = true
# Release the control lease, then perform the actual managed-pass read/write health check.
podman stop "$control_id" >/dev/null
podman run --rm -v "$secret_volume:/var/lib/gordon/secrets" \
  -v "$doctor_config:/tmp/gordon-doctor.toml:ro" \
  -e GNUPGHOME=/var/lib/gordon/secrets/current/gnupg \
  -e PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store \
  "$control_image" secrets doctor --config /tmp/gordon-doctor.toml --write-check >/dev/null
podman start "$control_id" >/dev/null
test "$(podman inspect --format '{{.State.Running}}' "$control_id")" = true
podman run --rm -v "$secret_volume:/live" docker.io/library/alpine:latest \
  sh -ec 'rm -rf /live/.restore-old /live/.restore-new'
podman volume rm "$staging_volume" >/dev/null
rm -f "$doctor_config"
trap - EXIT HUP INT TERM
```

If startup, doctor, read/write/delete health, or restart fails, the active trap restores the old `current` automatically. Do not manually remove `.restore-old` before the final health check.

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
