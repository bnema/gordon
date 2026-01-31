# Gordon V3 Local Testing Guide

This guide explains how to test the new v3 architecture with 4 isolated containers locally.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    Docker Network                         │
│                  (gordon-test)                            │
├─────────────────────────────────────────────────────────┤
│                                                           │
│  ┌──────────────┐      gRPC      ┌─────────────────┐     │
│  │              │◄──────────────►│                 │     │
│  │ gordon-core  │ :9090          │  gordon-secrets │     │
│  │ (orchestrator│                │     :9091       │     │
│  │              │◄──────────────►│                 │     │
│  │   :9090      │      gRPC      └─────────────────┘     │
│  │   :5000      │                                         │
│  │              │◄──────────────►┌─────────────────┐     │
│  │  (Docker     │      gRPC      │                 │     │
│  │   socket)    │                │ gordon-registry │     │
│  │              │                │  :5000 :9092    │     │
│  └──────────────┘                │                 │     │
│        ▲                         └─────────────────┘     │
│        │                                                   │
│        │    gRPC              ┌─────────────────┐         │
│        └─────────────────────►│                 │         │
│                               │  gordon-proxy   │         │
│                               │      :80        │         │
│                               │                 │         │
│                               └─────────────────┘         │
│                                    │                       │
│                                    ▼                       │
│                              Internet (:80)               │
└─────────────────────────────────────────────────────────┘
```

## Quick Start

### Using Make

```bash
# Build test image and start all containers
make test-v3-up

# Check status
make test-v3-status

# View logs
make test-v3-logs

# Stop everything
make test-v3-down

# Clean up all artifacts
make test-v3-clean
```

### Using Script

```bash
# Start everything
./scripts/test-v3.sh start

# Check status
./scripts/test-v3.sh status

# Run integration tests
./scripts/test-v3.sh test

# View logs
./scripts/test-v3.sh logs

# Follow core logs in real-time
./scripts/test-v3.sh follow

# Open shell in core for debugging
./scripts/test-v3.sh shell

# Clean up
./scripts/test-v3.sh clean
```

## Manual Testing Steps

### 1. Start the Environment

```bash
make test-v3-up
```

This will:
- Build the `gordon:v3-test` image
- Create a test network `gordon-test`
- Start `gordon-core` on ports 9090 (gRPC) and 5000 (Admin API)
- Core will automatically deploy the other 3 containers via gRPC

### 2. Verify Containers Are Running

```bash
make test-v3-status
# or
./scripts/test-v3.sh status
```

Expected output:
```
NAMES                STATUS              PORTS
gordon-core-test     Up 30 seconds       0.0.0.0:9090->9090, 0.0.0.0:5000->5000
gordon-proxy-test    Up 25 seconds       0.0.0.0:80->80
gordon-registry-test Up 25 seconds       0.0.0.0:5000->5000
gordon-secrets-test  Up 25 seconds       
```

### 3. Test gRPC Communication

Install grpcurl:
```bash
# On Fedora/RHEL
sudo dnf install grpcurl

# On Ubuntu/Debian
sudo apt install grpcurl

# On macOS
brew install grpcurl
```

Test core health:
```bash
grpcurl -plaintext localhost:9090 grpc.health.v1.Health/Check
```

List available services:
```bash
grpcurl -plaintext localhost:9090 list
```

Test GetTarget (proxy → core):
```bash
grpcurl -plaintext -d '{"domain": "test.example.com"}' localhost:9090 gordon.v1.CoreService/GetTarget
```

### 4. Test Registry Push/Deploy Flow

Push an image to trigger deployment:
```bash
# Tag and push to local registry
docker tag myapp:latest localhost:5000/myapp:latest
docker push localhost:5000/myapp:latest

# Check core logs for deploy event
make test-v3-logs-follow
```

### 5. Test Proxy Routing

Add a route via admin API:
```bash
curl -X POST http://localhost:5000/admin/routes \
  -H "Content-Type: application/json" \
  -d '{
    "domain": "test.local",
    "target": {
      "container": "myapp",
      "port": 8080
    }
  }'
