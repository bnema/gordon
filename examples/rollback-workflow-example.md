# Gordon Version Deployment Workflow Example

This document demonstrates how to use manifest annotations for simple version deployments and rollbacks in Gordon.

## Prerequisites

- Gordon server running with the registry and auto-route features enabled
- Podman or Docker with OCI manifest support
- A container image to deploy

## Basic Versioned Deployment

### 1. Build and Push Your Versioned Images

```bash
# Build and push your application images with versions
export VERSION=v1.0.1
podman build --tag myapp:$VERSION --tag registry.domain.com/myapp:$VERSION .
podman push registry.domain.com/myapp:$VERSION

export VERSION=v1.0.2
podman build --tag myapp:$VERSION --tag registry.domain.com/myapp:$VERSION .
podman push registry.domain.com/myapp:$VERSION
```

### 2. Create Manifest and Deploy

```bash
# Deploy v1.0.1 with version annotation
export VERSION=v1.0.1
podman manifest create myapp:latest
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest
```

### 3. Gordon Processing

When Gordon receives the manifest, it will:

1. Parse the manifest annotations from the JSON structure:
   ```json
   {
     "schemaVersion": 2,
     "mediaType": "application/vnd.oci.image.manifest.v1+json",
     "annotations": {
       "version": "v1.0.1"
     }
   }
   ```

2. Extract version information and deploy the specified version
3. Update routes to use the versioned image

## Simple Version Deployment and Rollback

### Deploying Different Versions

```bash
# Deploy v1.0.2
export VERSION=v1.0.2
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest

# Rollback to v1.0.1 (just change the version)  
export VERSION=v1.0.1
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest

# Deploy v1.0.3 (assuming you built and pushed it first)
export VERSION=v1.0.3
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest
```

### Gordon Version Processing

When Gordon detects version annotations, it will:

1. **Parse version annotations**: Extract the version from the `version` annotation
2. **Execute deployment**: Deploy the specified version to all matching routes
3. **Log the deployment**

**Example log output**:
```
INFO Processing versioned deployment image=myapp reference=latest version=v1.0.1
INFO Performing version deployment route=myapp.domain.com versioned_image=myapp:v1.0.1
INFO Version deployment completed successfully route=myapp.domain.com version=v1.0.1
```

## Supported Annotation Keys

### Version Annotations
- `version` - The version to deploy (required)

## Real-World Example

```bash
# Deploy production v1.0.1
export VERSION=v1.0.1
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest

# Deploy new version v1.0.2  
export VERSION=v1.0.2
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest

# Oh no! Bug found - rollback to v1.0.1
export VERSION=v1.0.1
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest

# Fix deployed - move forward to v1.0.3
export VERSION=v1.0.3
podman manifest create myapp:latest --amend
podman manifest add myapp:latest registry.domain.com/myapp:$VERSION
podman manifest annotate myapp:latest --annotation version=$VERSION registry.domain.com/myapp:$VERSION
podman manifest push myapp:latest registry.domain.com/myapp:latest
```

## Configuration Requirements

Ensure your Gordon configuration supports this workflow:

```toml
[auto_route]
enabled = true

[registry_auth]
enabled = true
username = "gordon"
password = "your-password"

[server]
registry_domain = "registry.domain.com"
```

## Monitoring and Observability

Gordon logs provide visibility into the version deployment process:

- Manifest annotation parsing
- Version detection and tracking
- Deployment execution
- Success/failure status

Check Gordon logs for detailed version deployment information:

```bash
# View Gordon logs for version operations
journalctl -u gordon -f | grep -i version

# Or check Docker/Podman logs if running in container
docker logs gordon-container | grep -i version
```

## Troubleshooting

### Common Issues

1. **Annotations not parsed**: Ensure you're using OCI manifest format, not Docker v2.2
2. **Version target not found**: Verify the target version exists in your registry
3. **Route not found**: Ensure the image has configured routes in Gordon
4. **Permission errors**: Check registry authentication and permissions

### Debugging Commands

```bash
# Inspect manifest annotations
podman manifest inspect myapp:latest

# Check Gordon configuration
gordon config show

# Verify image exists
podman search registry.domain.com/myapp
```

This version deployment workflow provides a simple, declarative way to manage deployments and rollbacks using standard container registry operations and OCI manifest annotations.