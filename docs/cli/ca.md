# CA Commands

Manage Gordon's internal Certificate Authority.

## gordon ca

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `export` | Export the root CA certificate in PEM format |
| `info` | Show CA status (root CN, fingerprint, intermediate expiry) |
| `install` | Install/uninstall the root CA in system trust stores |

All subcommands require Gordon to have TLS-capable HTTPS fallback configured on an entrypoint such as `entrypoints.edge` with `protocol = "smart_tcp"`.

### Local and remote

`export` and `info` target a remote with the same rules as other commands: `--remote`, then `GORDON_REMOTE`, then the active remote saved with `gordon remotes`. When a remote is targeted, they fetch the CA through the authenticated admin API (`GET /admin/ca`, scope `admin:status:read`). The remote answers `404` when its internal TLS is disabled.

The fetched certificate is checked locally: it must be a single self-signed CA certificate, and the displayed common name and fingerprint are computed from it. If the server reports a different fingerprint, the command fails. These checks prove the certificate is well formed, not where it came from: over an `insecure_tls` or `http://` remote the response can be replaced in transit. `export` prints the fingerprint on stderr; compare it with `gordon ca info` on the Gordon host before trusting the certificate.

With no remote targeted, `export` and `info` read the local server config (`--config`, or `gordon.toml` in `/etc/gordon`, `~/.config/gordon`, or the current directory) and its data directory. If that config has no TLS-capable entrypoint, the error names the config file that was read.

`install` is local-only and works on the Gordon host. It fails when a remote is targeted.

---

## gordon ca export

Export the root CA certificate for manual trust installation on clients.

```bash
gordon ca export                  # Print PEM to stdout
gordon ca export --out ca.pem     # Write to file
gordon ca export --remote prod    # Fetch the CA from a remote Gordon
```

### Options

| Option | Description |
|--------|-------------|
| `--out` | Write certificate to file instead of stdout |

---

## gordon ca info

Show CA status information: root common name, SHA-256 fingerprint, and intermediate certificate expiry.

```bash
gordon ca info
gordon ca info --json
gordon ca info --remote prod
```

### Options

| Option | Description |
|--------|-------------|
| `--json` | Output as JSON |

### JSON Output

```json
{
  "root_cn": "Gordon Internal Root CA",
  "fingerprint": "SHA256:AB:CD:...",
  "intermediate_expiry": "2026-07-01T12:00:00Z",
  "intermediate_ttl": "2160h0m0s"
}
```

---

## gordon ca install

Install or uninstall the root CA certificate in the system, Firefox, and Java trust stores of the **Gordon host machine**. Requires root privileges. The CA is read from the local Gordon data directory; the command never fetches it over the network.

```bash
sudo gordon ca install
sudo gordon ca install --uninstall
```

If a remote is targeted (`--remote`, `GORDON_REMOTE`, or an active remote), the command fails with:

```
ca install only works on the Gordon host; from another machine run `gordon ca export --remote <name> --out gordon-ca.pem` and install that file with your OS trust tools
```

### Options

| Option | Description |
|--------|-------------|
| `--uninstall` | Remove the CA from trust stores instead of installing |
| `--json` | Output as JSON |

> **Important:** This command is for the machine running Gordon, not for arbitrary clients. Other machines should export the certificate (`gordon ca export --remote <name> --out gordon-ca.pem`) or use one of the methods below.

---

## Client Trust Setup

Clients that connect directly to Gordon's HTTPS port need the root CA certificate in their trust store. Gordon provides the certificate through a browser-accessible onboarding page at `https://<gordon-host>/.well-known/gordon/ca`, or over plain HTTP at `http://<gordon-host>/.well-known/gordon/ca` for first-time setup.

### Firefox / Zen (NSS certificate store)

Firefox and Zen use their own certificate store, separate from the OS.

1. Open `https://<gordon-host>/.well-known/gordon/ca.crt` — Firefox will prompt to import the certificate.
2. Check **Trust this CA to identify websites** and confirm.

Alternatively, import manually: **Settings > Privacy & Security > Certificates > View Certificates > Import**.

### Linux System Trust Store

```bash
# Download the certificate
curl -k https://<gordon-host>/.well-known/gordon/ca.crt -o gordon-ca.pem

# Debian / Ubuntu
sudo cp gordon-ca.pem /usr/local/share/ca-certificates/gordon-ca.crt
sudo update-ca-certificates

# Fedora / RHEL
sudo cp gordon-ca.pem /etc/pki/ca-trust/source/anchors/gordon-ca.pem
sudo update-ca-trust
```

### Mobile Devices (iOS / Android)

Visit `http://<gordon-host>/.well-known/gordon/ca` from the device browser. The onboarding page offers:

- **iOS:** Download the `.mobileconfig` profile, then install it in **Settings > General > VPN & Device Management**.
- **Android:** Download `ca.crt`, then install it in **Settings > Security > Encryption & credentials > Install a certificate**.

---

## Related

- [Server Configuration](../config/server.md) — `force_https_redirect`, smart TCP edge, and internal CA settings
- [CLI Commands](./index.md)
- [Remotes](./remotes.md) — saved remote Gordon instances for `--remote`
