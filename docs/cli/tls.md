# TLS Commands

Inspect public TLS/ACME certificate status.

Remote targeting uses client config or an active remote by default.
Use `--remote` and `--token` to override. See [CLI Overview](./index.md).

## gordon tls

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Show public TLS certificate status |

---

## gordon tls status

Display the current public TLS/ACME certificate status, including ACME mode,
certificate details, route coverage, and any errors.

```bash
gordon tls status
gordon tls status --json
gordon tls status --remote https://gordon.mydomain.com --token $TOKEN
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

## Related

- [CLI Overview](./index.md)
- [Status Command](./status.md)
- [Routes Command](./routes.md)
