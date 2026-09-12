# Rollback

Roll forward to a previous version when a deploy misbehaves. There is no historical rollback command in v2.50: rolling back means applying a manifest that references the previous tag (or re-pushing it) and deploying again.

## Roll Forward to a Previous Tag

The image tags are still in the registry. Point the app file at the last good tag, apply, deploy:

```bash
# 1. Edit blog.toml: image = "gordon.mydomain.com/myapp:v2.0.0"
gordon apps apply --file blog.toml --remote prod
gordon apps deploy blog --remote prod
```

For HTTP services without volumes, the previous (broken) container keeps serving until the replacement passes readiness, so the recovery itself is zero-downtime.

## Re-push the Previous Image as latest

When the app file tracks `latest`, re-tag and re-push, then deploy (deploy re-resolves mutable tags):

```bash
docker pull gordon.mydomain.com/myapp:v2.0.0
docker tag gordon.mydomain.com/myapp:v2.0.0 gordon.mydomain.com/myapp:latest
docker push gordon.mydomain.com/myapp:latest
gordon apps deploy blog --remote prod
```

## Version Management

### Keep Previous Versions

Always push versioned tags alongside `latest`:

```bash
VERSION=$(git describe --tags)
docker tag myapp gordon.mydomain.com/myapp:$VERSION
docker tag myapp gordon.mydomain.com/myapp:latest
docker push gordon.mydomain.com/myapp:$VERSION
docker push gordon.mydomain.com/myapp:latest
```

### Semantic Versioning

Use semantic versions for clear recovery targets:

```
v2.1.0  ← Current (broken)
v2.0.0  ← Recovery target
v1.9.0  ← Older stable
```

### Git SHA Tags

Tag with commit SHA for precise recovery:

```bash
# Deploy
SHA=$(git rev-parse --short HEAD)
docker push gordon.mydomain.com/myapp:$SHA
```

## Verifying Recovery

After re-deploying the previous version:

```bash
# Check the app's effective vs observed state
gordon apps status blog --remote prod

# Check application responds
curl -I https://app.mydomain.com

# Check logs
gordon apps logs blog --remote prod
```

## Best Practices

1. **Always tag versions** - Don't rely solely on `latest`
2. **Keep N previous versions** - Maintain recovery options
3. **Test before deploy** - Reduce need for rollbacks
4. **Document known-good versions** - Track stable releases

## Related

- [Deployment Overview](./index.md)
- [GitHub Actions](./github-actions.md)
- [Apps CLI](../cli/apps.md)
