# All-container refactor docs map

Phase 1 inventory for the split toward `gordon-control`, `gordon-runtime`, `gordon-edge`, and `gordon-registry`.

This is an inventory, not final user documentation. User-facing docs should continue to describe the present product state: Gordon is a single `gordon` binary that can run with Docker or Podman and exposes registry, admin, reverse proxy, and deployment behavior from that process.

## Owner workstreams

| Workstream | Scope for docs updates |
| --- | --- |
| Release/docs | Terminology, install/upgrade narrative, compatibility statements, examples, docs site navigation |
| Control | Config loading, admin API, remote CLI, auth scopes, route/attachment/secret management |
| Runtime | Docker/Podman integration, managed containers, networks, volumes, logs, attachments, runtime socket access |
| Edge | Entrypoints, smart TCP, TLS/ACME, proxy allowlists, public app traffic, raw fallback |
| Registry | OCI registry API, auth/token exchange, upload limits, image push/pull/deploy triggers |

## Requested docs inventory

| Path | Present | Refactor surface | Owner workstream | Required update before split docs ship | Gate proving accuracy |
| --- | --- | --- | --- | --- | --- |
| `README.md` | Yes | Product summary, quick start, CLI examples, feature list | Release/docs | Keep single-binary quick start until split packaging exists; add split component wording only after commands and install artifacts exist. | `go test ./...`; install artifacts/CLI help show any documented component commands. |
| `docs/installation.md` | Yes | Installer, systemd, Docker/Podman setup, ports, runtime socket | Release/docs + Runtime + Edge + Registry | Replace any stale `server.port`/`registry_domain` examples with current entrypoints and `gordon_domain`; when components exist, document which service needs the runtime socket and which ports each component binds. | Install examples validated against `gordon.toml.example`; `gordon serve --help`; runtime smoke tests for Docker/Podman-gated paths where available. |
| `docs/getting-started.md` | Yes | First-run flow, auth, DNS, systemd, bootstrap/push | Release/docs + Control + Registry + Edge | Preserve current one-binary first deploy; later add component topology only after bootstrap/push work unchanged through control/registry split. | CLI integration tests for `bootstrap`, `push`, `remotes`, `auth`; docs examples use current config keys. |
| `docs/upgrading.md` | Yes | Migration notes, config compatibility, operational changes | Release/docs | Add split migration notes only when release behavior is implemented; keep existing migration language factual for current versions. | Upgrade tests/config loader compatibility; release checklist confirms old monolith UX/API still works. |
| `docs/config/reference.md` | Yes | Full config schema and defaults | Control + Edge + Runtime + Registry | This is the main schema map: annotate component ownership after config boundaries exist; remove/avoid stale env examples such as `GORDON_SERVER_PORT` if not supported by current schema. | Config defaults tests; `gordon config show --json` or equivalent generated reference check. |
| `docs/config/security-hardening.md` | Yes | Registry quotas, admin logs, volume pruning, images, smart TCP, networks, runtime profile | Runtime + Edge + Registry + Control | Split controls by component only after enforcement lives in those components; ensure socket exposure guidance says only runtime needs runtime access. | Security/unit tests for upload quotas, image allowlist, raw fallback, log permissions, volume pruning. |
| `docs/config/network-isolation.md` | Yes | Per-app networks, attachments, Docker commands | Runtime | Generalize Docker-specific text where Podman behavior is supported; document runtime-owned network creation after split. | Runtime network tests; Podman-gated tests skip clearly when Podman is unavailable. |
| `docs/config/secrets.md` | Yes | Auth secret backend, route/attachment env secrets | Control + Runtime | Separate control-owned auth token secret from runtime-delivered env/attachment secrets when component boundaries are implemented; remove stale password-auth guidance if present. | Secrets backend tests; CLI/admin tests for route and attachment secrets. |
| `docs/reference/env-variables.md` | Yes | Container env merge, env file naming, secret provider syntax | Runtime + Control | Clarify component env vars versus app container env vars after split; verify file naming for attachments and route domains. | Env merge tests; secrets resolution tests; config/env CLI tests. |
| `docs/reference/docker-labels.md` | Yes | Managed container/image labels, auto-route labels, naming | Runtime + Registry + Control | Keep labels runtime/registry-compatible; after split, identify which component reads image labels and which writes container labels. | Label constants/tests; deploy/auto-route tests; inspect examples match domain label constants. |
| `docs/reference/troubleshooting.md` | Yes | Registry, deployment, network, volume, config, logs diagnostics | Release/docs + Runtime + Edge + Registry | Add split diagnostics only after processes exist; keep current Docker commands and add Podman equivalents where supported. | Troubleshooting commands align with CLI help; runtime/registry/edge health checks documented only when implemented. |
| `wiki/guides/running-in-container.md` | Yes | Containerized Gordon, socket mount, ports, env vars | Runtime + Edge + Registry + Release/docs | High-risk stale page: update `server.port`, `registry_domain`, password auth, and 8080 health examples to current entrypoint/`gordon_domain` behavior before publishing split guidance. | Container image/run examples tested or reviewed against `gordon.toml.example`; no component other than runtime mounts Docker/Podman socket. |
| `wiki/guides/podman-rootless.md` | Yes | Rootless Podman, socket, firewall forwards, config | Runtime + Edge + Release/docs | Update old `server.port` high-port examples to entrypoint mapping; keep Podman-specific registry config current. | Podman-gated tests skip clearly without Podman; manual command review against current config schema. |
| `wiki/examples/production.md` | Yes | Full production config, auth, logging, networks, attachments, deploy workflow | Release/docs + Runtime + Edge + Registry | Replace stale `server.port` and registry-host examples with current `entrypoints.edge`/`gordon_domain`; keep as monolith example until split deployment examples exist. | Example TOML parses; documented deploy workflow matches registry/domain behavior. |
| `wiki/guides/remote-cli.md` | Yes | Remote admin API, tokens, saved remotes, logs | Control + Release/docs | Preserve remote CLI UX/API compatibility; after split, document control endpoint only if endpoint changes are implemented and tested. | Remote CLI tests; admin handler permission tests; `gordon remotes` and `auth` help match docs. |
| `gordon.toml.example` | Yes | Canonical config sample | Control + Edge + Runtime + Registry | Treat as source of truth for examples; update first when split config keys/components land, then align docs. | TOML parse/config default tests; examples in docs grep back to these keys. |

