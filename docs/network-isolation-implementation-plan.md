# Gordon Network Isolation Implementation Plan

## Overview
Implement per-app network isolation where each app creates its own Docker network and can have services (databases, caches, etc.) attached to it. Network groups are optional for advanced use cases.

## Configuration Design

### Basic Usage (Most Common)
```toml
[routes]
"myapp.example.com" = "myapp:latest"
"api.example.com" = "api:latest"

[attachments]
# Each app gets its own isolated network with attached services
"myapp.example.com" = ["postgres:15", "redis:7-alpine"]
"api.example.com" = ["mongodb:6", "redis:7-alpine"]  # Each gets own Redis instance
```

### Advanced: Network Groups (Optional)
```toml
[routes]
"app1.example.com" = "app1:latest"
"app2.example.com" = "app2:latest"
"app3.example.com" = "app3:latest"

[network_groups]  # Optional section
"backend" = ["app1.example.com", "app2.example.com"]
"frontend" = ["app3.example.com"]

[attachments]
# Attach to network groups
"backend" = ["redis:7-alpine", "rabbitmq:3"]     # Shared by app1 & app2
"frontend" = ["redis:7-alpine"]                   # Separate Redis for app3
"app1.example.com" = ["postgres:15"]              # app1-specific database
```

## Implementation Details

### 1. Config Schema Changes

**File: `internal/config/config.go`**

```go
type Config struct {
    // ... existing fields ...
    
    // Network isolation
    Attachments    map[string][]string `toml:"attachments"`     // domain/group -> services
    NetworkGroups  map[string][]string `toml:"network_groups"`  // Optional: group -> domains
    NetworkIsolation NetworkIsolationConfig `toml:"network_isolation"`
}

type NetworkIsolationConfig struct {
    Enabled       bool   `toml:"enabled"`        // Default: true
    NetworkPrefix string `toml:"network_prefix"` // Default: "gordon"
    DNSSuffix     string `toml:"dns_suffix"`     // Default: ".internal"
}

// Default values
func defaultConfig() *Config {
    return &Config{
        // ... existing defaults ...
        Attachments:   make(map[string][]string),
        NetworkGroups: make(map[string][]string),  // Empty by default
        NetworkIsolation: NetworkIsolationConfig{
            Enabled:       true,
            NetworkPrefix: "gordon",
            DNSSuffix:     ".internal",
        },
    }
}
```

### 2. Container Manager Network Logic

**File: `internal/container/manager.go`**

```go
// Network name generation
func (m *Manager) GetNetworkForApp(domain string) string {
    // Check if domain is part of a network group
    for groupName, domains := range m.config.NetworkGroups {
        for _, d := range domains {
            if d == domain {
                return m.generateNetworkName(groupName)
            }
        }
    }
    
    // Default: app gets its own network
    return m.generateNetworkName(domain)
}

func (m *Manager) generateNetworkName(identifier string) string {
    // "myapp.example.com" -> "gordon-myapp-example-com"
    // "backend" -> "gordon-backend"
    return fmt.Sprintf("%s-%s", 
        m.config.NetworkIsolation.NetworkPrefix,
        strings.ReplaceAll(identifier, ".", "-"))
}

// Get all apps that should have access to a network
func (m *Manager) GetAppsForNetwork(identifier string) []string {
    // Check if it's a network group
    if apps, ok := m.config.NetworkGroups[identifier]; ok {
        return apps
    }
    
    // Single app network
    return []string{identifier}
}

// Deploy attached service
func (m *Manager) DeployAttachedService(identifier, serviceName, image string) error {
    networkName := m.generateNetworkName(identifier)
    
    // For network groups, we need a different container naming scheme
    apps := m.GetAppsForNetwork(identifier)
    var containerName string
    
    if len(apps) > 1 {
        // Shared service for network group
        containerName = fmt.Sprintf("%s-shared-%s", m.config.NetworkIsolation.NetworkPrefix, serviceName)
    } else {
        // App-specific service
        containerName = fmt.Sprintf("%s-%s", identifier, serviceName)
    }
    
    // Check if already running
    if m.IsContainerRunning(containerName) {
        log.Printf("Service %s already running", containerName)
        return nil
    }
    
    // Load environment (inherit from first app in group or specific app)
    var env map[string]string
    if len(apps) > 0 {
        envFile := m.getEnvFileForDomain(apps[0])
        env = m.loadEnvironment(envFile)
    }
    
    // Deploy to network
    return m.DeployWithNetwork(image, containerName, networkName, env, nil)
}
```

