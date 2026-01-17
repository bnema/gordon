# Using SOPS for Secrets

Set up Mozilla SOPS as Gordon's secrets backend for encrypted file-based secret storage.

## What You'll Learn

- Installing SOPS and age (or GPG)
- Creating encrypted secrets files
- Configuring Gordon to use SOPS
- Using secrets in environment files
- Team workflows and key rotation

## Prerequisites

- Linux or macOS system
- Basic understanding of encryption concepts

## Why SOPS?

SOPS (Secrets OPerationS) encrypts specific values in YAML/JSON files while keeping keys readable:

- **Git-friendly**: Encrypted files can be version controlled
- **Multiple backends**: age, GPG, AWS KMS, GCP KMS, Azure Key Vault
- **Selective encryption**: Only values are encrypted, keys remain visible
- **Team support**: Multiple recipients can decrypt the same file

## Installation

### SOPS

```bash
# Ubuntu/Debian
curl -LO https://github.com/getsops/sops/releases/download/v3.9.0/sops-v3.9.0.linux.amd64
sudo mv sops-v3.9.0.linux.amd64 /usr/local/bin/sops
sudo chmod +x /usr/local/bin/sops

# macOS
brew install sops

# Arch Linux
sudo pacman -S sops
```

### age (Recommended)

age is simpler than GPG and recommended for new setups:

```bash
# Ubuntu/Debian
sudo apt install age

# macOS
brew install age

# Arch Linux
sudo pacman -S age
```

### GPG (Alternative)

```bash
# Ubuntu/Debian
sudo apt install gnupg

# macOS
brew install gnupg
```

## Setup with age

### 1. Generate an age Key

```bash
# Generate key pair
age-keygen -o ~/.config/sops/age/keys.txt

# Output shows your public key:
# Public key: age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Note your public key - you'll need it for encryption.

### 2. Configure SOPS

Create `.sops.yaml` in your project or home directory:

```yaml
creation_rules:
  - age: age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Or for multiple recipients:

```yaml
creation_rules:
  - age: >-
      age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx,
      age1yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy
```

### 3. Set age Key Location

```bash
# Add to ~/.bashrc or ~/.zshrc
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
```

## Setup with GPG

### 1. Generate or Use Existing GPG Key

```bash
# Generate new key
gpg --gen-key

# List keys to get fingerprint
gpg --list-keys
```

### 2. Configure SOPS

```yaml
creation_rules:
  - pgp: ABCD1234EFGH5678...  # Your GPG fingerprint
```

## Creating Encrypted Secrets

### Create secrets.yaml

```bash
# Create and encrypt in one step
sops edit secrets.yaml
```

This opens your editor. Add secrets in YAML format:

```yaml
auth:
  password_hash: "$2y$10$..."
  token_secret: "your-random-32-char-string-here"
database:
  password: "db-password-here"
api:
  key: "api-key-here"
```

Save and exit. SOPS encrypts the values automatically.

### View Encrypted File

```bash
cat secrets.yaml
```

Output shows encrypted values:

```yaml
auth:
    password_hash: ENC[AES256_GCM,data:...,iv:...,tag:...]
    token_secret: ENC[AES256_GCM,data:...,iv:...,tag:...]
database:
    password: ENC[AES256_GCM,data:...,iv:...,tag:...]
api:
    key: ENC[AES256_GCM,data:...,iv:...,tag:...]
sops:
    age:
        - recipient: age1xxx...
          enc: |
            -----BEGIN AGE ENCRYPTED FILE-----
            ...
            -----END AGE ENCRYPTED FILE-----
    lastmodified: "2024-01-15T12:00:00Z"
    version: 3.9.0
```

### Decrypt and View

```bash
# View entire file
sops decrypt secrets.yaml

# Extract specific value
sops decrypt --extract '["registry"]["token_secret"]' secrets.yaml
```

### Edit Existing File

```bash
sops edit secrets.yaml
```

## Gordon Configuration

### Enable SOPS Backend

```toml
[auth]
enabled = true
type = "token"
secrets_backend = "sops"
username = "admin"
password_hash = "secrets.yaml:auth.password_hash"
token_secret = "secrets.yaml:auth.token_secret"
```

The path format is `file:key.path` where:
- `file` is the SOPS-encrypted file path
- `key.path` is dot-notation to the value

### Using Secrets in Environment Files

Reference SOPS secrets in your app's environment files:

```bash
# ~/.gordon/env/app_mydomain_com.env
DATABASE_URL=postgresql://user:${sops:secrets.yaml:database.password}@postgres:5432/app
API_KEY=${sops:secrets.yaml:api.key}
```

## File Organization

Recommended structure:

```
project/
├── .sops.yaml              # SOPS configuration
├── secrets.yaml            # Encrypted secrets (commit this)
├── secrets.dev.yaml        # Dev environment secrets
├── secrets.prod.yaml       # Production secrets
└── gordon.toml             # Gordon config (references secrets)
```

### Environment-Specific Rules

```yaml
# .sops.yaml
creation_rules:
  # Dev secrets - developer keys
  - path_regex: \.dev\.yaml$
    age: age1devkey...

  # Prod secrets - production keys only
  - path_regex: \.prod\.yaml$
    age: age1prodkey...

  # Default
  - age: age1defaultkey...
```

## Running in Containers

### Custom Dockerfile

```dockerfile
FROM ghcr.io/bnema/gordon:latest

USER root
RUN apk add --no-cache sops age
USER gordon
```

### Mount Secrets and Keys

```bash
docker run -d \
  --name gordon \
  -p 80:8080 \
  -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v gordon-data:/data \
  -v $(pwd)/secrets.yaml:/app/secrets.yaml:ro \
  -v $(pwd)/.sops.yaml:/app/.sops.yaml:ro \
  -v ~/.config/sops/age/keys.txt:/home/gordon/.config/sops/age/keys.txt:ro \
  -e SOPS_AGE_KEY_FILE=/home/gordon/.config/sops/age/keys.txt \
  -v $(pwd)/gordon.toml:/etc/gordon/gordon.toml:ro \
  gordon-with-sops
```

### Docker Compose

```yaml
services:
  gordon:
    build:
      context: .
      dockerfile: Dockerfile.gordon
    container_name: gordon
    restart: unless-stopped
    ports:
      - "80:8080"
      - "5000:5000"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - gordon-data:/data
      - ./gordon.toml:/etc/gordon/gordon.toml:ro
      - ./secrets.yaml:/app/secrets.yaml:ro
      - ./.sops.yaml:/app/.sops.yaml:ro
      - ~/.config/sops/age/keys.txt:/home/gordon/.config/sops/age/keys.txt:ro
    environment:
      - SOPS_AGE_KEY_FILE=/home/gordon/.config/sops/age/keys.txt
      - GORDON_SECRETS_BACKEND=sops

volumes:
  gordon-data:
```

## Team Workflows

### Adding Team Members

1. Get their age public key
2. Add to `.sops.yaml`:

```yaml
creation_rules:
  - age: >-
      age1yourkey...,
      age1teammemberkey...
```

3. Re-encrypt existing files:

```bash
sops updatekeys secrets.yaml
```

### Key Rotation

1. Generate new key
2. Update `.sops.yaml`
3. Re-encrypt all files:

```bash
sops updatekeys secrets.yaml
sops updatekeys secrets.prod.yaml
```

### Git Workflow

Encrypted files are safe to commit:

```bash
git add .sops.yaml secrets.yaml
git commit -m "Add encrypted secrets"
git push
```

Team members with valid keys can decrypt after pulling.

## Troubleshooting

### "failed to get the data key"

Your key isn't in the recipients list or key file is missing:

```bash
# Check if age key is set
echo $SOPS_AGE_KEY_FILE
cat $SOPS_AGE_KEY_FILE

# Check recipients in encrypted file
grep -A5 "age:" secrets.yaml
```

### "sops: command not found"

Install SOPS:

```bash
# Check if installed
which sops

# Install if missing (see Installation section)
```

### "could not decrypt data key"

Wrong key or corrupted file:

```bash
# Verify your public key matches a recipient
age-keygen -y ~/.config/sops/age/keys.txt
# Compare output to recipients in .sops.yaml
```

### "no matching creation_rules"

Create or fix `.sops.yaml`:

```bash
# Check if .sops.yaml exists
cat .sops.yaml

# Ensure path_regex matches your file
```

### Permission Denied in Container

Ensure key file is readable:

```bash
docker exec gordon cat /home/gordon/.config/sops/age/keys.txt
```

Check mount permissions:

```bash
ls -la ~/.config/sops/age/keys.txt
# Should be readable by container user
```

## Security Best Practices

1. **Never commit unencrypted secrets**: Always use `sops edit`, never edit decrypted files directly
2. **Protect age/GPG keys**: Store private keys securely, never commit them
3. **Use separate keys per environment**: Production keys should be isolated
4. **Rotate keys regularly**: Update keys and re-encrypt periodically
5. **Audit access**: Track who has decryption keys for each environment
6. **Use .sops.yaml**: Enforce encryption rules via configuration

## Related

- [Secrets Configuration](/docs/config/secrets.md)
- [Authentication](/docs/config/auth.md)
- [Running in Containers](/wiki/guides/running-in-container.md)
- [Using Pass for Secrets](./secrets-pass.md)
