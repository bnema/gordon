# Deployment Overview

Strategies for deploying applications with Gordon.

## Deployment Methods

| Method | Use Case |
|--------|----------|
| Manual Push | Development, one-off deploys |
| GitHub Actions | Automated CI/CD |
| GitLab CI | Automated CI/CD |
| Generic CI | Any CI/CD system |

## Basic Deployment Workflow

### 1. Build Locally

```bash
docker build -t myapp .
```

### 2. Tag for Registry

```bash
docker tag myapp registry.mydomain.com/myapp:latest
```

### 3. Push to Deploy

```bash
docker push registry.mydomain.com/myapp:latest
```

Gordon automatically:
1. Receives the image
2. Deploys a new container
3. Routes traffic to it
4. Stops the old container

## Version Strategies

### Latest Tag

Always deploy the most recent build:

```bash
docker tag myapp registry.mydomain.com/myapp:latest
docker push registry.mydomain.com/myapp:latest
```

Configure route:
```toml
[routes]
"app.mydomain.com" = "myapp:latest"
```

### Semantic Versioning

Pin to specific versions:

```bash
docker tag myapp registry.mydomain.com/myapp:v2.1.0
docker push registry.mydomain.com/myapp:v2.1.0
```

Configure route:
```toml
[routes]
"app.mydomain.com" = "myapp:v2.1.0"
```

Update config to deploy new version:
```toml
[routes]
"app.mydomain.com" = "myapp:v2.2.0"
```

### Git SHA Tags

Tag with commit hash for traceability:

```bash
VERSION=$(git rev-parse --short HEAD)
docker tag myapp registry.mydomain.com/myapp:$VERSION
docker push registry.mydomain.com/myapp:$VERSION
```

## Zero-Downtime Deployment

Gordon performs zero-downtime deployments by default:

1. **New container starts** while old is still running
2. **Health check** waits for new container readiness
3. **Traffic switches** to new container
4. **Old container stops** after traffic moves

```
Timeline ─────────────────────────────────────────>

Old Container:  [═══════════════════]
                                    ↓ stop
New Container:           [═════════════════════════>
                         ↑ start    ↑ traffic routed
```

## Environment-Specific Deployments

### Development

```toml
[routes]
"dev.mydomain.com" = "myapp:latest"
```

```bash
docker push registry.mydomain.com/myapp:latest
```

### Staging

```toml
[routes]
"staging.mydomain.com" = "myapp:staging"
```

```bash
docker tag myapp registry.mydomain.com/myapp:staging
docker push registry.mydomain.com/myapp:staging
```

### Production

```toml
[routes]
"app.mydomain.com" = "myapp:v2.1.0"
```

```bash
docker tag myapp registry.mydomain.com/myapp:v2.1.0
docker push registry.mydomain.com/myapp:v2.1.0
# Then update gordon.toml and reload
```

## Deployment Checklist

Before deploying:

- [ ] Build passes locally
- [ ] Tests pass
- [ ] Environment file configured (`~/.gordon/env/app_mydomain_com.env`)
- [ ] DNS pointing to Gordon server
- [ ] Route configured in `gordon.toml`

After deploying:

- [ ] Container starts successfully
- [ ] Application responds to requests
- [ ] Logs show no errors
- [ ] Health checks pass

## Monitoring Deployments

### Watch Logs

```bash
gordon logs -f
```

### Check Container Status

```bash
docker ps | grep gordon
```

### Verify Routing

```bash
curl -I https://app.mydomain.com
```

## Related

- [GitHub Actions](./github-actions.md)
- [Rollback](./rollback.md)
- [Routes Configuration](../config/routes.md)