### 3. Deployment Flow

**File: `internal/deploy/deploy.go`**

```go
func (d *Deployer) Deploy(domain, image string) error {
    // Step 1: Determine which network this app should use
    networkName := d.containerManager.GetNetworkForApp(domain)
    
    // Step 2: Create network if it doesn't exist
    err := d.containerManager.CreateNetwork(networkName)
    if err != nil {
        return fmt.Errorf("failed to create network: %w", err)
    }
    
    // Step 3: Deploy main application
    err = d.deployMainApp(domain, image, networkName)
    if err != nil {
        return fmt.Errorf("failed to deploy main app: %w", err)
    }
    
    // Step 4: Deploy attachments for this specific app
    if attachments, ok := d.config.Attachments[domain]; ok {
        for _, serviceImage := range attachments {
            err = d.deployAttachedService(domain, serviceImage)
            if err != nil {
                log.Printf("Failed to deploy attachment %s: %v", serviceImage, err)
            }
        }
    }
    
    // Step 5: Check if app is part of a network group with attachments
    for groupName, domains := range d.config.NetworkGroups {
        for _, d := range domains {
            if d == domain {
                // Deploy group attachments if not already deployed
                if attachments, ok := d.config.Attachments[groupName]; ok {
                    for _, serviceImage := range attachments {
                        err = d.deployAttachedService(groupName, serviceImage)
                        if err != nil {
                            log.Printf("Failed to deploy group attachment %s: %v", serviceImage, err)
                        }
                    }
                }
            }
        }
    }
    
    return nil
}
```

### 4. Volume Management for Attached Services

**Problem**: Standard database images (postgres:15, mysql:8, etc.) don't have VOLUME directives, so Gordon's auto-volume feature won't work for persistent data.

**Solution**: Users must create custom Dockerfiles with VOLUME directives for services that need persistent storage.

**Example Setup for Persistent Database:**

1. **Create a Dockerfile for your database:**
```dockerfile
# Dockerfile.postgres
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]
```

2. **Build and push to your registry:**
```bash
docker build -f Dockerfile.postgres -t registry.yourdomain.com/my-postgres:latest .
docker push registry.yourdomain.com/my-postgres:latest
```

3. **Use in your Gordon config:**
```toml
[routes]
"myapp.example.com" = "myapp:latest"

[attachments]
"myapp.example.com" = ["my-postgres:latest", "redis:7-alpine"]
```

**Why This Approach:**
- **Explicit**: User controls exactly which directories get persistent volumes
- **Simple**: No magic or guessing - follows Gordon's existing volume system
- **Consistent**: Uses the same VOLUME directive pattern for all containers
- **Flexible**: User can add custom configuration, environment variables, or initialization scripts

### 5. Service Discovery & DNS

Services are accessible by their simple names within the network:

```yaml
# From myapp container
DATABASE_URL: postgresql://postgres:5432/mydb
REDIS_URL: redis://redis:6379
MONGO_URL: mongodb://mongodb:27017/myapp

# These hostnames resolve only within the network
```

### 6. Documentation for Users

**In Gordon Documentation:**

```markdown
## Using Attached Services with Persistent Data

For services that need persistent storage (databases, etc.), you must create a custom Dockerfile:

### PostgreSQL Example:
```dockerfile
# Dockerfile.postgres
FROM postgres:15
VOLUME ["/var/lib/postgresql/data"]
ENV POSTGRES_DB=myapp
ENV POSTGRES_USER=appuser
ENV POSTGRES_PASSWORD=secret
```

### MySQL Example:
```dockerfile  
# Dockerfile.mysql
FROM mysql:8
VOLUME ["/var/lib/mysql"]
ENV MYSQL_DATABASE=myapp
ENV MYSQL_USER=appuser
ENV MYSQL_PASSWORD=secret
ENV MYSQL_ROOT_PASSWORD=rootsecret
```

### Redis with Persistence Example:
```dockerfile
# Dockerfile.redis  
FROM redis:7-alpine
VOLUME ["/data"]
CMD ["redis-server", "--appendonly", "yes"]
```

Build and push these to your Gordon registry, then reference them in your config.
```

