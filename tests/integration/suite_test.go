// Package integration provides integration tests for Gordon v3.
// These tests verify the real scenario where gordon-core's lifecycle manager
// auto-deploys the other 3 containers (secrets, registry, proxy).
//
// Requirements:
//   - Docker rootless mode (socket at /run/user/1000/docker.sock)
//   - Pre-built gordon:v3-test image (run `make test-integration-build` first)
//   - Test image: ghcr.io/bnema/go-hello-world-http:latest
//
// Running tests:
//
//	go test -v -timeout 10m ./tests/integration/...
//
// Or use Make:
//
//	make test-integration-local
//
// Test duration: ~8 minutes (10 min max)
package integration

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"github.com/bnema/gordon/tests/integration/helpers"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	testImageName          = "gordon:v3-test"
	testAppImage           = "ghcr.io/bnema/go-hello-world-http:latest"
	maxTestDuration        = 10 * time.Minute
	coreStartupTimeout     = 120 * time.Second
	containerDeployTimeout = 90 * time.Second
)

// GordonTestSuite provides a shared test suite for all integration tests.
// Tests the REAL scenario where only gordon-core is started, and it
// auto-deploys the other 3 containers (secrets, registry, proxy).
type GordonTestSuite struct {
	suite.Suite
	ctx context.Context

	// Docker resources
	network      *testcontainers.DockerNetwork
	dockerClient *client.Client

	// Core container (only one we start manually)
	CoreC testcontainers.Container

	// Sub-containers (auto-deployed by core's lifecycle manager)
	SecretsC  *types.Container
	RegistryC *types.Container
	ProxyC    *types.Container

	// gRPC clients (initialized after containers start)
	SecretsClient  gordonv1.SecretsServiceClient
	RegistryClient gordonv1.RegistryInspectServiceClient
	CoreClient     gordonv1.CoreServiceClient

	// Ports (mapped host ports for auto-deployed containers)
	CoreGRPCPort     string
	CoreHTTPPort     string
	RegistryGRPCPort string
	RegistryHTTPPort string
	ProxyHTTPPort    string
	SecretsGRPCPort  string
}

// SetupSuite builds the Gordon image and starts only gordon-core.
// The lifecycle manager in gordon-core will auto-deploy the other containers.
func (s *GordonTestSuite) SetupSuite() {
	s.ctx = context.Background()

	// Initialize Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	s.Require().NoError(err, "failed to create docker client")
	s.dockerClient = cli

	// Build Gordon test image first
	s.Require().NoError(s.buildGordonImage(), "failed to build Gordon test image")

	// Create shared network
	nw, err := network.New(s.ctx,
		network.WithDriver("bridge"),
		network.WithAttachable(),
	)
	s.Require().NoError(err, "failed to create Docker network")
	s.network = nw

	// Start only gordon-core (lifecycle manager will deploy sub-containers)
	s.startCoreOnly()

	// Wait for gordon-core's lifecycle manager to deploy sub-containers
	s.waitForSubContainers()

	// Initialize gRPC clients to auto-deployed containers
	s.initializeGRPCClients()

	// Pull test app image for later use
	s.pullTestAppImage()
}

// TearDownSuite stops all containers and cleans up.
func (s *GordonTestSuite) TearDownSuite() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop manually started core container
	if s.CoreC != nil {
		_ = s.CoreC.Terminate(ctx)
	}

	// Stop auto-deployed containers
	containers := []*types.Container{s.ProxyC, s.RegistryC, s.SecretsC}
	for _, c := range containers {
		if c != nil {
			_ = s.dockerClient.ContainerStop(ctx, c.ID, container.StopOptions{})
			_ = s.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
		}
	}

	// Remove network
	if s.network != nil {
		_ = s.network.Remove(s.ctx)
	}

	// Close Docker client
	if s.dockerClient != nil {
		s.dockerClient.Close()
	}
}

// buildGordonImage builds the Gordon Docker image for testing.
func (s *GordonTestSuite) buildGordonImage() error {
	return helpers.BuildGordonImage(s.T(), s.ctx)
}

// pullTestAppImage pulls the test application image.
func (s *GordonTestSuite) pullTestAppImage() {
	cmd := exec.Command("docker", "pull", testAppImage)
	if output, err := cmd.CombinedOutput(); err != nil {
		s.T().Logf("Warning: failed to pull test app image: %v\n%s", err, output)
	}
}

