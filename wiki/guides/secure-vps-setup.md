# Secure VPS Setup

A production-ready VPS security configuration with Tailscale-only SSH access and CrowdSec intrusion prevention.

## Overview

This guide configures three security layers:

| Layer | Protection |
|-------|------------|
| SSH | Tailscale only (invisible to internet) |
| Firewall | firewalld with DROP policy |
| IPS | CrowdSec with community blocklists |

## Prerequisites

- Fresh Ubuntu 24.04 VPS
- Tailscale account
- Initial SSH access via public IP (temporary)
- Cloudflare account for HTTPS termination

---

## Step 1: Initial System Hardening

Before anything else, enable automatic security updates and time sync:

```bash
# Automatic security updates
apt update && apt install -y unattended-upgrades
dpkg-reconfigure -plow unattended-upgrades

# Verify time sync (enabled by default on Ubuntu 24.04)
timedatectl set-ntp true
timedatectl status

# Basic kernel hardening
cat > /etc/sysctl.d/99-security.conf << 'EOF'
# SYN flood protection
net.ipv4.tcp_syncookies = 1

# Reverse path filtering (anti-spoofing)
net.ipv4.conf.all.rp_filter = 1
net.ipv4.conf.default.rp_filter = 1

# Disable ICMP redirects
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv6.conf.all.accept_redirects = 0

# Ignore ICMP broadcasts
net.ipv4.icmp_echo_ignore_broadcasts = 1
EOF

sysctl --system
```

## Step 2: Install Tailscale

> **Important**: Use the official repository, NOT the Snap package (has AppArmor issues with Tailscale SSH on Ubuntu 24.04).

```bash
# Add official Tailscale repository
curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg | \
  tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null
curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list | \
  tee /etc/apt/sources.list.d/tailscale.list

# Install
apt update
apt install -y tailscale
```

## Step 3: Connect Tailscale with SSH

```bash
tailscale up --ssh
```

This outputs an authentication URL. Open it and authenticate with your Tailscale account.

**Important**: After connecting, go to https://login.tailscale.com/admin/machines and **disable key expiry** for this server to prevent lockouts.

## Step 4: Configure firewalld

```bash
# Install firewalld
apt install -y firewalld

# Enable and start
systemctl enable --now firewalld

# Trust Tailscale interface (allows all traffic from your tailnet)
firewall-cmd --permanent --zone=trusted --add-interface=tailscale0

# Allow Tailscale UDP port (WireGuard)
firewall-cmd --permanent --add-port=41641/udp

# Allow web traffic (for Gordon reverse proxy)
firewall-cmd --permanent --add-service=http
firewall-cmd --permanent --add-service=https

# Apply changes
firewall-cmd --reload
```

> **Note**: Gordon's registry is proxied through port 80/443 via `gordon_domain`. Port 5000 does NOT need to be exposed publicly.

## Step 5: Verify Tailscale SSH Works

From another machine on your tailnet:

```bash
# Get the server's Tailscale hostname
tailscale status

# Connect from your local machine
ssh root@<tailscale-hostname>
# or
tailscale ssh root@<tailscale-hostname>
```

## Step 6: Block Public SSH

**Only proceed once Tailscale SSH is confirmed working.**

```bash
# Remove SSH from public zone
firewall-cmd --permanent --remove-service=ssh

# Set default policy to DROP all unmatched traffic
firewall-cmd --permanent --zone=public --set-target=DROP

# Apply changes
firewall-cmd --reload
```

Verify public SSH is blocked:

```bash
# From another machine (not on tailnet)
ssh -o ConnectTimeout=5 root@<public-ip>  # Should timeout
```

## Step 6.5: Restrict Registry and Auth to Tailnet CIDR

If you want `/auth/*` and registry (`/v2/*`) reachable only from your tailnet, set Gordon's registry allowlist to the Tailscale CGNAT range.

Edit `~/.config/gordon/gordon.toml`:

```toml
[server]
registry_allowed_ips = ["100.64.0.0/10"]
```

Apply the change:

```bash
systemctl --user restart gordon
```

Behavior:

- Requests from tailnet IPs (`100.64.0.0/10`) are allowed.
- Requests outside that CIDR get `403 Forbidden` on registry/auth endpoints.
- App routes still use normal host-based routing through your proxy ports.

For private operations (auth, push/pull, admin API), keep using the HTTPS registry domain, but resolve that domain to the VPS tailnet IP (for example, via Cloudflare DNS to the tailnet address). With self-signed certs, configure the CLI remote with insecure TLS enabled.

