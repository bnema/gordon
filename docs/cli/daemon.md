# Daemon Commands

Inspect and operate the Gordon daemon: status, process logs, reload, configuration, TLS, traffic, and networks.

These commands target the local daemon through its owner-only socket, or the daemon selected with `--remote`, `GORDON_REMOTE`, or the active remote. See [CLI Overview](./index.md).

## gordon daemon

| Subcommand | Description |
|------------|-------------|
| `status` | Show daemon status and the app fleet summary |
| `logs` | Show daemon process logs |
| `reload` | Reload installation configuration |
| `config show` | Show installation configuration |
| `config validate` | Statically validate a local configuration file |
| `tls` | Show public TLS certificate status |
| `traffic` | Show traffic entrypoint, router, and counter status |
| `networks` | List Gordon-managed networks |

---

## gordon daemon status

Display installation identity plus one status line per app (from desired/active state, no container inspection). Per-service detail lives under `gordon apps show APP` and `gordon apps status APP`.

```bash
gordon daemon status
gordon daemon status --remote prod
```

`gordon daemon status` works in local mode and remote mode.

- Local mode reads status from in-process services.
- Remote mode reads status from the target admin API.

### Output

```
Gordon Status

Gordon Domain: gordon.example.com
Registry Port: 5000
Server Port: 8088
Apps: 3
Network Isolation: false

Container Status:
  blog: active
  shop: deploying
  old-site: stopped
```

### Information Displayed

| Field | Description |
|-------|-------------|
| Gordon Domain | Public Gordon domain from configuration |
| Registry Port | Docker registry port |
| Server Port | Gordon admin port |
| Apps | Total apps in desired/active state |
| Network Isolation | Whether installation network policy is enabled |
| Container Status | Fleet status per app (see states below) |

### App States

| State | Description |
|-------|-------------|
| active | App deployed and converged on desired state |
| deploying | Desired state diverges from effective state |
| pending | App applied but never deployed |
| stopped | Durable stopped intent (stays stopped across reboot) |

### Flags

Uses the global remote flags:

| Flag | Description |
|------|-------------|
| `--remote, -r` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token-file` | Read the remote token from a mode 0600 file |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `GORDON_REMOTE` | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `GORDON_TOKEN` | Authentication token |

### Examples

### Check Local or Remote Status

```bash
# Local
gordon daemon status

# Using a saved remote
gordon daemon status --remote prod

# Using environment variables
export GORDON_REMOTE=https://gordon.mydomain.com
export GORDON_TOKEN=your-token
gordon daemon status
```

### Quick Fleet Check

```bash
# Check for non-converged apps
gordon daemon status --remote prod | grep -E "(deploying|pending|stopped)"
```

### Required Permissions (Remote Only)

Remote status calls require `admin:status:read` scope in the authentication token.

```bash
# Generate token with required scope
gordon auth token generate --subject admin --scopes admin:status:read
```

---

## gordon daemon logs

Display Gordon daemon process logs. Application workload output is read with
`gordon apps logs APP --service SVC`, which resolves the app's active container
through the daemon.

### Synopsis

```bash
gordon daemon logs [options]
```

### Options

| Option | Short | Default | Description |
|--------|-------|---------|-------------|
| `--config` | `-c` | Auto | Path to config file |
| `--follow` | `-f` | false | Follow log output (like `tail -f`) |
| `--lines` | `-n` | 50 | Number of lines to show |
| `--remote, -r` | | | Remote name or URL (e.g., prod, https://gordon.mydomain.com) |
| `--token-file` | | | Read the remote token from a mode 0600 file |

Remote targeting uses client config or an active remote by default. Use
`--remote` and `--token-file` to override. See [CLI Overview](./index.md).

Remote log access requires an admin token with `admin:logs:read` (or `admin:*:*`). `admin:status:read` is not sufficient for logs.

### Examples

```bash
# Gordon process logs
gordon daemon logs              # Last 50 lines
gordon daemon logs -f           # Follow logs
gordon daemon logs -n 100       # Last 100 lines
gordon daemon logs -f -n 200    # Follow, starting from last 200 lines