// startCoreOnly starts only the gordon-core container.
// Core's lifecycle manager will auto-deploy secrets, registry, and proxy containers.
func (s *GordonTestSuite) startCoreOnly() {
	s.T().Log("Starting gordon-core (lifecycle manager will deploy sub-containers)...")

	// Get project root for config
	_, b, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(b), "..", "..")
	configPath := filepath.Join(projectRoot, "tests", "integration", "fixtures", "configs", "test-gordon.toml")

	env := map[string]string{
		"GORDON_COMPONENT": "core",
		"GORDON_LOG_LEVEL": "debug",
		"DOCKER_HOST":      "unix:///var/run/docker.sock",
		"GORDON_ENV":       "test",
	}

	mounts := []testcontainers.ContainerMount{
		testcontainers.BindMount(helpers.GetDockerSocketPath(), "/var/run/docker.sock"),
		testcontainers.BindMount(configPath, "/app/gordon.toml"),
	}

	// Core takes longer to start as it needs to deploy sub-containers
	// Use simple port wait with long timeout - HTTP starts after DeployAll completes
	waitStrategy := wait.ForListeningPort("9090/tcp").WithStartupTimeout(coreStartupTimeout)

	req := testcontainers.ContainerRequest{
		Image:        testImageName,
		Name:         "gordon-core-test",
		ExposedPorts: []string{"5000/tcp", "9090/tcp"},
		Networks:     []string{s.network.Name},
		NetworkAliases: map[string][]string{
			s.network.Name: {"gordon-core"},
		},
		Mounts:     mounts,
		Env:        env,
		Cmd:        []string{"--component=core"},
		WaitingFor: waitStrategy,
	}

	container, err := testcontainers.GenericContainer(s.ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	s.Require().NoError(err, "failed to start gordon-core")

	s.CoreC = container

	// Get mapped ports for core
	httpPort, err := container.MappedPort(s.ctx, "5000/tcp")
	s.Require().NoError(err)
	s.CoreHTTPPort = httpPort.Port()

	grpcPort, err := container.MappedPort(s.ctx, "9090/tcp")
	s.Require().NoError(err)
	s.CoreGRPCPort = grpcPort.Port()

	s.T().Logf("gordon-core running - HTTP on port %s, gRPC on port %s", s.CoreHTTPPort, s.CoreGRPCPort)
	s.T().Logf("Waiting for lifecycle manager to deploy sub-containers (max %v)...", containerDeployTimeout)
}

// waitForSubContainers waits for gordon-core's lifecycle manager to deploy
// the secrets, registry, and proxy containers. Uses Docker API to discover them.
func (s *GordonTestSuite) waitForSubContainers() {
	s.T().Log("Discovering auto-deployed sub-containers...")

	ctx, cancel := context.WithTimeout(s.ctx, containerDeployTimeout)
	defer cancel()

	// Poll until all 3 sub-containers are found
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	requiredComponents := map[string]**types.Container{
		"secrets":  &s.SecretsC,
		"registry": &s.RegistryC,
		"proxy":    &s.ProxyC,
	}

	for {
		// List containers with gordon-component label
		filters := filters.NewArgs()
		filters.Add("label", "gordon-component")

		containers, err := s.dockerClient.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters,
		})
		if err != nil {
			s.T().Logf("Error listing containers: %v", err)
			continue
		}

		found := make(map[string]*types.Container)
		for _, c := range containers {
			if compLabel, ok := c.Labels["gordon-component"]; ok {
				// Skip core itself - we only want sub-containers
				if compLabel != "core" {
					found[compLabel] = &c
					s.T().Logf("Found sub-container: %s (ID: %s, State: %s, Status: %s)",
						compLabel, c.ID[:12], c.State, c.Status)
				}
			}
		}

		// Check if all required components are running
		allReady := true
		for component, ptr := range requiredComponents {
			if c, exists := found[component]; exists {
				if c.State == "running" {
					// Store the container reference
					*ptr = c
				} else {
					allReady = false
					s.T().Logf("Container %s found but not running (state: %s)", component, c.State)
				}
			} else {
				allReady = false
			}
		}

		if allReady {
			s.T().Log("All sub-containers are running!")
			break
		}

		select {
		case <-ctx.Done():
			s.Require().Fail(fmt.Sprintf("timeout waiting for sub-containers: %v", ctx.Err()))
		case <-ticker.C:
			s.T().Logf("Waiting for sub-containers... (found %d containers)", len(found))
		}
	}

	// Get mapped ports for sub-containers
	s.getSubContainerPorts()

	s.T().Log("All sub-containers discovered and ready")
}

