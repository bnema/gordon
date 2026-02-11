# Getting Started

Deploy your first app with Gordon in under 5 minutes.

## Prerequisites

- A Linux VPS with Docker or Podman installed
- A domain pointing to your VPS (DNS A record)
- Cloudflare account (free tier) for HTTPS termination
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
> Skip to [Step 4](#4-configure-your-registry-domain). For production, continue below.

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

## 4. Configure Your Registry Domain

Edit `~/.config/gordon/gordon.toml`:

```toml
[server]
port = 8080                              # Proxy port (use with Cloudflare)
registry_port = 5000                     # Registry port
gordon_domain = "gordon.mydomain.com"    # Your Gordon domain

[routes]
"app.mydomain.com" = "myapp:latest"      # Domain → Image mapping
```

## 5. Set Up DNS

In Cloudflare (or your DNS provider):

| Type | Name | Content |
|------|------|---------|
| A | `app` | `YOUR_SERVER_IP` |
| A | `registry` | `YOUR_SERVER_IP` |

Enable Cloudflare proxy (orange cloud) for HTTPS.

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

Create a token for remote CLI deploys (skip if auth is disabled):

```bash
gordon auth token generate --subject deploy --scopes push,pull --expiry 0
```

`--expiry 0` creates a non-expiring token. Prefer a finite expiry and a rotation policy unless you explicitly need a long-lived deploy token.

Save this token securely - you'll use it with `gordon --remote` (or a saved remote), and it should be limited to remote deploy use.

## 8. Deploy Your First App

On your local machine:

```bash
# Save and select your Gordon remote (one-time)
gordon remotes add prod https://gordon.mydomain.com --token <your-token>
gordon remotes use prod

# Build, push, and deploy through Gordon
gordon push myapp --build --no-confirm
```

What this command does:

- `gordon push myapp` resolves the route for `myapp`, chooses a version tag,
  pushes image tags to Gordon's registry, and triggers deploy.
- `--build` runs `docker buildx build` first, injecting `VERSION` from the tag
  into the build and then pushing both version and `latest` tags. This requires
  Docker with Buildx; a Podman-only setup is not supported for `--build`.
- `--no-confirm` skips the interactive "Deploy now?" prompt so the deploy runs
  immediately.

Your app is now live at `https://app.mydomain.com`!

## 9. Update Your App

Push a new image to deploy with zero downtime:

```bash
# Make changes, then build + push + deploy
gordon push myapp --build --no-confirm
```

Gordon automatically:
1. Starts the new container
2. Waits for it to be ready
3. Routes traffic to the new container
4. Stops the old container

## Next Steps

- [Installation Guide](./installation.md) - Production setup with firewall and rootless containers
- [Configuration Reference](./config/index.md) - All configuration options
- [Authentication](./config/auth.md) - Secure your registry
- [Environment Variables](./config/env.md) - Configure per-app settings

## Related

- [First Deploy Tutorial](/wiki/tutorials/first-deploy.md)
- [Podman Rootless Setup](/wiki/guides/podman-rootless.md)
