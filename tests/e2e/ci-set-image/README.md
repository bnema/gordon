# E2E: OIDC CI image promotion

A local stack that runs the full promotion path with real components. An
ermite CI job, in a Firecracker VM, gets an OIDC ID token. It pushes an image
to Gordon's CI host (`ci.localtest.me`) and promotes exactly that digest to
one service of a 2-service app. The other service must stay unchanged.

## Topology

```
host 127.0.0.1:18443 ─┐
                      ▼
            edge (caddy:2, tls internal, E2E_EDGE_IP)
            ├─ ermite.localtest.me ─▶ ermite (OIDC issuer https://ermite.localtest.me/-/oidc)
            ├─ files.localtest.me  ─▶ static gordon binary built from this worktree
            └─ gordon / ci / app / api.localtest.me ─▶ gordon :8080 (smart_tcp, cleartext)
runner (ermite-runner, vm isolation) ── guest forge relay ─▶ edge:443
dind (private dockerd) ◀── gordon (shares dind's netns, DOCKER_HOST=tcp://127.0.0.1:2375)
```

- Jobs resolve only `ermite.localtest.me` (to their sandbox gateway). The
  runner relays that port to the edge. The job pins the other edge names to the
  same address in `/etc/hosts`, and the edge routes them by SNI/Host.
- Gordon trusts the edge CA through `SSL_CERT_FILE`. The job installs it with
  `update-ca-certificates`, plus `/etc/docker/certs.d/ci.localtest.me`.
- Gordon runs its workloads in `dind`, never on the host daemon. It shares
  dind's network namespace because it dials backends and its registry on
  `127.0.0.1`.
- The runner uses `vm` isolation in a container (`/dev/kvm`, `/dev/net/tun`,
  `NET_ADMIN`). `container` isolation refuses a rootless Docker daemon.

## Prerequisites

- Rootless Docker with compose, `/dev/kvm` usable by your user.
- An ermite source checkout, given by `ERMITE_SRC` (required). Compose builds
  the forge from its `Dockerfile`.
- The runner image `ermite-runner:e2e-final` (or set `RUNNER_IMAGE`), built
  from an ermite-runner checkout as in its `docs/container.md`:

  ```sh
  scripts/build-kernel.sh tmp/image
  scripts/build-image.sh tmp/image
  docker build -f contrib/container/Containerfile --build-arg VERSION=e2e-final -t ermite-runner:e2e-final .
  ```

- `go`, `jq`, `git`, `curl`, `openssl`, `ss` (iproute2) and `sha256sum`
  (coreutils). For the browser checks: `playwright` and `chromium` on `PATH`
  (set `CHROMIUM` to use another browser binary). No npm install is needed.
- Free `127.0.0.1:18443` (`E2E_EDGE_PORT`) and subnet `10.231.77.0/24` with
  the edge on `10.231.77.10` (`E2E_SUBNET`, `E2E_EDGE_IP`). setup.sh records
  them in `.state/.env` and renders them into `gordon.toml`; change them before
  the first `setup.sh`, or after `teardown.sh`.

## Run

Invoke the scripts with `bash`; they work from any shell, fish included.

```sh
bash setup.sh                  # edge + ermite + runner, fixture repo pushed
bash run.sh probe              # OIDC claims + relay checks
bash playwright.sh probe

bash setup.sh --with-gordon    # + dind + gordon, e2e-demo deployed at v1
bash run.sh release v2         # push + promote web to v2
bash playwright.sh release     # run page OK, no JWT, web=v2, api=v1
bash scenarios.sh              # negative checks; web keeps its version

bash teardown.sh               # or --keep-state to keep logs and secrets
```

`setup.sh` is idempotent. Secrets (ermite admin token, `ERMITE_SECRETS_KEY`,
Gordon token secret), the edge CA, the rendered `gordon.toml`, the admin token
and the run logs live in `.state/` (mode 0700, git-ignored). `run.sh release`
records the version in `.state/runs/release.version`; `playwright.sh release`
expects web to serve it.

The ermite admin token reaches git through `GIT_CONFIG_*` variables and the
ermite CLI through the container environment, never a command line.

## Files

| File | Role |
|---|---|
| `compose.yaml` | project `gordon-e2e-ci`: edge, ermite, runner, dind, gordon, tools |
| `Caddyfile` | edge routes; `ci.localtest.me/-/e2e/ping` is answered by the edge itself |
| `gordon.toml` | template: `ci_domain`, trusted proxy, `[[trusted_issuers]]`, `[[promotion_policies]]` |
| `e2e-demo.toml.tmpl` | app `e2e-demo`: services `web` (app.localtest.me) and `api` (api.localtest.me) |
| `fixture-repo/` | pushed to ermite as `e2e/e2e-demo`: demo Dockerfile, workflows, job scripts |
| `playwright/` | browser checks of the ermite run page and the app |

The policy allows only subject `repo:e2e/e2e-demo:ref:refs/heads/main` to
promote service `web` of `e2e-demo` from repository `e2e-demo`, with audience
`https://ci.localtest.me`.

## Scenarios

| Workflow / script | Checks |
|---|---|
| `probe.yml` | token `iss`, `aud`, `sub`, RS256 + `kid`, lifetime ≤ 1h (header and payload printed, never the token); `ci.localtest.me` and `files.localtest.me` reachable through the relay |
| `release.yml` | `gordon images push --json` and `gordon apps promote` in OIDC mode, then `app.localtest.me` serves the new version |
| `scenarios.sh` (host) | CI host: `_catalog`, manifest/blob pull, `/admin`, `/.well-known` → 404; `/v2/` → 401 with realm `https://ci.localtest.me/auth/token`; plain HTTP → 404 (trusted peer) / 403 (other peer) |
| `scenarios.yml` on `main` | both ID tokens obtained; wrong audience → 401; exchange → 200; catalog/tags/pull/admin/mount → 404; tag image → 400; service `api` → 403; same idempotency key → same op; lookup by key → 200 |
| `scenarios.yml` on `other` | subject outside the policy → 403 |
| `playwright.sh` | run page shows success; no JWT or ermite job token in any job log; web shows the promoted version, api keeps v1 |

Every run log is also scanned for JWTs and ermite job tokens (`run.sh`,
`scenarios.sh`); a log that cannot be fetched or is empty fails the run.

The replay check promotes the digest service `web` runs when `scenarios.sh`
starts, so it never changes the app: after `run.sh release`, web keeps the
released version.

## Cleanup

`bash teardown.sh` runs `docker compose down -v --rmi local` for project
`gordon-e2e-ci` (with placeholder values when `.state/.env` is missing),
removes the seed image `gordon.localtest.me/e2e-demo:v1` it built on the host
(found by its `gordon-e2e-ci.seed` label), and deletes `.state/`. It touches
no other container, network or volume.