# App service logs
gordon apps logs blog --service web
gordon apps logs blog --service web --follow --remote prod

# Remote process logs (override)
gordon daemon logs --remote prod
```

### Log Locations

```bash
# Using gordon daemon logs
gordon daemon logs -f

# Direct file access
tail -f ~/.gordon/logs/gordon.log

# With systemd
journalctl --user -u gordon -f

# App service logs through Gordon
gordon apps logs blog --service web --tail 50
gordon apps logs blog --service web --follow
```

---

## gordon daemon reload

Reload installation configuration.

### Synopsis

```bash
gordon daemon reload
```

### Description

Sends `SIGUSR1` to the running Gordon process, triggering:

- Installation settings reload (live keys apply, restart-required keys are reported)
- Traffic/proxy state refresh from ACTIVE app projection

Reload never activates pending desired app state, re-resolves images, or flips intent. Obsolete application keys (`routes`, `attachments`, `services`, `auto`, previews, …) are rejected with a `config-retired` diagnostic before any mutation.

### Example

```bash
# After editing gordon.toml, apply changes without restart
vim ~/.config/gordon/gordon.toml
gordon daemon reload
```

---

## gordon daemon config show

Display the Gordon installation configuration including server settings,
network isolation, volumes, and external route domains. App routes live
under `gordon apps show`, never here. Sensitive filesystem paths and upstream external route targets are redacted by default.

```bash
gordon daemon config show
gordon daemon config show --json
gordon daemon config show --remote prod
```

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### JSON Output

```json
{
  "server": {
    "port": 1111,
    "registry_port": 5000,
    "registry_domain": "reg.example.com"
  },
  "network_isolation": {
    "enabled": true,
    "prefix": "gordon"
  },
  "volumes": {
    "auto_create": true,
    "prefix": "gordon",
    "preserve": true
  },
  "external_routes": [
    {"domain": "reg.example.com"}
  ]
}
```

External route targets and `server.data_dir` are intentionally omitted from the default admin config response because they reveal internal network and filesystem layout.

---

## gordon daemon config validate

Statically validates a candidate configuration file before it is installed.
This command is local-only: `--remote` is rejected. Validation is static —
runtime, ACTIVE-state, secret, pull, and listener checks are not performed.
A file that fails validation exits non-zero; `--json` is still written first.

```bash
gordon daemon config validate --file ./gordon.toml
gordon daemon config validate --file ./gordon.toml --json
```

### Flags

| Flag | Description |
|------|-------------|
| `--file` | Local candidate configuration file (required) |
| `--json` | Output as JSON |

### JSON Output

On success:

```json
{
  "valid": true,
  "diagnostics": [],
  "scope": "static"
}
```

On failure, `valid` is `false` and `diagnostics` carries the failure:

```json
{
  "valid": false,
  "diagnostics": [
    {"code": "config-invalid", "key": "", "message": "configuration failed static validation"}
  ],
  "scope": "static"
}
```

---

## gordon daemon tls

Inspect public TLS/ACME certificate status.

Gordon serves normal HTTPS fallback on TLS-capable entrypoints such as `entrypoints.edge` with `protocol = "smart_tcp"`. Certificate priority is static certificates first, then public ACME certificates, then Gordon's internal CA.

ACME challenge notes:

- DNS-01 (`cloudflare-dns-01`) does not require a special external port 80 edge.
- HTTP-01 requires an HTTP-capable smart TCP entrypoint reachable on external port 80 for every hostname being validated.
- TLS-ALPN-01 is not supported.

Display the current public TLS/ACME certificate status, including ACME mode,
certificate details, route coverage, and any errors.

```bash
gordon daemon tls
gordon daemon tls --json
gordon daemon tls --remote prod
```

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### Human Output

```text
Public TLS / ACME Status

ACME: enabled
Configured Mode: auto
Effective Mode: http-01
Reason: configured
Token Source: env

