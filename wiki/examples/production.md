# Production configuration

Use this source configuration for a rootless Podman host. Gordon generates narrower role configuration during split migration.

```toml
[server]
port = 9000 # generated split edge listen port; match the external forward
registry_port = 5000
gordon_domain = "gordon.example.com"
data_dir = "~/.gordon"

[entrypoints.edge]
address = ":9000"
protocol = "smart_tcp"

[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
access_token_ttl = "15m"

[runtime]
token_env = "GORDON_RUNTIME_HANDOFF_TOKEN"

[containers]
security_profile = "compat"

[network_isolation]
enabled = true
network_prefix = "gordon"
internal = false

[volumes]
auto_create = true
prefix = "gordon"
preserve = true

[images]
allowed_registries = []
require_digest = false

[logging]
level = "info"
format = "json"

[logging.container_logs]
enabled = true
dir = "~/.gordon/logs/containers"

[env]
dir = "~/.gordon/env"

[routes]
"app.example.com" = { image = "app:v1" }
```

Store the auth secret in `pass` and provide the migration seed only to the monolith process:

```bash
openssl rand -base64 32 | pass insert -e gordon/auth/token_secret
export GORDON_RUNTIME_HANDOFF_TOKEN="$(openssl rand -hex 32)"
```

Terminate public TLS in an operator-owned proxy/load balancer and send clear HTTP to the unprivileged generated edge bind (9000 in this example). A raw firewall redirect of TLS port 443 is sufficient for the monolith smart-TCP listener but not for the generated split edge's external-TLS contract. Registry clients use `gordon.example.com`; edge forwards registry traffic to the private `gordon-registry` alias. Do not configure edge to use `localhost:5000`.

Only monolith/runtime receives the rootless Podman endpoint. Generated control, edge, and registry role environments omit it.

```ini
Environment=XDG_RUNTIME_DIR=/run/user/%U
Environment=DOCKER_HOST=unix:///run/user/%U/podman/podman.sock
```

Before migrating, set `GORDON_MIGRATION_IMAGE`, run `gordon migrate plan --json`, and follow the [migration runbook](/docs/operations/migration.md).

## Related

- [Rootless Podman](/wiki/guides/podman-rootless.md)
- [Security](/docs/config/security-hardening.md)
- [Split mode](/docs/operations/split-mode.md)
