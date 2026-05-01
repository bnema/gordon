# CA Commands

Manage Gordon's internal Certificate Authority.

## gordon ca

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `export` | Export the root CA certificate in PEM format |
| `info` | Show CA status (root CN, fingerprint, intermediate expiry) |
| `install` | Install/uninstall the root CA in system trust stores |

All subcommands require `server.tls_port` to be non-zero in the Gordon configuration.

---

## gordon ca export

Export the root CA certificate for manual trust installation on clients.

```bash
gordon ca export                  # Print PEM to stdout
gordon ca export --out ca.pem     # Write to file
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

Install or uninstall the root CA certificate in system, Firefox, and Java trust stores on the **Gordon host machine**. Requires root privileges.

```bash
sudo gordon ca install
sudo gordon ca install --uninstall
```

### Options

| Option | Description |
|--------|-------------|
| `--uninstall` | Remove the CA from trust stores instead of installing |
| `--json` | Output as JSON |

> **Important:** This command is for the machine running Gordon, not for arbitrary clients. Remote users should import the certificate through one of the methods below.

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

- [Server Configuration](../config/server.md) — `tls_port`, `force_https_redirect`, and internal CA settings
- [CLI Commands](./index.md)
