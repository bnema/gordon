# Using Pass for Secrets

Set up the Unix password manager (`pass`) as Gordon's secrets backend for secure credential storage.

## What You'll Learn

- Installing and initializing pass
- Storing Gordon secrets securely
- Configuring Gordon to use pass
- Team workflows with shared GPG keys
- Troubleshooting common issues

## Prerequisites

- Linux or macOS system
- GPG installed
- Basic understanding of GPG keys

## Why Pass?

Pass is a simple, Unix-philosophy password manager that:

- Stores secrets as GPG-encrypted files
- Integrates with Git for version control
- Works with standard Unix tools
- Supports multiple GPG keys for team access

## Installation

### Ubuntu/Debian

```bash
sudo apt install pass gnupg
```

### macOS

```bash
brew install pass gnupg
```

### Arch Linux

```bash
sudo pacman -S pass gnupg
```

## Initial Setup

### 1. Generate a GPG Key (if needed)

```bash
# Generate a new GPG key
gpg --gen-key

# Follow prompts:
# - Real name: Gordon Server
# - Email: gordon@yourdomain.com
# - Passphrase: (use a strong passphrase or leave empty for automation)
```

List your keys to find the key ID:

```bash
gpg --list-keys
```

Output:

```
pub   ed25519 2024-01-15 [SC]
      ABC123DEF456...  <-- This is your key ID
uid           [ultimate] Gordon Server <gordon@yourdomain.com>
```

### 2. Initialize Pass Store

```bash
pass init ABC123DEF456...  # Use your GPG key ID
```

This creates `~/.password-store/`.

### 3. Create Gordon Secrets

```bash
# Registry password hash (bcrypt)
# Generate with: htpasswd -nbB admin yourpassword | cut -d: -f2
pass insert gordon/registry/password_hash

# Token secret (random 32+ chars for JWT signing)
pass insert gordon/registry/token_secret
# Or generate automatically:
openssl rand -base64 32 | pass insert -m gordon/registry/token_secret
```

## Gordon Configuration

### Enable Pass Backend

```toml
[secrets]
backend = "pass"

[registry_auth]
enabled = true
type = "token"
username = "admin"
password_hash = "gordon/registry/password_hash"
token_secret = "gordon/registry/token_secret"
```

### Using Secrets in Environment Files

Reference pass secrets in your app's environment files:

```bash
# ~/.gordon/env/app_mydomain_com.env
DATABASE_URL=postgresql://user:${pass:myapp/db/password}@postgres:5432/app
API_KEY=${pass:myapp/api/key}
JWT_SECRET=${pass:myapp/jwt/secret}
```

## Organizing Secrets

Recommended directory structure in pass:

```
~/.password-store/
├── gordon/
│   └── registry/
│       ├── password_hash.gpg
│       └── token_secret.gpg
├── myapp/
│   ├── db/
│   │   └── password.gpg
│   ├── api/
│   │   └── key.gpg
│   └── jwt/
│       └── secret.gpg
└── production/
    └── stripe/
        └── secret_key.gpg
```

## Team Workflows

### Multiple GPG Keys

Allow multiple team members to decrypt secrets:

```bash
# Initialize with multiple keys
pass init KEY_ID_1 KEY_ID_2 KEY_ID_3

# Or add keys later
pass init --path=gordon KEY_ID_1 KEY_ID_2
```

### Git Integration

Version control your encrypted secrets:

```bash
# Initialize git in pass store
pass git init

# Add remote
pass git remote add origin git@github.com:yourorg/secrets.git

# Commit and push
pass git push -u origin main
```

### Pull Secrets on New Server

```bash
# Clone the pass store
git clone git@github.com:yourorg/secrets.git ~/.password-store

# Import team GPG keys
gpg --import team-member.pub
```

## Automation (Passphrase-less Keys)

For servers running Gordon automatically, use a GPG key without a passphrase:

```bash
# Generate key without passphrase
gpg --batch --gen-key <<EOF
Key-Type: EdDSA
Key-Curve: ed25519
Name-Real: Gordon Automation
Name-Email: gordon@server.local
Expire-Date: 0
%no-protection
%commit
EOF
```

> **Security Note**: Protect access to the server itself when using passphrase-less keys.

## Running in Containers

See [Running Gordon in a Container](/wiki/guides/running-in-container.md#using-pass-secrets-backend) for container-specific setup.

Quick summary:

```bash
docker run -d \
  --name gordon \
  -v $HOME/.gnupg:/home/gordon/.gnupg:ro \
  -v $HOME/.password-store:/home/gordon/.password-store:ro \
  gordon-with-pass
```

## Troubleshooting

### "gpg: decryption failed: No secret key"

Your GPG private key isn't available.

```bash
# List available secret keys
gpg --list-secret-keys

# If empty, import your key
gpg --import your-private-key.asc
```

### "pass: command not found"

Pass isn't installed.

```bash
# Ubuntu/Debian
sudo apt install pass

# macOS
brew install pass
```

### "Error: password store is empty"

Initialize pass first:

```bash
pass init YOUR_GPG_KEY_ID
```

### "gpg: public key decryption failed: Inappropriate ioctl for device"

GPG needs a TTY for passphrase entry:

```bash
export GPG_TTY=$(tty)
```

Add to `~/.bashrc` for persistence.

### "gpg-agent: no running gpg-agent"

Start the GPG agent:

```bash
gpg-agent --daemon
```

### Permissions Issues

Ensure correct permissions:

```bash
chmod 700 ~/.gnupg
chmod 600 ~/.gnupg/*
chmod 700 ~/.password-store
```

## Security Best Practices

1. **Protect GPG keys**: Back up securely, use strong passphrases for interactive use
2. **Limit access**: Use separate GPG keys per environment (dev, staging, prod)
3. **Rotate secrets**: Periodically update passwords and tokens
4. **Audit access**: Review who has GPG keys for each environment
5. **Use Git**: Track changes to secrets with pass git integration

## Related

- [Secrets Configuration](/docs/config/secrets.md)
- [Registry Authentication](/docs/config/registry-auth.md)
- [Running in Containers](/wiki/guides/running-in-container.md)
- [Using SOPS for Secrets](./secrets-sops.md)
