# Minimal Configuration

The simplest working Gordon configuration.

## When to Use

- Getting started with Gordon
- Local development
- Testing Gordon features

## Configuration

```toml
# ~/.config/gordon/gordon.toml

[server]
port = 8080
registry_port = 5000
registry_domain = "registry.local"

# Authentication disabled for simplicity
[registry_auth]
enabled = false

# Simple route
[routes]
"app.local" = "myapp:latest"
```

## Setup

### 1. Add to /etc/hosts

```bash
echo "127.0.0.1 app.local registry.local" | sudo tee -a /etc/hosts
```

### 2. Start Gordon

```bash
gordon start
```

### 3. Build and Deploy

```bash
docker build -t myapp .
docker tag myapp registry.local:5000/myapp:latest
docker push registry.local:5000/myapp:latest
```

### 4. Access

Open http://app.local:8080

## What's Included

| Feature | Enabled |
|---------|---------|
| Registry | Yes |
| HTTP Proxy | Yes |
| Authentication | No |
| Logging | Console only |
| Network Isolation | No |
| Attachments | No |

## What's Not Included

- HTTPS (requires Cloudflare or similar)
- Registry authentication
- File-based logging
- Network isolation
- Service attachments

## Next Steps

To add more features:

```toml
# Enable logging
[logging.file]
enabled = true
path = "~/.gordon/logs/gordon.log"

# Enable network isolation
[network_isolation]
enabled = true

# Add attachments
[attachments]
"app.local" = ["postgres:latest"]
```

## Related

- [Production Configuration](./production.md)
