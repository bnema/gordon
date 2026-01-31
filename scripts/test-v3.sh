#!/bin/bash
# V3 Architecture Local Testing Script
# This script helps test the 4-container Gordon architecture locally

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_IMAGE="gordon:v3-test"
TEST_NETWORK="gordon-test"
DATA_DIR="${SCRIPT_DIR}/test-data"
ENGINE="${ENGINE:-podman}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Build the test image
build() {
    log_info "Building v3 test image..."
    cd "${SCRIPT_DIR}"
    
    # Build the binary
    go build -o dist/gordon ./main.go
    
    # Build the container image
    cp dist/gordon ./gordon
    $ENGINE build -t $TEST_IMAGE .
    rm ./gordon
    
    log_success "Test image built: $TEST_IMAGE"
}

# Start the test environment
start() {
    log_info "Starting v3 test environment..."
    
    # Create data directories
    mkdir -p "$DATA_DIR"/{registry,logs,env,secrets}
    
    # Create test network
    $ENGINE network create $TEST_NETWORK 2>/dev/null || log_warn "Network already exists"
    
    # Check if image exists
    if ! $ENGINE image exists $TEST_IMAGE; then
        log_warn "Test image not found, building..."
        build
    fi
    
    # Start core (which will deploy other containers)
    log_info "Starting gordon-core (orchestrator)..."
    $ENGINE run -d --name gordon-core-test \
        --network $TEST_NETWORK \
        -p 9090:9090 \
        -p 5000:5000 \
        -v /var/run/docker.sock:/var/run/docker.sock:Z \
        -v "$DATA_DIR:/var/lib/gordon" \
        -e GORDON_COMPONENT=core \
        -e GORDON_IMAGE=$TEST_IMAGE \
        -e GORDON_LOG_LEVEL=debug \
        $TEST_IMAGE serve --component=core 2>/dev/null && log_success "Core started" || log_warn "Core already running or failed to start"
    
    log_info "Waiting for sub-containers to deploy..."
    sleep 5
    
    show_status
    
    echo ""
    log_success "V3 test environment started!"
    echo "   Core gRPC:   localhost:9090"
    echo "   Admin API:   localhost:5000"
    echo "   Registry:    localhost:5000 (in core)"
    echo ""
    echo "Commands:"
    echo "   $0 logs     - View all logs"
    echo "   $0 status   - Check container status"
    echo "   $0 test     - Run integration tests"
    echo "   $0 stop     - Stop all containers"
}

# Stop the test environment
stop() {
    log_info "Stopping v3 test environment..."
    
    $ENGINE stop gordon-core-test gordon-proxy-test gordon-registry-test gordon-secrets-test 2>/dev/null || true
    $ENGINE rm -f gordon-core-test gordon-proxy-test gordon-registry-test gordon-secrets-test 2>/dev/null || true
    $ENGINE network rm $TEST_NETWORK 2>/dev/null || true
    
    log_success "V3 test environment stopped"
}

# Show container status
status() {
    echo "=== V3 Test Container Status ==="
    $ENGINE ps --filter name=gordon-test --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" 2>/dev/null || echo "No containers found"
    echo ""
    
    # Check if core is responding to gRPC
    log_info "Testing gRPC health..."
    if command -v grpcurl &> /dev/null; then
        grpcurl -plaintext localhost:9090 grpc.health.v1.Health/Check 2>/dev/null && log_success "Core gRPC is healthy" || log_error "Core gRPC is not responding"
    else
        log_warn "grpcurl not installed, skipping gRPC health check"
    fi
}

# Show logs
logs() {
    echo "=== Gordon Core Logs (last 30 lines) ==="
    $ENGINE logs --tail=30 gordon-core-test 2>/dev/null || log_error "Core not running"
    echo ""
    
    for container in gordon-proxy-test gordon-registry-test gordon-secrets-test; do
        echo "=== $container Logs (last 20 lines) ==="
        $ENGINE logs --tail=20 $container 2>/dev/null || log_warn "$container not running"
        echo ""
    done
}

# Follow core logs
logs_follow() {
    log_info "Following core logs (Ctrl+C to exit)..."
    $ENGINE logs -f gordon-core-test
}

# Run integration tests
test_integration() {
    log_info "Running integration tests..."
    
    # Test 1: Check if core gRPC is accessible
    log_info "Test 1: Core gRPC accessibility"
    if command -v grpcurl &> /dev/null; then
        grpcurl -plaintext localhost:9090 list 2>/dev/null && log_success "✓ gRPC services accessible" || log_error "✗ gRPC not accessible"
    else
        log_warn "grpcurl not installed, skipping gRPC tests"
    fi
    
    # Test 2: Check if proxy container exists
    log_info "Test 2: Sub-container deployment"
    for container in gordon-proxy-test gordon-registry-test gordon-secrets-test; do
        if $ENGINE ps --filter name=$container --format "{{.Names}}" | grep -q $container; then
            log_success "✓ $container is running"
        else
            log_error "✗ $container is not running"
        fi
    done
    
    # Test 3: Check network connectivity
    log_info "Test 3: Network connectivity"
    if $ENGINE network inspect $TEST_NETWORK &> /dev/null; then
        log_success "✓ Test network exists"
    else
        log_error "✗ Test network missing"
    fi
    
    echo ""
    log_info "Integration tests completed"
}

# Open shell in core container
shell() {
    log_info "Opening shell in gordon-core-test..."
    $ENGINE exec -it gordon-core-test /bin/sh
}

# Clean up everything
clean() {
    log_info "Cleaning up test environment..."
    stop
    rm -rf "$DATA_DIR"
    $ENGINE rmi $TEST_IMAGE 2>/dev/null || true
    log_success "Cleanup complete"
}

# Show help
help() {
    echo "Gordon V3 Local Testing Script"
    echo ""
    echo "Usage: $0 [command]"
    echo ""
    echo "Commands:"
    echo "  start   - Build and start the v3 test environment"
    echo "  stop    - Stop all test containers"
    echo "  status  - Check container status and health"
    echo "  logs    - Show logs from all containers"
    echo "  follow  - Follow core logs in real-time"
    echo "  test    - Run integration tests"
    echo "  shell   - Open shell in core container"
    echo "  clean   - Stop and remove all test artifacts"
    echo "  build   - Build test image only"
    echo "  help    - Show this help"
    echo ""
    echo "Environment Variables:"
    echo "  ENGINE  - Container engine (podman or docker, default: podman)"
    echo ""
    echo "Examples:"
    echo "  $0 start          # Start everything"
    echo "  $0 logs           # Check logs"
    echo "  $0 test           # Run tests"
    echo "  $0 clean          # Clean up"
}

# Main command dispatcher
case "${1:-help}" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    status)
        status
        ;;
    logs)
        logs
        ;;
    follow)
        logs_follow
        ;;
    test)
        test_integration
        ;;
    shell)
        shell
        ;;
    clean)
        clean
        ;;
    build)
        build
        ;;
    help|--help|-h)
        help
        ;;
    *)
        log_error "Unknown command: $1"
        help
        exit 1
        ;;
esac
