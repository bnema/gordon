# Rollback

Roll back to previous versions when deployments fail.

## Rollback Strategies

### 1. Config-Based Rollback

Update the route to point to the previous version:

```toml
# Current (broken)
[routes]
"app.mydomain.com" = "myapp:v2.1.0"

# Rollback to previous
[routes]
"app.mydomain.com" = "myapp:v2.0.0"
```

Then reload:

```bash
gordon reload
```

### 2. Push Previous Version

Re-push the previous image with the `latest` tag:

```bash
docker pull registry.mydomain.com/myapp:v2.0.0
docker tag registry.mydomain.com/myapp:v2.0.0 registry.mydomain.com/myapp:latest
docker push registry.mydomain.com/myapp:latest
```

### 3. Manifest-Based Rollback

Use OCI manifest annotations for version control:

```bash
# Rollback to v2.0.0
export VERSION=v2.0.0
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.mydomain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.mydomain.com/myapp:$VERSION
podman manifest push myapp:latest registry.mydomain.com/myapp:latest
```

## Version Management

### Keep Previous Versions

Always push versioned tags alongside `latest`:

```bash
VERSION=$(git describe --tags)
docker tag myapp registry.mydomain.com/myapp:$VERSION
docker tag myapp registry.mydomain.com/myapp:latest
docker push registry.mydomain.com/myapp:$VERSION
docker push registry.mydomain.com/myapp:latest
```

### Semantic Versioning

Use semantic versions for clear rollback targets:

```
v2.1.0  ← Current (broken)
v2.0.0  ← Rollback target
v1.9.0  ← Older stable
```

### Git SHA Tags

Tag with commit SHA for precise rollbacks:

```bash
# Deploy
SHA=$(git rev-parse --short HEAD)
docker push registry.mydomain.com/myapp:$SHA

# Rollback to specific commit
docker pull registry.mydomain.com/myapp:abc1234
docker tag registry.mydomain.com/myapp:abc1234 registry.mydomain.com/myapp:latest
docker push registry.mydomain.com/myapp:latest
```

## Rollback Workflow

### Quick Rollback

```bash
# 1. Identify last working version
docker image ls registry.mydomain.com/myapp

# 2. Tag as latest
docker tag registry.mydomain.com/myapp:v2.0.0 registry.mydomain.com/myapp:latest

# 3. Push
docker push registry.mydomain.com/myapp:latest
```

### Config Rollback

```bash
# 1. Edit config
vim ~/.config/gordon/gordon.toml

# 2. Change version
# "app.mydomain.com" = "myapp:v2.0.0"

# 3. Reload
gordon reload
```

## Automated Rollback

### GitHub Actions

Add rollback capability to your workflow:

```yaml
name: Rollback

on:
  workflow_dispatch:
    inputs:
      version:
        description: 'Version to rollback to (e.g., v2.0.0)'
        required: true

jobs:
  rollback:
    runs-on: ubuntu-latest
    steps:
      - name: Login to Registry
        run: |
          echo "${{ secrets.GORDON_TOKEN }}" | \
          docker login -u ${{ secrets.GORDON_USERNAME }} --password-stdin ${{ secrets.GORDON_REGISTRY }}

      - name: Rollback
        run: |
          docker pull ${{ secrets.GORDON_REGISTRY }}/myapp:${{ github.event.inputs.version }}
          docker tag ${{ secrets.GORDON_REGISTRY }}/myapp:${{ github.event.inputs.version }} \
                     ${{ secrets.GORDON_REGISTRY }}/myapp:latest
          docker push ${{ secrets.GORDON_REGISTRY }}/myapp:latest

      - name: Summary
        run: |
          echo "## Rollback Complete" >> $GITHUB_STEP_SUMMARY
          echo "Rolled back to version: ${{ github.event.inputs.version }}" >> $GITHUB_STEP_SUMMARY
```

### Rollback Script

Create a local rollback script:

```bash
#!/bin/bash
# rollback.sh

REGISTRY="registry.mydomain.com"
IMAGE="myapp"
VERSION=$1

if [ -z "$VERSION" ]; then
  echo "Usage: ./rollback.sh <version>"
  echo "Available versions:"
  docker image ls "$REGISTRY/$IMAGE" --format "{{.Tag}}"
  exit 1
fi

echo "Rolling back to $IMAGE:$VERSION..."
docker pull "$REGISTRY/$IMAGE:$VERSION"
docker tag "$REGISTRY/$IMAGE:$VERSION" "$REGISTRY/$IMAGE:latest"
docker push "$REGISTRY/$IMAGE:latest"
echo "Rollback complete!"
```

## Verifying Rollback

After rollback:

```bash
# Check container is running correct version
docker inspect gordon-app-mydomain-com | grep Image

# Check application responds
curl -I https://app.mydomain.com

# Check logs
gordon logs -f
```

## Best Practices

1. **Always tag versions** - Don't rely solely on `latest`
2. **Keep N previous versions** - Maintain rollback options
3. **Test before deploy** - Reduce need for rollbacks
4. **Document known-good versions** - Track stable releases
5. **Automate rollback** - Reduce time to recovery

## Related

- [Deployment Overview](./index.md)
- [GitHub Actions](./github-actions.md)
- [Routes Configuration](../config/routes.md)
