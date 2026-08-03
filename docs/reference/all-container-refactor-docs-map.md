# Split-mode documentation map

Gordon's current product supports monolith operation and an orchestrated four-role split deployment.

| Topic | Canonical document | Owner | Blocking gate |
| --- | --- | --- | --- |
| Install and initial service | [Installation](../installation.md) | Docs | `go test ./...` |
| First deploy | [Getting started](../getting-started.md) | Docs | `go test ./...` |
| Split architecture and trust boundaries | [Split mode](../operations/split-mode.md) | Architecture | `make compat-harness-security` |
| Split update and rollback boundary | [Split updates and rollback](../operations/split-updates-and-rollback.md) | Runtime | `make compat-harness-proxy` |
| Current config | [Configuration reference](../config/reference.md) and `gordon.toml.example` | Config | `make proto-check` and config compatibility gate |
| Runtime/environment ownership | [Environment variables](./env-variables.md) | Runtime | `make compat-harness-security` |
| Network and socket isolation | [Security hardening](../config/security-hardening.md) | Security | `make compat-harness-security` |
| Labels and component inventory | [Labels](./docker-labels.md) | Runtime | `make compat-harness-runtime` |
| Compatibility verification | [Compatibility harness](./compatibility-harness.md) | Release | all `make compat-harness-*` targets |
| Blocking release checks | [Release gates](./release-gates.md) | Release | CI required checks and `make release-smoke` |
| Operational diagnosis | [Troubleshooting](./troubleshooting.md) | Operations | `go test ./...` |
| Safe engineering handoff | [Agent handoff](../development/agent-handoff.md) | Engineering | PR checklist below |

## Change ownership and PR gate matrix

A change must update every canonical document in its row, identify the listed owner in review, and run the exact listed gate. Cross-cutting changes run the union of affected gates. Release changes additionally require immutable action/image pin review, `make release-check`, and non-publishing `make release-smoke`; publishing remains blocked until that smoke completes.

## Invariants

- Only runtime receives an engine socket or engine endpoint.
- Split registry traffic reaches the `gordon-registry` private-network alias, never loopback.
- Generated role TOML and `0600` role environment files are the deployment source of truth.
- External infrastructure owns the split edge's public TLS boundary.
- Migration is checkpointed and resumed with `plan`, `prepare`, `status`, `switch`, and `resume`; there is no migration `rollback` command.
- Retry state is durable and must not be deleted to force progress.
