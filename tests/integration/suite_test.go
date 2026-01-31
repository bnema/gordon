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
	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
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

	// Docker client
	dockerClient *client.Client

	// Core container (only one we start manually)
	CoreC testcontainers.Container

	// Sub-containers (auto-deployed by core's lifecycle manager)
	SecretsC  *container.Summary
	RegistryC *container.Summary
	ProxyC    *container.Summary

	// gRPC clients (initialized after containers start)
	SecretsClient  gordonv1.SecretsServiceClient
	RegistryClient gordonv1.RegistryInspectServiceClient
	CoreClient     gordonv1.CoreServiceClient

	// gRPC connections (stored for health checks and cleanup)
	secretsConn  *grpc.ClientConn
	registryConn *grpc.ClientConn
	coreConn     *grpc.ClientConn

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

	// Start only gordon-core on the gordon-internal network
	// (lifecycle manager will create the network and deploy sub-containers)
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

	// Close gRPC connections
	if s.secretsConn != nil {
		_ = s.secretsConn.Close()
	}
	if s.registryConn != nil {
		_ = s.registryConn.Close()
	}
	if s.coreConn != nil {
		_ = s.coreConn.Close()
	}

	// Stop manually started core container
	if s.CoreC != nil {
		_ = s.CoreC.Terminate(ctx)
	}

	// Stop auto-deployed containers
	containers := []*container.Summary{s.ProxyC, s.RegistryC, s.SecretsC}
	for _, c := range containers {
		if c != nil {
			_ = s.dockerClient.ContainerStop(ctx, c.ID, container.StopOptions{})
			_ = s.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
		}
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

	binds := []string{
		fmt.Sprintf("%s:/var/run/docker.sock", helpers.GetDockerSocketPath()),
		fmt.Sprintf("%s:/app/gordon.toml", configPath),
	}

	// Create gordon-internal network first (core needs to be on it for proxy to reach it)
	networkName := "gordon-internal"
	if _, err := s.dockerClient.NetworkInspect(s.ctx, networkName, network.InspectOptions{}); err != nil {
		if !errdefs.IsNotFound(err) {
			s.Require().NoError(err, "failed to inspect gordon-internal network")
		}
		_, err = s.dockerClient.NetworkCreate(s.ctx, networkName, network.CreateOptions{
			Driver:     "bridge",
			Attachable: true,
		})
		s.Require().NoError(err, "failed to create gordon-internal network")
	}

	// Core takes longer to start as it needs to deploy sub-containers
	// Use simple port wait with long timeout - HTTP starts after DeployAll completes
	waitStrategy := wait.ForListeningPort("9090/tcp").WithStartupTimeout(coreStartupTimeout)

	req := testcontainers.ContainerRequest{
		Image:        testImageName,
		Name:         "gordon-core",
		ExposedPorts: []string{"5000/tcp", "9090/tcp"},
		Env:          env,
		Cmd:          []string{"--component=core"},
		WaitingFor:   waitStrategy,
		Networks:     []string{networkName},
		HostConfigModifier: func(hostConfig *container.HostConfig) {
			hostConfig.Binds = append(hostConfig.Binds, binds...)
		},
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

	requiredComponents := map[string]**container.Summary{
		"secrets":  &s.SecretsC,
		"registry": &s.RegistryC,
		"proxy":    &s.ProxyC,
	}

	for {
		// List containers with gordon.component label
		filters := filters.NewArgs()
		filters.Add("label", "gordon.component")

		containers, err := s.dockerClient.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters,
		})
		if err != nil {
			s.T().Logf("Error listing containers: %v", err)
			continue
		}

		found := make(map[string]*container.Summary)
		for i := range containers {
			c := &containers[i]
			if compLabel, ok := c.Labels["gordon.component"]; ok {
				// Skip core itself - we only want sub-containers
				if compLabel != "core" {
					found[compLabel] = c
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
	containers := []*container.Summary{s.SecretsC, s.RegistryC, s.ProxyC}
	for i, c := range containers {
		if c == nil {
			continue
		}

		// Inspect container to get detailed info including ports
		inspect, err := s.dockerClient.ContainerInspect(s.ctx, c.ID)
		s.Require().NoError(err, "failed to inspect container %d", i)

		component := c.Labels["gordon.component"]
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
	var err error
	s.secretsConn, err = grpc.NewClient(
		secretsAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err, "failed to connect to secrets service")
	s.SecretsClient = gordonv1.NewSecretsServiceClient(s.secretsConn)

	// Initialize registry client
	registryAddr := net.JoinHostPort("localhost", s.RegistryGRPCPort)
	s.registryConn, err = grpc.NewClient(
		registryAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err, "failed to connect to registry service")
	s.RegistryClient = gordonv1.NewRegistryInspectServiceClient(s.registryConn)

	// Initialize core client
	coreAddr := net.JoinHostPort("localhost", s.CoreGRPCPort)
	s.coreConn, err = grpc.NewClient(
		coreAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	s.Require().NoError(err, "failed to connect to core service")
	s.CoreClient = gordonv1.NewCoreServiceClient(s.coreConn)

	// Wait for services to be ready with gRPC health checks
	s.waitForServicesWithHealth()

	s.T().Log("gRPC clients initialized successfully")
}

// waitForServicesWithHealth waits for all gRPC services to be ready using proper gRPC health checks.
func (s *GordonTestSuite) waitForServicesWithHealth() {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	// Check secrets service health
	s.T().Log("Waiting for secrets service health...")
	secretsHealthClient := grpc_health_v1.NewHealthClient(s.secretsConn)
	err := s.waitForGRPCHealth(ctx, "secrets", secretsHealthClient)
	s.Require().NoError(err, "secrets service health check failed")

	// Check registry service health
	s.T().Log("Waiting for registry service health...")
	registryHealthClient := grpc_health_v1.NewHealthClient(s.registryConn)
	err = s.waitForGRPCHealth(ctx, "registry", registryHealthClient)
	s.Require().NoError(err, "registry service health check failed")

	// Check core service health
	s.T().Log("Waiting for core service health...")
	coreHealthClient := grpc_health_v1.NewHealthClient(s.coreConn)
	err = s.waitForGRPCHealth(ctx, "core", coreHealthClient)
	s.Require().NoError(err, "core service health check failed")

	s.T().Log("All gRPC services are healthy")
}

// waitForGRPCHealth waits for a gRPC health check to pass.
func (s *GordonTestSuite) waitForGRPCHealth(ctx context.Context, name string, client grpc_health_v1.HealthClient) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s service health: %w", name, ctx.Err())
		case <-ticker.C:
			resp, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
			if err == nil && resp.Status == grpc_health_v1.HealthCheckResponse_SERVING {
				s.T().Logf("%s service health check passed", name)
				return nil
			}
		}
	}
}

// TestGordonIntegration runs all integration tests sequentially.
func TestGordonIntegration(t *testing.T) {
	suite.Run(t, new(GordonTestSuite))
}

// refreshAllContainerRefs updates all sub-container references by looking up current containers by label.
// This is needed because containers may be restarted/replaced during auto-restart tests.
func (s *GordonTestSuite) refreshAllContainerRefs() {
	containers, err := s.dockerClient.ContainerList(s.ctx, container.ListOptions{All: true})
	if err != nil {
		s.T().Logf("Warning: failed to refresh container refs: %v", err)
		return
	}

	for i := range containers {
		c := &containers[i]
		if compLabel, ok := c.Labels["gordon.component"]; ok {
			if c.State == "running" {
				switch compLabel {
				case "secrets":
					s.SecretsC = c
				case "registry":
					s.RegistryC = c
				case "proxy":
					s.ProxyC = c
				}
			}
		}
	}
}
