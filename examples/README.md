# Gordon v2 Configuration Examples

This directory contains configuration examples for different use cases and environments.

## 📁 Available Examples

### 🚀 [`minimal.toml`](minimal.toml)
**Perfect for getting started**
- Single route configuration
- Default settings
- No authentication
- Ideal for testing and learning

### 🏠 [`development.toml`](development.toml)
**Local development setup**
- Multiple .local domains
- No authentication for ease of use
- Third-party development tools
- localhost registry

### 🧪 [`staging.toml`](staging.toml)
**Staging and preview environments**
- Branch-based deployments
- Feature branch testing
- PR preview environments
- Separate staging registry

### 🏭 [`production.toml`](production.toml)
**Production-ready configuration**
- Pinned image versions
- Registry authentication
- Multiple production services
- Monitoring tools included

### 🏢 [`saas-multi-tenant.toml`](saas-multi-tenant.toml)
**Multi-tenant SaaS platform**
- Customer subdomains
- Custom domains
- Shared application architecture
- Enterprise features

## 🚀 Quick Start

1. **Choose an example** that matches your use case
2. **Copy the config file** to your Gordon directory:
   ```bash
   cp examples/minimal.toml gordon.toml
   ```
3. **Edit the configuration** with your domains and settings
4. **Start Gordon**:
   ```bash
   gordon start
   ```

## 🔧 Customization Tips

### Domain Configuration
Update the `[routes]` section with your actual domains:
```toml
[routes]
"your-domain.com" = "your-app:latest"
"api.your-domain.com" = "your-api:v1.0.0"
```

### Registry Setup
Configure your registry domain and authentication:
```toml
[server]
registry_domain = "registry.your-domain.com"

[registry_auth]
enabled = true
username = "your-username"
password = "your-secure-password"
```

### Environment Variables
For production, use environment variables for sensitive data:
```bash
export GORDON_REGISTRY_PASSWORD="your-secure-password"
export GORDON_SSL_EMAIL="admin@your-domain.com"
```

## 🌐 DNS Configuration

### Cloudflare Setup (Recommended)
For all examples except development, set up these DNS records:

```
Type  Name                    Content
A     app.your-domain.com     YOUR_SERVER_IP
A     api.your-domain.com     YOUR_SERVER_IP  
A     registry.your-domain.com YOUR_SERVER_IP
```

Or use a wildcard:
```
Type  Name                    Content
A     *.your-domain.com       YOUR_SERVER_IP
```

### Local Development
For the development example, add to `/etc/hosts`:
```
127.0.0.1  app.local
127.0.0.1  api.local
127.0.0.1  admin.local
```

## 🔄 Workflow Examples

### Simple Deployment
```bash
# Build your app
docker build -t myapp:latest .

# Tag for registry  
docker tag myapp:latest registry.your-domain.com/myapp:latest

# Push to deploy
docker push registry.your-domain.com/myapp:latest
```

### Version Management
```bash
# Tag with version
docker tag myapp:latest registry.your-domain.com/myapp:v1.2.0

# Update config
# "app.your-domain.com" = "myapp:v1.2.0"

# Push to deploy specific version
docker push registry.your-domain.com/myapp:v1.2.0
```

## 🤝 Need Help?

- 📖 **Read the main [README](../README.md)** for detailed documentation
- 🔍 **Check the logs** for deployment issues
- 🐛 **Open an issue** if you find problems with these examples
- 💡 **Contribute** your own configuration examples!

---
*These examples are starting points - customize them for your specific needs!*