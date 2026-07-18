# Split-mode documentation map

Gordon's current product supports monolith operation and an orchestrated four-role split deployment.

| Topic | Canonical document |
| --- | --- |
| Install and initial service | [Installation](../installation.md) |
| First deploy | [Getting started](../getting-started.md) |
| Split architecture and trust boundaries | [Split mode](../operations/split-mode.md) |
| Rootless migration and resume | [Migration runbook](../operations/migration.md) |
| Split update and rollback boundary | [Split updates and rollback](../operations/split-updates-and-rollback.md) |
| Current config | [Configuration reference](../config/reference.md) and `gordon.toml.example` |
| Runtime/environment ownership | [Environment variables](./env-variables.md) |
| Network and socket isolation | [Security hardening](../config/security-hardening.md) |
| Labels and component inventory | [Labels](./docker-labels.md) |
| Compatibility verification | [Compatibility harness](./compatibility-harness.md) |
| Blocking release checks | [Release gates](./release-gates.md) |
| Operational diagnosis | [Troubleshooting](./troubleshooting.md) |
| Safe engineering handoff | [Agent handoff](../development/agent-handoff.md) |

## Invariants

- Only runtime receives an engine socket or engine endpoint.
- Split registry traffic reaches the `gordon-registry` private-network alias, never loopback.
- Generated role TOML and `0600` role environment files are the deployment source of truth.
- External infrastructure owns the split edge's public TLS boundary.
- Migration is checkpointed and resumed with `plan`, `prepare`, `status`, `switch`, and `resume`; there is no migration `rollback` command.
- Retry state is durable and must not be deleted to force progress.
