# CLI Command Parity Matrix

This matrix tracks parity between remote mode (`--remote`) and local mode (no `--remote`).

## Current Gaps

| Command | Remote | Local | Gap | Owner Files |
|---|---|---|---|---|
| `gordon push` | Supported | Not supported | Requires remote mode | `internal/adapters/in/cli/push.go` |
| `gordon rollback` | Supported | Not supported | Requires remote mode | `internal/adapters/in/cli/rollback.go` |
| `gordon backups list/run/detect/status` | Supported | Not supported | Requires remote mode | `internal/adapters/in/cli/backup.go` |
| `gordon status` | Supported | Not supported | Requires remote mode | `internal/adapters/in/cli/routes.go` |
| `gordon restart --with-attachments` | Supported | Not supported | Local path blocks flag | `internal/adapters/in/cli/restart.go` |

## Target

- All management commands support local mode through in-process service calls.
- Remote mode remains supported through the admin HTTP API.
- Local mode does not proxy through `/admin/*` HTTP endpoints.