```bash
# Example: save remote once
gordon remotes add tailnet-reg https://gordon.example.com --token-env GORDON_TOKEN --insecure
gordon remotes use tailnet-reg

# Then use commands normally (auth + admin API)
gordon auth login
gordon routes list
gordon backup status
```

`~/.config/gordon/remotes.toml` should contain an entry like:

```toml
active = "tailnet-reg"

[remotes.tailnet-reg]
url = "https://gordon.example.com"
token_env = "GORDON_TOKEN"
insecure_tls = true
```

This keeps transport on HTTPS while allowing self-signed certificates in tailnet-only deployments.

---

## Step 7: Install CrowdSec

```bash
# Add CrowdSec repo (NOT Ubuntu's outdated version)
curl -s https://install.crowdsec.net | bash

# Install CrowdSec
apt update
apt install -y crowdsec
```

## Step 8: Install nftables Bouncer

Ubuntu 24.04 uses nftables by default:

```bash
apt install -y crowdsec-firewall-bouncer-nftables
systemctl enable --now crowdsec-firewall-bouncer
```

## Step 9: Install Security Collections

```bash
# HTTP protection
cscli collections install crowdsecurity/http-cve
cscli collections install crowdsecurity/base-http-scenarios

# Reload to apply
systemctl reload crowdsec
```

## Step 10: Whitelist Tailscale IPs

Prevent CrowdSec from blocking Tailscale traffic:

```bash
cat > /etc/crowdsec/parsers/s02-enrich/tailscale-whitelist.yaml << 'EOF'
name: tailscale-whitelist
description: "Whitelist Tailscale CGNAT range"
whitelist:
  reason: "Tailscale internal traffic"
  cidr:
    - "100.64.0.0/10"
EOF

systemctl reload crowdsec
```

## Step 11: Enroll in CrowdSec Console

This enables community blocklists - IPs blocked across all CrowdSec users globally (15,000+ malicious IPs).

1. Create account at https://app.crowdsec.net
2. Go to **Security Engines** → **Add Security Engine**
3. Copy enrollment key and run:

```bash
cscli console enroll <your-enrollment-key>
```

4. Accept the enrollment in the CrowdSec console
5. Enable console management:

```bash
systemctl restart crowdsec
cscli console enable console_management
systemctl reload crowdsec
```

---

## Verification

```bash
# Firewall status
firewall-cmd --state
firewall-cmd --list-all
firewall-cmd --list-all --zone=trusted

# CrowdSec status
cscli metrics
cscli bouncers list
cscli console status

# Verify nftables has CrowdSec rules
nft list ruleset | grep -A5 'crowdsec'

# View blocked IPs count
cscli decisions list | wc -l
```

---

## Port Summary

| Port | Protocol | Status | Purpose |
|------|----------|--------|---------|
| 22 | TCP | Closed | SSH (use Tailscale instead) |
| 80 | TCP | Open | HTTP (Gordon proxy) |
| 443 | TCP | Open | HTTPS (Gordon proxy via Cloudflare) |
| 5000 | TCP | Closed | Registry (proxied through 80/443, containers bind to 127.0.0.1) |
| 41641 | UDP | Open | Tailscale WireGuard |

> **Security Note:** All managed containers bind to `127.0.0.1` by default, preventing direct access to their ports. The only entry point to your applications is Gordon's reverse proxy on ports 80/443, where authentication and rate limiting are enforced.

---

## Troubleshooting

### Can't SSH via Tailscale

Check for ACL issues:

```bash
journalctl -u tailscaled --since "10 minutes ago" | grep -i "rejected\|acl"
```

If you see `rejected due to acl`, check your Tailscale ACLs at https://login.tailscale.com/admin/acls

### Locked out completely

Use your VPS provider's console access (e.g., Hetzner Cloud Console):

```bash
firewall-cmd --add-service=ssh  # Temporarily re-enable public SSH
```

### CrowdSec not blocking

Verify bouncer is running:

```bash
systemctl status crowdsec-firewall-bouncer
cscli bouncers list  # Should show "Valid"
```

### Check what's being blocked

```bash
cscli decisions list
cscli alerts list
```

---

## Next Steps

- [Install Gordon](/docs/getting-started.md)
- [Podman Rootless Setup](./podman-rootless.md) - Enhanced container isolation
- [Cloudflare DNS & Proxy Setup](#) - HTTPS termination (coming soon)

## References

- [Tailscale SSH Documentation](https://tailscale.com/kb/1193/tailscale-ssh)
- [firewalld Documentation](https://firewalld.org/documentation/)
- [CrowdSec Documentation](https://docs.crowdsec.net/)
- [CrowdSec Console](https://app.crowdsec.net)
