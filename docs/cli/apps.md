# Apps Commands

Validate, persist, inspect, and operate applications.

All mutations are executed by the daemon through the admin API.
Without a reachable daemon the commands fail with `daemon-unavailable`
instead of writing locally. Mutations are idempotent: every request
carries a client-generated key, and ambiguous outcomes must be
re-queried by key before retrying (never retry under a fresh key).

## Local and remote targets

With no `--remote`/`GORDON_REMOTE` selected, commands reach the daemon
through its owner-only administration socket
(`$XDG_RUNTIME_DIR/gordon/admin.sock`, falling back to
`~/.gordon/run/admin.sock`). This works with `auth.enabled=false`: the
socket is owner-only, carries no bearer token, and grants the
`local-owner` principal only app administration and app log reads.
An explicit remote is authoritative and never falls back to the socket.
See [Local-only Mode](../config/auth.md#local-only-mode).

## gordon apps

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `apply` | Validate and persist an app manifest |
| `list` | List applications |
| `show` | Show desired and active state for an app |
| `diff` | Show the normalized desired-vs-active diff |
| `secrets` | Manage app secret values |
| `deploy` | Activate an app revision |
| `restart` | Restart an app from pinned digests |
| `stop` | Stop an app (preserves all data) |
| `start` | Start a stopped app from active state |
| `remove` | Remove app workloads (volumes and secrets are retained) |
| `status` | Show effective vs observed state for an app |
| `logs` | Show logs for an app service |

---

## gordon apps apply

```bash
gordon apps apply --file blog.toml [--dry-run] [--deploy] [--json]
```

Validates a manifest file and persists it as desired state (or dry-runs).
With `--deploy`, chains exactly the accepted revision into a deploy after
persistence succeeds; the two outcomes are reported separately because a
deploy may fail after the apply succeeded.

`--dry-run` and `--deploy` cannot be combined.

---

## gordon apps list

```bash
gordon apps list [--json]
```

Lists applications with one row per app (`active`, `stopped`, or `pending`).

---

## gordon apps show

```bash
gordon apps show APP [--json]
```

Shows desired revision, per-service effective state (ids and digests only,
never secret values), stopped intent, and the last operation.

---

## gordon apps diff

```bash
gordon apps diff APP [--json]
```

Shows the normalized desired-vs-active diff (`added`, `removed`, `changed`).

---

## gordon apps secrets

Values are accepted via `KEY=VALUE` arguments (discouraged: shell history),
`--stdin` (preferred), or an interactive prompt. Names must already exist in
desired or active state. Only key names are ever echoed back — never values.

```bash
gordon apps secrets set APP --service SVC KEY=VALUE… [--stdin] [--json]
gordon apps secrets delete APP KEY --service SVC [--json]
```

`--service` is required: secrets are service-scoped.

---

## gordon apps deploy

```bash
gordon apps deploy APP [--revision REV] [--service SVC] [--json]
```

Activates a revision (default: desired head). Fail-fast across services:
the first failure stops the deploy, successful services are preserved,
later services stay unchanged.

---

## gordon apps restart

```bash
gordon apps restart APP [--service SVC] [--json]
```

Restarts from pinned digests without re-resolution.

---

## gordon apps stop

```bash
gordon apps stop APP [--json]
```

Persists the durable stopped intent and stops exact containers.
All data (volumes, secrets) is preserved. Stopped apps stay stopped
across reboot.

---

## gordon apps start

```bash
gordon apps start APP [--json]
```

Clears the stopped intent and ensures running from active state.

---

## gordon apps remove

```bash
gordon apps remove APP [--json]
```

Withdraws workloads. Volumes and secrets are retained as owned orphans
under the old internal UUID; removing frees the name but never implicitly
attaches retained resources to a new app reusing the name.

There is deliberately no `purge`: destructive volume deletion requires a
separately accepted destructive-action contract.

---

## gordon apps status

```bash
gordon apps status APP [--json]
```

Shows effective vs observed state per service.

---

## gordon apps logs

```bash
gordon apps logs APP [--service SVC] [--follow] [--tail N] [--json]
```

Streams container logs by container ref. `--service` is required when the
app has several services.

---

## Workflow Example

```bash
# Push the image first (OCI transfer only)
gordon push myapp --build --remote prod

# Apply the manifest that references the pushed tag
gordon apps apply --file blog.toml --remote prod

# Write secret values for registered names
gordon apps secrets set blog --service web DATABASE_URL=... --remote prod

# Deploy the accepted revision
gordon apps deploy blog --remote prod

# Inspect
gordon apps show blog --remote prod
gordon apps status blog --remote prod
```

## Related

- [CLI Overview](./index.md)
- [Push Command](./push.md)