Missing requested docs: none.

## Examples and tutorials inventory

| Path | Present | Refactor surface | Owner workstream | Required update before split docs ship | Gate proving accuracy |
| --- | --- | --- | --- | --- | --- |
| `wiki/examples/index.md` | Yes | Examples navigation and quick snippets | Release/docs | Update snippets to current entrypoint/`gordon_domain` format; link component examples only after they exist. | Links resolve; snippets match `gordon.toml.example`. |
| `wiki/examples/minimal.md` | Yes | Minimal local setup, auth disabled, route push | Release/docs + Edge + Registry | Update stale `server.port` and network-isolation claims; keep local-development scope explicit. | Example TOML parses; local push/access commands match current ports. |
| `wiki/examples/production.md` | Yes | Production example | Release/docs + Runtime + Edge + Registry | Covered above; keep single source of required updates with requested docs table. | Covered above. |
| `wiki/tutorials/index.md` | Yes | Tutorial prerequisites/navigation | Release/docs | Keep prerequisites generic until split packaging exists; update any HTTPS/Cloudflare assumptions to match current edge docs. | Links resolve; prerequisites match installation page. |
| `wiki/tutorials/first-deploy.md` | Yes | Build/tag/push, route config, deploy verification | Registry + Runtime + Control | Prefer current `gordon_domain` registry naming; avoid separate `registry.*` host unless legacy/explicit. | End-to-end deploy smoke test or CLI/registry integration tests. |
| `wiki/tutorials/postgres-service.md` | Yes | Attachments, network isolation, env files, volumes | Runtime + Control | Verify attachment env file naming and service DNS against implementation; after split, document runtime-owned attachment lifecycle. | Attachment deploy tests; network/volume tests; secrets tests. |

## Cross-cutting update checklist

- Keep monolith UX/API compatibility as the default wording until split artifacts are implemented.
- Do not document a runtime socket for control, edge, or registry components; only runtime-owned operations should need Docker/Podman access.
- Use `gordon_domain` and route-capable `entrypoints.*` examples as the current config baseline.
- Keep Docker and Podman wording explicit; Podman checks must skip clearly when the runtime is unavailable.
- Every future user-facing split doc update needs a code-backed gate: config parse/default tests, CLI/admin tests, runtime tests, registry tests, or manual command smoke evidence.
