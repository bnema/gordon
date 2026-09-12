# Getting Started

Deploy your first app with Gordon in under 5 minutes.

## Prerequisites

- A Linux VPS with Docker or Podman installed
- A domain pointing to your VPS (DNS A record)
- A public TCP edge address for Gordon (for example external `:443`, mapped to your chosen `entrypoints.edge.address`)
- [pass](https://www.passwordstore.org/) (password manager) with GPG key initialized

## 1. Install Gordon

```bash
curl -fsSL https://gordon.bnema.dev/install | bash
```

This script automatically detects your OS and architecture, downloads the appropriate binary, and installs it to `/usr/local/bin`.

## 2. Initialize Configuration

```bash
# First run creates the default config
gordon serve
# Press Ctrl+C to stop
```

Config is created at `~/.config/gordon/gordon.toml`.

## 3. Set Up Authentication

Gordon requires a `token_secret` for JWT authentication. You can store it in `pass` or provide it via the `GORDON_AUTH_TOKEN_SECRET` environment variable. We recommend using `pass` to store secrets securely.

> **Local development?** If you just want to try Gordon locally, you can disable auth temporarily:
> ```toml
> [auth]
> enabled = false
> ```
> Skip to [Step 4](#4-configure-your-gordon-domain). For production, continue below.

**Initialize pass (if not already done):**
```bash
# Generate a GPG key if you don't have one
gpg --gen-key

# Initialize pass with your GPG key ID
pass init <your-gpg-key-id>
```

**Create the JWT token secret:**
```bash
# Generate and store a random 32-character secret
openssl rand -base64 32 | pass insert -m gordon/auth/token_secret
```

**Or set it via environment variable:**

```bash
export GORDON_AUTH_TOKEN_SECRET="your-32-character-secret-here"
```

**Update your config** (`~/.config/gordon/gordon.toml`):
```toml
[auth]
enabled = true
secrets_backend = "pass"
token_secret = "gordon/auth/token_secret"
```

## 4. Configure Your Gordon Domain

Edit `~/.config/gordon/gordon.toml`:

```toml
[server]
registry_port = 5000                     # Registry port
gordon_domain = "gordon.mydomain.com"    # Registry + Admin API domain

[entrypoints.edge]
address = ":443"                         # Public smart TCP edge (choose your bind/mapping)
protocol = "smart_tcp"
```

Application workloads are NOT declared in `gordon.toml`. Each app lives in its own standalone TOML file (see step 8). The old `[routes]`, `[attachments]`, `[network_groups]`, `[[services]]`-as-apps, `[service_routes]`, `[auto_route]`, and `[previews]` keys were removed in v2.50 — Gordon refuses to start when any of them is present.

## 5. Set Up DNS (Including Wildcard)

`gordon_domain` is the single domain used by both the registry and admin API.

In Cloudflare (or your DNS provider), create:

| Type | Name | Content | Proxy |
|------|------|---------|-------|
| A | `gordon` | `YOUR_SERVER_IP` | Yes |
| A/CNAME | `*` | `YOUR_SERVER_IP` or `gordon.mydomain.com` | Yes |

Why wildcard (`*`)?
- It automatically covers app hosts like `app.mydomain.com`, `api.mydomain.com`, `demo.mydomain.com`, etc.
- You can add new app HTTP hosts without creating DNS records one by one.

If your DNS provider supports wildcard CNAME flattening (Cloudflare does), `* -> gordon.mydomain.com` is usually the cleanest option.

> **Important for Cloudflare or other proxied setups:** when Gordon serves HTTP paths behind Cloudflare or another proxy, add your proxy edge IPs to `server.proxy_allowed_ips` or proxied HTTP traffic can get `403 Forbidden`. See [Installation](./installation.md#proxy-origin-allowlist).

## 6. Start Gordon as a Service

```bash
# Create systemd user service
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/gordon.service <<EOF
[Unit]
Description=Gordon Container Platform

[Service]
Type=simple
Restart=always
ExecStart=/usr/local/bin/gordon serve

[Install]
WantedBy=default.target
EOF

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now gordon
sudo loginctl enable-linger $USER
```

## 7. Generate a Deploy Token

Create a token for remote CLI use (skip if auth is disabled):

```bash
gordon auth token generate --subject deploy --scopes "push,pull,admin:apps:read,admin:apps:write" --expiry 90d
```

`--expiry 0` creates a non-expiring token. Prefer a finite expiry and a rotation policy unless you explicitly need a long-lived deploy token.

Save this token securely -- you will use it to authenticate with your Gordon server from CI/CD pipelines and remote CLI sessions.

## 8. Deploy Your First App

On your local machine:

```bash
# Save and select your Gordon remote (one-time)
gordon remotes add prod https://gordon.mydomain.com --token <your-token>
gordon remotes use prod

# Build and push the image (OCI transfer only, never deploys)
gordon push myapp --build --remote prod
```

Write the app file (`blog.toml`). The file is intended for Git: it must never contain secret values.

```toml
name = "blog"

[[service]]
name = "web"
image = "gordon.mydomain.com/myapp:latest"

[[service.http]]
host = "app.mydomain.com"
port = 3000
```

Apply the manifest, then deploy:

```bash
gordon apps apply --file blog.toml --remote prod
gordon apps deploy blog --remote prod
```

What these commands do:

- `gordon push` builds, uploads, and stores the image. It never deploys.
- `gordon apps apply` validates the manifest and persists it as desired state.
- `gordon apps deploy` activates the accepted revision: pulls the image, starts the container, waits for readiness, switches traffic, retires the old container.

If the app needs secrets, register their names in the manifest (`[service.secrets]` maps ENV name to secret name), then write values — values stay in pass, never in the file:

```bash
gordon apps secrets set blog --service web APP_ENV=production --remote prod
```

Your app is now live at `https://app.mydomain.com`!

## 9. Update Your App

Push a new image, update the manifest tag if needed, apply, deploy:

```bash
# Make changes, then build + push
gordon push myapp --build --remote prod

# Deploy the new tag (re-resolves mutable tags)
gordon apps deploy blog --remote prod
```

For HTTP services without volumes, Gordon keeps the old container serving until the replacement passes readiness, then switches traffic, drains, and retires the old container. TCP/UDP and volume-owning services replace with interruption.

## Next Steps

- [Installation Guide](./installation.md) - Production setup with firewall and rootless containers
- [Configuration Reference](./config/index.md) - All installation configuration options
- [Apps CLI](./cli/apps.md) - Apply, deploy, and lifecycle commands
- [Authentication](./config/auth.md) - Secure your registry
- [App secrets](./cli/apps.md#gordon-apps-secrets) - Service-scoped secret values in pass

## Related

- [First Deploy Tutorial](/wiki/tutorials/first-deploy.md)
- [Podman Rootless Setup](/wiki/guides/podman-rootless.md)
