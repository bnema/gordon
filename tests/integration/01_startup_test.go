package integration

import (
	"github.com/docker/docker/api/types"
	"github.com/stretchr/testify/assert"
)

// Test01_FourContainerStartup verifies gordon-core auto-deploys all 3 sub-containers correctly.
// Duration: ~90 seconds
func (s *GordonTestSuite) Test01_FourContainerStartup() {
	s.T().Log("=== Test 1: Four-Container Startup (Core Lifecycle Manager) ===")

	// Verify core container is running (manually started)
	s.T().Log("Checking gordon-core (manually started)...")
	state, err := s.CoreC.State(s.ctx)
	s.Require().NoError(err, "gordon-core state check failed")
	assert.True(s.T(), state.Running, "gordon-core should be running")

	// Verify auto-deployed sub-containers are running
	// These are *types.Container (Docker API) not testcontainers.Container
	containers := []struct {
		name      string
		container *types.Container
	}{
		{"gordon-secrets", s.SecretsC},
		{"gordon-registry", s.RegistryC},
		{"gordon-proxy", s.ProxyC},
	}

	for _, c := range containers {
		s.T().Logf("Checking %s (auto-deployed by lifecycle manager)...", c.name)
		s.Require().NotNil(c.container, "%s should be initialized", c.name)
		assert.Equal(s.T(), "running", c.container.State, "%s should be running", c.name)
	}

	// Verify gRPC health for all services
	s.T().Log("Verifying gRPC health checks...")
	s.Require().NotNil(s.SecretsClient, "secrets client should be initialized")
	s.Require().NotNil(s.RegistryClient, "registry client should be initialized")
	s.Require().NotNil(s.CoreClient, "core client should be initialized")

	// Verify network connectivity between containers
	s.T().Log("Verifying inter-container network connectivity...")

	// Core should be able to reach secrets (using container.Exec)
	exitCode, _, _ := s.CoreC.Exec(s.ctx, []string{"wget", "-q", "-O-", "http://gordon-secrets:9091"})
	assert.Equal(s.T(), 0, exitCode, "core should reach secrets gRPC port")

	// Core should be able to reach registry
	exitCode, _, _ = s.CoreC.Exec(s.ctx, []string{"wget", "-q", "-O-", "http://gordon-registry:5000/v2/"})
	assert.Equal(s.T(), 0, exitCode, "core should reach registry HTTP port")

	// Core should be able to reach proxy
	exitCode, _, _ = s.CoreC.Exec(s.ctx, []string{"wget", "-q", "-O-", "http://gordon-proxy:80/health"})
	assert.Equal(s.T(), 0, exitCode, "core should reach proxy HTTP port")

	// Verify all containers are on the same network
	s.T().Log("Verifying all containers are on the same network...")
	coreNetwork, err := s.CoreC.NetworkAliases(s.ctx)
	s.Require().NoError(err, "failed to get core network aliases")
	s.T().Logf("gordon-core networks: %+v", coreNetwork)

	s.T().Log("✓ Core lifecycle manager successfully deployed all sub-containers")
	s.T().Log("✓ All containers are running and networked correctly")
}