Certificates
  ID: cert-abc123
  Names: example.com, www.example.com
  Status: valid
  Not After: 2026-05-29 12:00:00

Route Coverage
  example.com  covered=yes  covered_by=cert-abc123
  internal.local  covered=no  error=self-signed cert

Errors
  route internal.local has no ACME cert
```

### JSON Output

```json
{
  "acme_enabled": true,
  "configured_mode": "auto",
  "effective_mode": "http-01",
  "selection_reason": "configured",
  "token_source": "env",
  "certificates": [
    {
      "id": "cert-abc123",
      "names": ["example.com", "www.example.com"],
      "challenge": "http-01",
      "status": "valid",
      "not_after": "2026-05-29T12:00:00Z",
      "renewal_pending": false
    }
  ],
  "routes": [
    {
      "domain": "example.com",
      "covered": true,
      "covered_by": "cert-abc123",
      "required_acme": true
    },
    {
      "domain": "internal.local",
      "covered": false,
      "required_acme": false,
      "error": "self-signed cert"
    }
  ],
  "errors": ["route internal.local has no ACME cert"]
}
```

### Token Source

The `token_source` field indicates where the ACME token was sourced from
(e.g., `env`, `file`, `config`). The token value is never displayed.

---

## gordon daemon traffic

```bash
gordon daemon traffic --remote prod
gordon daemon traffic --remote https://gordon.example.com --json
```

Remote mode queries the running Gordon admin API. Local mode does not synthesize runtime traffic state from a fresh config load; use a configured remote target for authoritative counters and reload status.

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Output machine-readable JSON |
| `--remote`, `-r` | Remote Gordon instance or saved remote name |

### JSON Output

```json
{
  "last_reload_status": "ok",
  "entrypoints": [
    {
      "name": "postgres",
      "address": "0.0.0.0:5432",
      "protocol": "tcp",
      "active": true,
      "active_tcp_connections": 1,
      "active_udp_sessions": 0,
      "total_accepted": 12,
      "total_refused": 0,
      "total_errors": 0,
      "bytes_in": 4096,
      "bytes_out": 8192,
      "smart_tcp": {
        "http_accepted": 0,
        "h2c_accepted": 0,
        "https_fallback_accepted": 0,
        "tls_passthrough_accepted": 0,
        "raw_fallback_accepted": 0,
        "entrypoint_cidr_refused": 0,
        "raw_fallback_cidr_refused": 0,
        "proxy_refused": 0,
        "unknown_no_fallback_refused": 0,
        "malformed_rejected": 0,
        "sniff_timeout": 0,
        "client_hello_too_large": 0
      }
    }
  ],
  "routers": [],
  "services": [],
  "counters": {
    "active_tcp_connections": 1,
    "active_udp_sessions": 0,
    "total_accepted": 12,
    "total_refused": 0,
    "total_errors": 0,
    "bytes_in": 4096,
    "bytes_out": 8192,
    "smart_tcp": {
      "http_accepted": 0,
      "h2c_accepted": 0,
      "https_fallback_accepted": 0,
      "tls_passthrough_accepted": 0,
      "raw_fallback_accepted": 0,
      "entrypoint_cidr_refused": 0,
      "raw_fallback_cidr_refused": 0,
      "proxy_refused": 0,
      "unknown_no_fallback_refused": 0,
      "malformed_rejected": 0,
      "sniff_timeout": 0,
      "client_hello_too_large": 0
    }
  }
}
```

---

## gordon daemon networks

Display Docker networks managed by Gordon, including which containers
are connected to each network.

```bash
gordon daemon networks
gordon daemon networks --json
gordon daemon networks --remote prod
```

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

### JSON Output

```json
[
  {
	"name": "gordon_myapp",
	"driver": "bridge",
	"containers": ["container1", "container2"]
  }
]
```

## Related

- [CLI Overview](./index.md)
- [Apps Commands](./apps.md)
- [Serve Command](./serve.md)
- [Traffic configuration](../config/traffic.md)
- [Remote CLI Management](/wiki/guides/remote-cli.md)
