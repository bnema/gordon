# Gordon v2.50.0 — Contract Index (P0.1)

Status: draft for maintainer acceptance. This directory freezes the
consequential decisions D1–D6 from the declarative-apps plan before any
P1+ implementation. No code in this stack slice implements these
contracts; each document ends with an explicit **Decision required**
gates list.

Baseline: `main` at `da9b3d24` (verified equal to `origin/main` on
2026-09-08). PR #242 (`feat/service-http-routes`, tip `cd1e023f`)
remains untouched; its traffic design is superseded only by the
contracts below, after its tip is preserved.

## Documents

| File | Decision gate | Summary |
|---|---|---|
| `01-manifest.md` | D6 | App TOML schema: sections, strictness, identity rules, TOML examples |
| `02-state.md` | D1 + pass consistency | App-state store: layout, atomicity, journal, pass secret identity |
| `03-deployment.md` | D2 + restart authority | Deploy engine: preflight, ordering, readiness, partial failure, restart ownership |
| `04-network.md` | D3 + RCON policy | Traffic projection, reservations, backend reachability, RCON private default |
| `05-api-cli.md` | D4 + D5 | Admin DTOs, auth scopes, retry/idempotency, CLI lifecycle surface |
| `06-config-disposition.md` | D6 (global side) | Field-by-field global keep/move/remove table, reloadability, rejection |
| `07-runtime-evidence.md` | P0.2 | Docker/Podman fixture results backing D1–D3 |

## Reading order

1. `01-manifest.md` — what the user writes.
2. `02-state.md` — where desired/active state lives.
3. `03-deployment.md` — how desired becomes active.
4. `04-network.md` — how active becomes reachable.
5. `05-api-cli.md` — how the operator drives 1–4.
6. `06-config-disposition.md` — what happens to the existing `gordon.toml`.
7. `07-runtime-evidence.md` — why the above is believed feasible.

## Conventions used across documents

- `APP` = globally unique app name. `SERVICE` = service name unique
  within its app. `REV` = apply revision (`rev-<ulid>`).
- `OP` = deploy operation id (`op-<ulid>`).
- Normative keywords MUST / MUST NOT / SHOULD follow RFC 2119.
- Every wire-facing name is frozen by these documents; P1+ code that
  renames a frozen field is a contract change requiring re-acceptance.
- Secrets never appear in state files, diffs, logs, backups metadata,
  or any example in this directory.