### 7. Runtime Interface Updates

**File: `pkg/runtime/interface.go`**

```go
type Runtime interface {
    // ... existing methods ...
    
    // Network management
    CreateNetwork(name string, options map[string]string) error
    RemoveNetwork(name string) error
    ListNetworks() ([]NetworkInfo, error)
    ConnectContainerToNetwork(containerName, networkName string) error
    DisconnectContainerFromNetwork(containerName, networkName string) error
}

type CreateOptions struct {
    // ... existing fields ...
    NetworkMode string   // Network to join
    Hostname    string   // Container hostname for DNS
    Aliases     []string // Additional network aliases
}
```

## Examples

### Example 1: Simple App with Database
```toml
[routes]
"blog.company.com" = "wordpress:latest"

[attachments]
"blog.company.com" = ["my-mysql:latest", "my-redis:latest"]

# Prerequisites: User created Dockerfiles with VOLUME directives:
# - my-mysql:latest (FROM mysql:8 + VOLUME ["/var/lib/mysql"])  
# - my-redis:latest (FROM redis:7-alpine + VOLUME ["/data"])
#
# Creates network: gordon-blog-company-com
# Containers: blog.company.com, blog.company.com-my-mysql, blog.company.com-my-redis
# Volumes: Auto-created based on VOLUME directives in the custom images
```

### Example 2: Microservices with Shared Cache
```toml
[routes]
"api.company.com" = "api:latest"
"worker.company.com" = "worker:latest"
"admin.company.com" = "admin:latest"

[network_groups]
"backend" = ["api.company.com", "worker.company.com"]

[attachments]
# Shared services for backend group
"backend" = ["redis:7-alpine", "rabbitmq:3"]
# Individual databases
"api.company.com" = ["postgres:15"]
"admin.company.com" = ["postgres:15"]

# Creates:
# - Network: gordon-backend (contains api, worker, redis, rabbitmq, api's postgres)
# - Network: gordon-admin-company-com (contains admin, admin's postgres)
```

### Example 3: Multi-Tenant SaaS
```toml
[routes]
"tenant1.app.com" = "app:v2.0"
"tenant2.app.com" = "app:v2.0"
"tenant3.app.com" = "app:v2.0"

[attachments]
# Each tenant gets isolated services
"tenant1.app.com" = ["postgres:15", "redis:7"]
"tenant2.app.com" = ["postgres:15", "redis:7"]
"tenant3.app.com" = ["postgres:15", "redis:7"]

# Complete isolation between tenants
```

## Migration Strategy

1. **Backward Compatible**: Apps without attachments use default bridge network
2. **Opt-in**: Only apps with attachments get isolated networks
3. **Gradual Migration**:
   ```toml
   [network_isolation]
   enabled = false  # Start disabled
   ```

## Testing Plan

### Test Cases
1. **Single App Isolation**: App + attached services can communicate
2. **Network Groups**: Multiple apps share services correctly
3. **Mixed Mode**: Some apps grouped, some isolated
4. **Service Discovery**: DNS names resolve correctly
5. **Environment Inheritance**: Attached services get parent's env
6. **Cleanup**: Networks removed when last app removed

### Edge Cases
- App with no attachments (uses default network)
- Empty network group (ignored)
- Conflicting names (app and group with same name)
- Service image not found (graceful failure)
- Network already exists (reuse it)

## Implementation Priority

1. **Phase 1**: Basic attachments (no network groups)
2. **Phase 2**: Add network groups support
3. **Phase 3**: Advanced features (custom DNS, network policies)

## Summary

This design provides:
- **Simple by default**: Just use `[attachments]` section
- **Powerful when needed**: Network groups for complex scenarios
- **Zero breaking changes**: Existing configs work unchanged
- **Gordon philosophy**: Convention over configuration