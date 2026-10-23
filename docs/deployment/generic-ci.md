# Generic CI/CD Deployment

Use OIDC promotion for CI: push an image, then run exactly its digest in one
service. No long-lived Gordon admin token is required.

## Requirements

- Docker on the runner; Gordon CLI and `jq` for the CLI example, or `curl` and `jq` for the HTTP example
- A public HTTPS [CI host](../config/server.md#ci-host), separate from the main registry/admin host
- A configured [trusted issuer](../config/auth.md#oidc-trusted-issuers) and [promotion policy](../config/promotion.md)
- An existing running app whose other services match desired state; provision app manifests and secrets separately as an administrator

Gordon must reach issuer discovery and JWKS endpoints over HTTPS. The main
host may remain private. Never target the loopback `server.registry_port`.

## CLI: Push and Promote

Obtain a job OIDC JWT with the configured audience and pass it through
`GORDON_OIDC_TOKEN`. Gordon also supports `ACTIONS_ID_TOKEN_REQUEST_URL` and
`ACTIONS_ID_TOKEN_REQUEST_TOKEN`: it fetches a JWT with the audience from
`--oidc-audience` or `GORDON_OIDC_AUDIENCE` (required in this mode).
A non-empty `GORDON_OIDC_TOKEN` takes precedence. With neither source,
the CLI uses stored-token authentication (`GORDON_TOKEN`).

```bash
set -euo pipefail
export GORDON_REMOTE=https://ci.example.com
# GORDON_OIDC_TOKEN is supplied by the job's identity provider.
gordon images push web --build --tag v1.2.0 \
  --remote "$GORDON_REMOTE" --json >push.json
gordon apps promote web --service web \
  --image "$(jq -r .image push.json)" \
  --remote "$GORDON_REMOTE" --json >promote.json
jq '{op, app, status, outcome}' promote.json
```

Push JSON contains `repository`, `tag`, `digest`, and `image`; `image` is
`repository@digest`. Use that value, not a tag or local image ID.
Promotion waits by polling the operation by key. Exit 0 means success;
exit 1 means refusal or unsuccessful completion. JSON mode prints one final
document to stdout; build, upload, and watch progress goes to stderr.

## Docker and curl

Use the OIDC JWT as the Docker password. Do not enable shell tracing or
print JWTs or exchanged tokens. A `pull` scope is needed for HEAD checks,
but the CI host does not serve image pulls.

```bash
set -euo pipefail
CI=https://ci.example.com
docker build -t ci.example.com/web:v1.2.0 .
# Log in right before the push: the JWT may expire during a long build.
printf '%s' "$GORDON_OIDC_TOKEN" |
  docker login ci.example.com --username oidc --password-stdin
docker push ci.example.com/web:v1.2.0
docker logout ci.example.com
# The pushed manifest digest, not a layer digest or image ID.
DIGEST=$(docker image inspect \
  --format '{{range .RepoDigests}}{{println .}}{{end}}' \
  ci.example.com/web:v1.2.0 | grep '^ci.example.com/web@' | cut -d@ -f2)
```

Exchange the JWT for a bearer token to promote; the Basic username is ignored:

```bash
# Keep credentials out of curl's command-line arguments.
BASIC=$(printf 'oidc:%s' "$GORDON_OIDC_TOKEN" | base64 | tr -d '\n')
TOKEN=$(printf 'header = "Authorization: Basic %s"\n' "$BASIC" |
  curl --fail-with-body -sS --config - --get "$CI/auth/token" \
    --data-urlencode 'service=ci.example.com' \
    --data-urlencode 'scope=repository:web:push,pull' | jq -er .token)
unset BASIC

# Persist this key and the exact request body before submitting.
KEY="release-$(date +%s)-$$"
printf '%s\n' "$KEY" >operation-key.txt
jq -n --arg image "web@$DIGEST" \
  '{service:"web",image:$image}' >request.json
printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
  curl --fail-with-body -sS --config - \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $KEY" \
    --data-binary @request.json "$CI/-/ci/apps/web/promote" >operation.json
```

The exchange can also be used by a registry client instead of Docker's
login handshake. `/auth/token` returns `token`, `expires_in`, and `issued_at`.
Use the exchanged token, never the raw JWT, for promotion and lookup.
Poll `GET /-/ci/apps/{app}/operations/by-key/{key}` until `status` is no
longer `running`. For example, a bounded wait:

```bash
for _ in $(seq 1 120); do
  printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
    curl --fail-with-body -sS --config - \
      "$CI/-/ci/apps/web/operations/by-key/$KEY" >operation.json
  case "$(jq -r .status operation.json)" in
    success) exit 0 ;;
    running) sleep 2 ;;
    *) cat operation.json; exit 1 ;;
  esac
done
echo "Wait timed out; query the same key before retrying" >&2
exit 1
```

Refresh the bearer token if it expires while waiting. Lookup is scoped to
the submitting principal and requires a current policy for the app.

## HTTP Results and Retries

Promotion requires an `Idempotency-Key`: 1–128 printable ASCII bytes without
spaces or `/`. Use a URL-safe value for lookup. JSON accepts only `service`, `image`,
and optional `expected_revision`, with a 4 KiB body limit.

| Status | Meaning |
|--------|---------|
| `202` | Accepted, still running; poll by key |
| `200` | Terminal promotion result (including successful replay or no-op); lookup also returns 200 while running |
| `400` | Malformed request, missing or invalid `Idempotency-Key`, tag/non-installation image, or invalid candidate configuration |
| `401` | Invalid identity at exchange, or missing/invalid ci-access bearer token |
| `403` | No matching policy or requested repository scope |
| `404` | Missing app, service, manifest, or visible operation; route outside the CI allowlist |
| `409` | `app-busy`, state/revision conflict, key reuse, or unfinished/failed replay |
| `429` | `/auth/token` rate limit exceeded; `Retry-After: 1` |
| `500` | Promotion unavailable on this daemon, or an internal error |
| `503` | Identity provider unavailable; `Retry-After: 30` |

`app-busy` returns `Retry-After: 5`: nothing was promoted; wait, then submit
with a new key. For an ambiguous network result, query the original key
before any retry. An identical replay returns the recorded operation;
a different request under the same key is refused. The CLI does not retry
409 mutations. Terminal `partial` or `failed` is not success.

The CI host allowlist is:

- `GET /auth/token` and `GET /v2/`
- `HEAD /v2/<repo>/blobs/<digest>`
- `POST /v2/<repo>/blobs/uploads/` without `mount` or `from`
- `PATCH` or `PUT /v2/<repo>/blobs/uploads/<uuid>`
- `PUT` or `HEAD /v2/<repo>/manifests/<ref>`
- `POST /-/ci/apps/{app}/promote`
- `GET /-/ci/apps/{app}/operations/by-key/{key}`

Repositories must be policy-granted. Manifest/blob GET, tag listing,
catalog, deletion, cross-repository mounts, and `/admin` return 404.

## Stored-Token Deployment

For CI without OIDC, use the main host and a least-privilege stored token.
This requires network access to the private main host. Do not send stored
tokens to the CI host.

### 1. Generate Deployment Token

On your Gordon server:

```bash
gordon auth token generate \
  --subject ci-deploy \
  --scopes "push,pull,admin:apps:read,admin:apps:write" \
  --expiry 0
```

### 2. Configure CI Secrets

Add these secrets to your CI system:

| Secret | Value |
|--------|-------|
| `GORDON_TOKEN` | The generated token |
| `GORDON_REMOTE` | `https://gordon.example.com` |

### 3. Pipeline Steps

```bash
# 1. Install Gordon binary
curl -fsSL https://git.bnema.dev/gordon/releases/latest/download/gordon_linux_amd64 \
  -o /usr/local/bin/gordon
chmod +x /usr/local/bin/gordon

# 2. Build and push (does not deploy)
export GORDON_TOKEN="$GORDON_TOKEN"
gordon images push --build \
  --remote "$GORDON_REMOTE"

# Apply the app manifest and activate it explicitly
gordon apps apply --file myapp.toml --remote "$GORDON_REMOTE"
gordon apps deploy myapp --remote "$GORDON_REMOTE"
```

Gordon auto-detects the version from CI environment variables:

| CI System | Variable | Example |
|-----------|----------|---------|
| GitHub Actions | `$GITHUB_REF` | `refs/tags/v1.2.0` |
| GitLab CI | `$CI_COMMIT_TAG` | `v1.2.0` |
| Azure DevOps | `$BUILD_SOURCEBRANCH` | `refs/tags/v1.2.0` |
| Other | `--tag` flag | `gordon images push --tag v1.2.0` |

## Alternative: docker push

If installing the Gordon binary is not possible:

```bash
# 1. Login to Gordon registry
echo "$GORDON_TOKEN" | docker login gordon.example.com \
  -u ci-deploy --password-stdin

# 2. Build and push
docker build -t gordon.example.com/myapp:v1.0.0 .
docker push gordon.example.com/myapp:v1.0.0
```

This requires the token subject (`ci-deploy`) as the username.
Pushing only stores the image; deploy explicitly with `gordon apps deploy` afterwards.

## Token Scopes Reference

Use the minimum scopes for the steps the pipeline performs. A repository restriction limits registry access only; it does not narrow `admin:*` permissions.

| Workflow | Required Scopes |
|----------|----------------|
| Build + push, then apply + deploy | `push,pull,admin:apps:read,admin:apps:write` |
| `gordon images push` only (no deploy) | `push,pull,admin:apps:read` |
| `docker push` only (no deploy) | `push,pull` |
| docker push, then apply + deploy | `push,pull` + `admin:apps:read,admin:apps:write` for the CLI step |

## Troubleshooting

### "failed to verify token"

The token is invalid or has been revoked. Generate a new one.

### "deploy target not found" / app has no desired state

Push only stores the image. Declare the app first:

```bash
gordon apps apply --file myapp.toml --remote "$GORDON_REMOTE"
gordon apps deploy myapp --remote "$GORDON_REMOTE"
```

### Version shows "latest"

Gordon could not detect a version tag. Either:
- Tag your git repo: `git tag v1.0.0 && git push --tags`
- Pass explicitly: `gordon images push --tag v1.0.0`

### Large images time out

Gordon uploads in 50MB chunks. For very large images (> 1GB), the push may take several minutes. Ensure your CI runner timeout is sufficient.

## Related

- [GitHub Actions](./github-actions.md)
- [GitLab CI](./gitlab-ci.md)
- [Deployment Overview](./index.md)
- [Authentication](../config/auth.md)
- [Images Commands](../cli/images.md#gordon-images-push)
