# Apps Commands

Validate, persist, inspect, and operate applications.

All mutations are executed by the daemon through the admin API.
Without a reachable daemon the commands fail with `daemon-unavailable`
instead of writing locally. Mutations are idempotent: every request carries a
client-generated key, and the daemon binds that key to the exact request.
Repeating a key replays the recorded result; reusing it for a different request
is refused. An interrupted operation is never executed twice, so an ambiguous
outcome is re-queried by key before any retry (never retry under a fresh
key). Unknown apps are reported as `app not found` and create no state.

## Local and remote targets

With no `--remote`/`GORDON_REMOTE` selected, commands discover the daemon's
owner-only administration socket. The CLI checks `$XDG_RUNTIME_DIR/gordon/admin.sock`
when set, then `/run/user/<uid>/gordon/admin.sock`, then `~/.gordon/run/admin.sock`.
It accepts only a safe owner-owned socket. This works with `auth.enabled=false`:
the socket carries no bearer token and grants the `local-owner` principal only
app administration and app log reads. An explicit remote is authoritative and
never falls back to the socket.
See [Local-only Mode](../config/auth.md#local-only-mode).

## gordon apps

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `apply` | Validate and persist an app manifest |
| `operations` | Inspect app operation journals |
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

When the daemon accepts the deploy asynchronously (HTTP 202), the command
reuses the same by-key watch as `gordon apps deploy`: it polls the
operation journal until terminal, prints progress only when the operation
or step state changes, and never reissues the deploy. A terminal
`partial`/`failed` deploy exits nonzero while still reporting the
successful apply. Ctrl-C stops only local polling: the daemon-side
operation keeps running and the command prints how to resume it.

With `--json`, stdout carries exactly one final combined document after the
deploy reaches a terminal state:

```json
{
  "apply": { "app": "blog", "resulting_revision": "rev-b", "pending": true },
  "deploy": { "op": "op-1", "app": "blog", "status": "success", "outcome": "success" }
}
```

No initial running document is emitted, and progress, transient warnings,
and Ctrl-C resume guidance go to stderr.

`--dry-run` and `--deploy` cannot be combined.

---

## gordon apps list

```bash
gordon apps list [--json]
```

Lists applications with one row per app. States are `applied` when no revision is active, `pending` when desired state awaits activation, `stopped` when stopped intent is set, and otherwise `active`. A non-success last operation is appended to `active` in parentheses.

---

## gordon apps show

```bash
gordon apps show APP [--json]
```

Shows desired revision and acceptance status, pending state, per-service
effective revision, container, digest, and restart safety, the resources the
app owns (volumes, secret paths, image references — never secret values),
stopped intent, and the last operation with its outcome.

---

## gordon apps operations show

```bash
gordon apps operations show APP --key KEY [--json]
```

Recovers one operation journal entry by its client-generated request key.
`--key` is required. Use it to re-query an ambiguous mutation outcome before
retrying: repeating the same key replays the recorded result, while a key
reused for a different request is refused.

### Flags

| Flag | Description |
|------|-------------|
| `--key` | Request key (required) |
| `--json` | Output as JSON |

---

## gordon apps operations watch

```bash
gordon apps operations watch APP --key KEY [--json]
```

Polls the operation journal by request key until the operation reaches a
terminal state, then renders the terminal journal. It never reissues the
mutation, so it is the safe way to resume after an interrupted deploy or to
follow an operation started elsewhere. Progress is printed only when the
operation or step state changes; unchanged polls are not repeated.

Exit status is nonzero for terminal `partial`/`failed` outcomes and for an
interrupted watch. Ctrl-C stops only local polling: the daemon-side
operation is not cancelled and keeps running, and the command prints the
command to resume. With `--json`, stdout carries exactly one final document
and progress goes to stderr.

### Flags

| Flag | Description |
|------|-------------|
| `--key` | Request key (required) |
| `--json` | Output as JSON |

---

## gordon apps diff

```bash
gordon apps diff APP [--json]
```

Shows the normalized desired-vs-active diff (`added`, `removed`, `changed`).

---

## gordon apps secrets

Values are accepted via `KEY=VALUE` arguments (discouraged: shell history) or
stdin. Names must already exist in desired or active state. Only key names are
ever echoed back — never values. Secrets are service-scoped: `--service` is
required for `set` and `delete`, and optional for `list` where it filters to
one service.

### gordon apps secrets list

```bash
gordon apps secrets list APP [--service SVC] [--json]
```

Lists registration metadata for an app's secrets — service, key, name, source,
and presence — never secret values. `--service` filters to one service.

#### Flags

| Flag | Description |
|------|-------------|
| `--service` | Filter by service |
| `--json` | Output as JSON |

#### JSON Output

```json
[
  {"service": "web", "key": "DATABASE_URL", "name": "app_database_url", "source": "desired", "presence": "unknown"}
]
```

### gordon apps secrets set

```bash
gordon apps secrets set APP --service SVC KEY=VALUE… [--json]
gordon apps secrets set APP --service SVC --stdin [--json]
gordon apps secrets set APP --service SVC --stdin --key KEY [--json]
```

Reads values in one of three ways:

- `KEY=VALUE` arguments: the value must be a single line of 1–65536 bytes.
- `--stdin`: reads `KEY=VALUE` lines; blank and whitespace-only lines are
  ignored and every non-blank value byte is preserved.
- `--stdin --key KEY`: reads one raw value for `KEY`; a single trailing newline
  is stripped and the value must still be a single non-empty line.

`--key` requires `--stdin` and cannot be combined with `KEY=VALUE` arguments.
An empty value is rejected, so `KEY=` is invalid.

Running containers keep the values they were created with. Apply new values
with `gordon apps deploy APP --service SVC`: deploy sees the changed secret and
recreates the service. `restart` does not apply new values.

#### Flags

| Flag | Description |
|------|-------------|
| `--service` | Service the secrets belong to (required) |
| `--stdin` | Read `KEY=VALUE` lines, or one raw value with `--key` |
| `--key` | Secret key for single-value stdin mode |
| `--json` | Output as JSON |

#### JSON Output

```json
{"app": "blog", "service": "web", "keys": ["DATABASE_URL"]}
```

### gordon apps secrets delete

```bash
gordon apps secrets delete APP KEY --service SVC [--json]
```

Deletes the registered value for `KEY`.

#### Flags

| Flag | Description |
|------|-------------|
| `--service` | Service the secret belongs to (required) |
| `--json` | Output as JSON |

---

## gordon apps deploy

```bash
gordon apps deploy APP [--revision REV] [--service SVC | --all] [--json]
```

Activates a revision (default: desired head). An app with several services
needs `--service NAME` (one service) or `--all` (every service); without
either, the command fails before any change and lists the services. A
single-service app needs neither. `apps apply --deploy` always deploys every
service.

| Flag | Description |
|------|-------------|
| `--revision REV` | Revision to activate (default: desired head) |
| `--service NAME` | Deploy one service only |
| `--all` | Deploy every service |
| `--json` | Output as JSON |

Deploy is the only command that applies changes. It recreates a service
when its image digest, spec, app environment, or secret values changed. A
new image behind the same tag (for example `latest`) has a new digest and is
deployed. A service already running with all of these unchanged keeps its
container and is reported `unchanged`. Services with host binds or devices
are always replaced, so bind and device policy changes apply on deploy.

Fail-fast across services:
the first failure stops the deploy, successful services are preserved,
later services stay unchanged.

When the daemon accepts the deploy and runs the two phases in the
background (HTTP 202), the command polls the operation journal by key until
it is terminal and prints concise progress only when the operation or step
state changes. It never reissues the mutation. In `--json` mode stdout
carries one final document and progress goes to stderr. Ctrl-C stops only
local polling (the daemon-side operation keeps running) and prints the
`gordon apps operations watch` command to resume.

---

## gordon apps restart

```bash
gordon apps restart APP [--service SVC | --all] [--json]
```

Restarts the same container. Nothing is applied: new secret values, env,
image, or config need `gordon apps deploy`. Traffic is withdrawn, the
container restarts, readiness is checked, and traffic returns. If the
container no longer exists, restart rebuilds it from its pinned digest. An
app with several services needs `--service NAME` or `--all`.

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

Streams logs for a service in the app's active deployment. The app name and
`--service` are the only accepted identity: domains and raw container IDs are
never accepted. `--service` is required when the app has several services.

---

## Workflow Example

```bash
# Push the image first (OCI transfer only)
gordon images push myapp --build --remote prod

# Apply the manifest that references the pushed tag
gordon apps apply --file blog.toml --remote prod

# Write secret values for registered names
gordon apps secrets set blog --service web DATABASE_URL=... --remote prod

# Deploy the accepted revision (polls 202 operations to terminal)
gordon apps deploy blog --remote prod

# Resume watching an interrupted or externally started operation
gordon apps operations watch blog --key <operation-key> --remote prod

# Inspect
gordon apps show blog --remote prod
gordon apps status blog --remote prod
```

## Related

- [CLI Overview](./index.md)
- [Images Commands](./images.md#gordon-images-push)
- [Deployment](../deployment/index.md)
