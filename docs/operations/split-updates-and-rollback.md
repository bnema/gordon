# Split updates and rollback

Gordon exposes migration lifecycle commands, not a general `components update` or `migrate rollback` CLI. Do not invent service-by-service update commands or replace generated role manifests manually.

## Before updating

- Pass all [release gates](../reference/release-gates.md) for the candidate.
- Take a verified backup of config, `server.data_dir`, registry storage, migration checkpoint, and component volumes.
- Confirm no retry/outbox backlog and record `gordon migrate status --json`.
- Keep exactly one runtime owner.

## During migration

Use a maintenance window for the v2-to-split migration. Take and verify a mutually consistent snapshot, prepare an executable restoration script, stop the host `gordon.service`, and keep it stopped while running `migrate plan`, `prepare`, and `switch`.

`prepare` starts candidate roles without transferring public/runtime authority; the old host process is not retained as a serving fallback. A failed cold switch removes a partial final edge and proves the probe-only prepared edge. Repair the cause and rerun `switch`/`resume` only when status reports a retryable failure. Otherwise preserve checkpoint evidence and run the prepared rollback script, which must stop split public listeners before restoring the snapshot and starting exactly one host service.

## After switch

Phase `switched` has no automatic reverse-migration command. Repair/resume the split deployment for normal incidents. Disaster recovery to monolith requires a maintenance window:

1. stop split edge and runtime ownership;
2. preserve checkpoint/outbox and collect redacted diagnostics;
3. restore a verified mutually consistent data/registry/volume backup;
4. start one monolith using the matching binary/config;
5. verify application, registry, and admin paths before reopening traffic.

Never start a monolith against live split-owned runtime state.

## Related

- [Migration](./migration.md)
- [Upgrading](../upgrading.md)
- [Troubleshooting](../reference/troubleshooting.md)