```

Test proxy routing:
```bash
# Add to /etc/hosts: 127.0.0.1 test.local
curl http://test.local/
```

### 6. Test Sub-Container Health

Check individual containers:
```bash
# Core logs
podman logs gordon-core-test

# Proxy logs
podman logs gordon-proxy-test

# Registry logs
podman logs gordon-registry-test

# Secrets logs
podman logs gordon-secrets-test
```

### 7. Test Auto-Restart

Kill a sub-container and verify it's restarted:
```bash
# Kill proxy
podman kill gordon-proxy-test

# Wait 15-20 seconds for health check
sleep 20

# Verify it was restarted
make test-v3-status
```

## Debugging

### Open Shell in Core Container

```bash
make test-v3-shell
# or
./scripts/test-v3.sh shell
```

### Check gRPC from Inside Container

```bash
# Inside the core container
apk add grpcurl  # or apt-get install grpcurl
grpcurl -plaintext gordon-secrets:9091 grpc.health.v1.Health/Check
```

### View Docker Events

```bash
# In another terminal, watch container events
podman events --filter container=gordon-core-test
```

### Check Network Connectivity

```bash
# Inspect the test network
podman network inspect gordon-test

# Test connectivity between containers
podman exec gordon-core-test ping -c 3 gordon-secrets
podman exec gordon-core-test ping -c 3 gordon-registry
podman exec gordon-core-test ping -c 3 gordon-proxy
```

## Common Issues

### Issue: Containers fail to start

**Solution**: Check Docker socket permissions
```bash
ls -la /var/run/docker.sock
# If using Podman on Linux, you may need:
sudo chmod 666 /var/run/docker.sock
```

### Issue: gRPC connection refused

**Solution**: Wait a bit longer for services to initialize
```bash
sleep 5
./scripts/test-v3.sh status
```

### Issue: Port conflicts

**Solution**: Change ports in the Makefile or stop conflicting services
```bash
# Check what's using port 80
sudo lsof -i :80
sudo lsof -i :5000
sudo lsof -i :9090
```

### Issue: Permission denied on data directory

**Solution**: Fix SELinux labels (if on RHEL/Fedora)
```bash
# Add :Z flag to volume mounts in the run command
-v $(TEST_DATA_DIR):/var/lib/gordon:Z
```

## Environment Variables

Set these to customize the test environment:

```bash
# Use Docker instead of Podman
export ENGINE=docker

# Custom data directory
export DATA_DIR=/tmp/gordon-test

# Start with custom script
./scripts/test-v3.sh start
```

## CI/CD Testing

For automated testing in CI:

```yaml
# .github/workflows/v3-test.yml
name: V3 Integration Test
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      
      - name: Start test environment
        run: |
          export ENGINE=docker
          ./scripts/test-v3.sh start
      
      - name: Wait for services
        run: sleep 10
      
      - name: Run integration tests
        run: ./scripts/test-v3.sh test
      
      - name: Show logs on failure
        if: failure()
        run: ./scripts/test-v3.sh logs
      
      - name: Cleanup
        run: ./scripts/test-v3.sh clean
```

## Next Steps

After verifying v3 works locally:

1. **Deploy to staging** with real domain names
2. **Test production workloads** with the new architecture
3. **Monitor stability** for a few days
4. **Remove monolith fallback** (Phase 10)

## Architecture Verification Checklist

- [ ] Core starts successfully
- [ ] All 3 sub-containers are deployed by core
- [ ] gRPC communication works between all components
- [ ] Proxy can resolve targets via core gRPC
- [ ] Registry push triggers deploy via gRPC events
- [ ] Secrets service is isolated (no volume access from proxy)
- [ ] Sub-containers auto-restart on failure
- [ ] Graceful shutdown works for all containers
