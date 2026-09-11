# Secrets Commands

Manage installation secrets on local or remote Gordon instances.

Storage depends on the secrets backend:
- `pass`: secrets are stored in pass under `gordon/env/<domain>/<KEY>`
- `sops`: secrets live in domain `.env` files and can reference SOPS-encrypted values
- `unsafe`: secrets are stored in plain-text domain `.env` files

App secret VALUES stay in pass under
`gordon/apps/<uuid>/<service>/<name>` and are managed with
`gordon apps secrets`. The commands below manage installation secrets only.

## gordon secrets

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all secret keys for a domain |
| `set` | Set secrets for a domain from a file |
| `remove` | Remove a secret |

---

## gordon secrets list

List all secret key names for a specific domain. Only key names are shown, never values.

```bash
gordon secrets list <domain>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name to list secrets for |

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output secrets as JSON |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon secrets list myapp.example.com

# Remote (override)
gordon secrets list myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
```

### Output

```
Secrets for app.mydomain.com

Key                       Value
DATABASE_URL              (hidden)
API_KEY                   (hidden)
```

### JSON Output

```bash
gordon secrets list app.mydomain.com --json
```

```json
{
  "domain": "app.mydomain.com",
  "keys": ["API_KEY", "DATABASE_URL"]
}
```

---

## gordon secrets set

Set secrets for a domain from a mode 0600 file containing one `KEY=value` pair per line.

```bash
gordon secrets set <domain> --from-file <path>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name to set secrets for |

### Options

| Option | Description |
|--------|-------------|
| `--from-file` | Read KEY=value lines from a mode 0600 file (required) |
| `--json` | Output as JSON |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon secrets set myapp.example.com --from-file ./app.env

# Remote (override)
gordon secrets set myapp.example.com --from-file ./app.env --remote https://gordon.mydomain.com --token $TOKEN
```

---

## gordon secrets remove

Remove a secret from a domain.

```bash
gordon secrets remove <domain> <key>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `<domain>` | The domain name |
| `<key>` | The secret key to remove |

### Options

| Option | Description |
|--------|-------------|
| `--force`, `-f` | Remove without confirmation |
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token` | Authentication token for remote |

### Examples

```bash
# Local
gordon secrets remove myapp.example.com DATABASE_URL

# Remote (override)
gordon secrets remove myapp.example.com DATABASE_URL --remote https://gordon.mydomain.com --token $TOKEN
```

---

## Workflow Examples

### Setting Up Application Secrets

```bash
# Write secrets to a protected file
cat > app.env <<'EOF'
DATABASE_URL=postgres://user:pass@postgres:5432/mydb
API_KEY=your-api-key
EOF
chmod 600 app.env

gordon secrets set myapp.example.com --from-file ./app.env

# Verify
gordon secrets list myapp.example.com
```

### CI/CD Secret Management

```bash
# In your CI/CD pipeline
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=$GORDON_TOKEN

# Update secrets
gordon secrets set myapp.example.com --from-file ./app.env
```

### Rotating Secrets

```bash
# Generate new secret
NEW_JWT_SECRET=$(openssl rand -base64 32)
printf 'JWT_SECRET=%s\n' "$NEW_JWT_SECRET" > rotate.env
chmod 600 rotate.env

# Update the secret
gordon secrets set myapp.example.com --from-file ./rotate.env
```

## Related

- [CLI Overview](./index.md)
- [Secrets Configuration](../config/secrets.md)
- [Environment Variables](../config/env.md)
- [Apps Commands](./apps.md)