// getSubContainerPorts retrieves the mapped host ports for auto-deployed containers.
func (s *GordonTestSuite) getSubContainerPorts() {
	s.T().Log("Getting mapped ports for sub-containers...")

	// Refresh container info to get latest port mappings
	containers := []*types.Container{s.SecretsC, s.RegistryC, s.ProxyC}
	for i, c := range containers {
		if c == nil {
			continue
		}

		// Inspect container to get detailed info including ports
		inspect, err := s.dockerClient.ContainerInspect(s.ctx, c.ID)
		s.Require().NoError(err, "failed to inspect container %d", i)

		component := c.Labels["gordon-component"]
		s.T().Logf("Container %s - Ports: %+v", component, inspect.NetworkSettings.Ports)

		// Extract ports based on component type
		switch component {
		case "secrets":
			if port, ok := inspect.NetworkSettings.Ports["9091/tcp"]; ok && len(port) > 0 {
				s.SecretsGRPCPort = port[0].HostPort
				s.T().Logf("gordon-secrets gRPC on port %s", s.SecretsGRPCPort)
			}
		case "registry":
			if httpPort, ok := inspect.NetworkSettings.Ports["5000/tcp"]; ok && len(httpPort) > 0 {
				s.RegistryHTTPPort = httpPort[0].HostPort
				s.T().Logf("gordon-registry HTTP on port %s", s.RegistryHTTPPort)
			}
			if grpcPort, ok := inspect.NetworkSettings.Ports["9092/tcp"]; ok && len(grpcPort) > 0 {
				s.RegistryGRPCPort = grpcPort[0].HostPort
				s.T().Logf("gordon-registry gRPC on port %s", s.RegistryGRPCPort)
			}
		case "proxy":
			if port, ok := inspect.NetworkSettings.Ports["80/tcp"]; ok && len(port) > 0 {
				s.ProxyHTTPPort = port[0].HostPort
				s.T().Logf("gordon-proxy HTTP on port %s", s.ProxyHTTPPort)
			}
		}
	}

	// Verify all required ports are set
	s.Require().NotEmpty(s.SecretsGRPCPort, "secrets gRPC port not found")
	s.Require().NotEmpty(s.RegistryHTTPPort, "registry HTTP port not found")
	s.Require().NotEmpty(s.RegistryGRPCPort, "registry gRPC port not found")
	s.Require().NotEmpty(s.ProxyHTTPPort, "proxy HTTP port not found")
}

// initializeGRPCClients initializes gRPC clients for all Gordon services.
func (s *GordonTestSuite) initializeGRPCClients() {
	s.T().Log("Initializing gRPC clients...")

	// Initialize secrets client
	secretsAddr := net.JoinHostPort("localhost", s.SecretsGRPCPort)
	secretsConn, err := grpc.NewClient(
		secretsAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err, "failed to connect to secrets service")
	s.SecretsClient = gordonv1.NewSecretsServiceClient(secretsConn)

	// Initialize registry client
	registryAddr := net.JoinHostPort("localhost", s.RegistryGRPCPort)
	registryConn, err := grpc.NewClient(
		registryAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err, "failed to connect to registry service")
	s.RegistryClient = gordonv1.NewRegistryInspectServiceClient(registryConn)

	// Initialize core client
	coreAddr := net.JoinHostPort("localhost", s.CoreGRPCPort)
	coreConn, err := grpc.NewClient(
		coreAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err, "failed to connect to core service")
	s.CoreClient = gordonv1.NewCoreServiceClient(coreConn)

	// Wait for services to be ready with health checks
	s.waitForServices()

	s.T().Log("gRPC clients initialized successfully")
}

// waitForServices waits for all gRPC services to be ready.
func (s *GordonTestSuite) waitForServices() {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	// Check secrets service
	err := s.waitForService(ctx, "secrets", func(ctx context.Context) error {
		_, err := s.SecretsClient.ListTokens(ctx, &gordonv1.ListTokensRequest{})
		return err
	})
	s.Require().NoError(err, "secrets service failed to become ready")

	// Check registry service
	err = s.waitForService(ctx, "registry", func(ctx context.Context) error {
		// Registry inspect service - use ListRepositories to test connectivity
		_, err := s.RegistryClient.ListRepositories(ctx, &gordonv1.ListRepositoriesRequest{})
		// Error is expected if registry is empty, but connection should work
		return err
	})
	s.Require().NoError(err, "registry service failed to become ready")

	// Check core service
	err = s.waitForService(ctx, "core", func(ctx context.Context) error {
		_, err := s.CoreClient.GetRoutes(ctx, &gordonv1.GetRoutesRequest{})
		return err
	})
	s.Require().NoError(err, "core service failed to become ready")
}

// waitForService waits for a service to pass a health check.
func (s *GordonTestSuite) waitForService(ctx context.Context, name string, check func(context.Context) error) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s service: %w", name, ctx.Err())
		case <-ticker.C:
			if err := check(ctx); err == nil {
				s.T().Logf("%s service is ready", name)
				return nil
			}
		}
	}
}

// TestGordonIntegration runs all integration tests sequentially.
func TestGordonIntegration(t *testing.T) {
	suite.Run(t, new(GordonTestSuite))
}
