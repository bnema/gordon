# Gordon V3 Integration Tests

Comprehensive integration test suite for Gordon's 4-container architecture using Testcontainers-Go v0.40.0.

## Prerequisites

- **Docker in rootless mode** (socket at `/run/user/1000/docker.sock`)
- **Go 1.25+**
- **Pre-built test app image**: `ghcr.io/bnema/go-hello-world-http:latest`

## Quick Start

```bash
# Build the test image and run all integration tests (~8 minutes)
make test-integration

# Run only quick tests (startup + gRPC only, ~3 minutes)
make test-integration-quick

# Build just the test image
make test-integration-build
```

## Manual Execution

```bash
# Build the Gordon Docker image
go build -o dist/gordon ./main.go
docker build -t gordon:v3-test .

# Pull test app image
docker pull ghcr.io/bnema/go-hello-world-http:latest

# Run all tests
go test -v -timeout 10m ./tests/integration/...

# Run specific test
go test -v -timeout 10m -run Test01 ./tests/integration/...
```

## Test Suite

| Test | File | Duration | Description | Status |
|------|------|----------|-------------|--------|
| Test01 | `01_startup_test.go` | ~4s | Four-container startup and health checks | ✅ Implemented |
| Test02 | `02_grpc_test.go` | ~30s | gRPC communication between components | 📝 Planned |
| Test03 | `03_registry_test.go` | ~3min | Image push triggers auto-deploy | 📝 Planned |
| Test04 | `04_restart_test.go` | ~2min | Auto-restart of failed sub-containers | 📝 Planned |
| Test05 | `05_security_test.go` | ~45s | Security isolation verification | 📝 Planned |

**Current Duration**: ~4 seconds (Test01 only)

## Architecture Under Test

```
Internet → gordon-proxy:80 (HTTP only)
              │ gRPC
              ▼
           gordon-core:9090 (Docker socket, orchestrator)
              │ gRPC                    │ gRPC
              ▼                         ▼
           gordon-secrets:9091       gordon-registry:5000 + :9092
           (.gnupg/.password-store)  (Docker registry storage)
```

### Implementation Status

| Component | gRPC Server | gRPC Client | Status |
|-----------|-------------|-------------|--------|
| gordon-core | ✅ CoreService | ❌ Missing (needs clients to secrets & registry) | Partial |
| gordon-proxy | ❌ N/A | ❌ Missing (needs client to core) | Not Started |
| gordon-registry | ✅ RegistryInspectService | ❌ Missing (needs client to core for events) | Partial |
| gordon-secrets | ✅ SecretsService | ❌ N/A | Complete |

**Note**: Current test validates container deployment and individual gRPC server startup. Full inter-service gRPC communication is pending implementation of gRPC clients in core, proxy, and registry components.

## Testcontainers v0.40.0

This test suite uses the latest Testcontainers-Go v0.40.0 API:
- `testcontainers.Run()` instead of `GenericContainer()`
- Functional options pattern
- Simplified network configuration

See: https://github.com/testcontainers/testcontainers-go/releases/tag/v0.40.0

## Configuration

Test configuration is in `fixtures/configs/test-gordon.toml`:
- Disabled auth (simplifies testing)
- Debug logging
- Temporary data directory

## Troubleshooting

### Docker rootless not detected
```bash
# Verify your socket location
systemctl --user status docker.socket
# Should show: Listen: /run/user/1000/docker.sock
```

### Tests timeout
- Tests are sequential and take ~8 minutes
- If timeout occurs, increase: `-timeout 15m`

### Port conflicts
Tests use ports: 80, 5000, 9090, 9091, 9092
Ensure these are free or tests will fail.

### Image not found
```bash
# Build manually
go build -o dist/gordon ./main.go
docker build -t gordon:v3-test .
```

## Notes

- **Local only**: These tests require Docker and are not run in CI
- **Sequential execution**: Tests run one after another for reliability
- **Automatic cleanup**: Containers are terminated after tests
- **Rootless Docker support**: Automatically detects rootless socket

## Security Model Verification

Current test validates:
- ✅ Sub-containers deployed on `gordon-internal` network (isolation from host)
- ✅ Sub-containers have no Docker socket access
- ✅ gordon-core has Docker socket access (for orchestration)

Pending verification:
- 📝 gRPC mTLS between services
- 📝 No cross-container filesystem access
- 📝 Secrets container has GPG mounted, others don't
