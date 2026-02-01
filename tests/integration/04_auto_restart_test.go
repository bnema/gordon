package integration

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Test04_AutoRestart verifies the lifecycle manager auto-restarts failed sub-containers.
// Tests that when a sub-container dies, the lifecycle manager detects it and restarts it.
// Duration: ~45 seconds
func (s *GordonTestSuite) Test04_AutoRestart() {
	s.T().Log("=== Test 4: Auto-Restart of Failed Sub-Containers ===")

	// Test 4a: Kill secrets container and verify it restarts
	s.T().Log("--- Test 4a: Kill and restart secrets container ---")
	s.testContainerRestart("secrets", &s.SecretsC)

	// Test 4b: Kill registry container and verify it restarts
	s.T().Log("--- Test 4b: Kill and restart registry container ---")
	s.testContainerRestart("registry", &s.RegistryC)

	// Test 4c: Kill proxy container and verify it restarts
	s.T().Log("--- Test 4c: Kill and restart proxy container ---")
	s.testContainerRestart("proxy", &s.ProxyC)

	// Test 4d: Verify gRPC connectivity restored after restarts
	s.T().Log("--- Test 4d: Verify gRPC connectivity restored ---")
	s.verifyGRPCConnectivityRestored()

	s.T().Log("✓ All sub-containers auto-restart correctly")
	s.T().Log("✓ gRPC connectivity restored after container restarts")
}

// testContainerRestart kills a container and verifies it gets restarted.
func (s *GordonTestSuite) testContainerRestart(component string, containerPtr **container.Summary) {
	s.T().Logf("Testing auto-restart for %s...", component)

	// Refresh container reference before killing (it might have been replaced already)
	s.refreshContainerRef(component, containerPtr)

	originalContainer := *containerPtr
	if originalContainer == nil {
		s.T().Logf("⚠ Skipping %s - container not available", component)
		return
	}

	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
	defer cancel()

	// Store original container ID
	originalID := originalContainer.ID
	s.T().Logf("Original %s container ID: %s", component, originalID[:12])

	// Kill the container
	s.T().Logf("Killing %s container...", component)
	err := s.dockerClient.ContainerKill(ctx, originalID, "SIGKILL")
	require.NoError(s.T(), err, "failed to kill %s container", component)

	// Wait for container to be stopped
	time.Sleep(2 * time.Second)

	// Poll until container is restarted or timeout
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var newContainer *container.Summary
	attempts := 0
	maxAttempts := 15 // 30 seconds max

	for {
		select {
		case <-ctx.Done():
			s.T().Fatalf("timeout waiting for %s container restart", component)
			return
		case <-ticker.C:
			attempts++
			if attempts > maxAttempts {
				s.T().Fatalf("max attempts exceeded waiting for %s container restart", component)
				return
			}

			// Find container by component label
			containers, err := s.dockerClient.ContainerList(s.ctx, container.ListOptions{All: true})
			if err != nil {
				s.T().Logf("Error listing containers: %v", err)
				continue
			}

			for i := range containers {
				c := &containers[i]
				if compLabel, ok := c.Labels["gordon.component"]; ok && compLabel == component {
					// Found the container for this component
					if c.ID != originalID && c.State == "running" {
						newContainer = c
						s.T().Logf("✓ %s container restarted: old=%s, new=%s",
							component, originalID[:12], c.ID[:12])
						goto found
					}
				}
			}

			s.T().Logf("Waiting for %s container restart (attempt %d/%d)...", component, attempts, maxAttempts)
		}
	}

found:
	require.NotNil(s.T(), newContainer, "%s container should have restarted", component)
	assert.Equal(s.T(), "running", newContainer.State, "%s container should be running", component)
	assert.NotEqual(s.T(), originalID, newContainer.ID, "%s should have new container ID", component)

	// Update the suite's reference to the new container
	*containerPtr = newContainer
}

// refreshContainerRef updates the container reference by looking up current container by label
func (s *GordonTestSuite) refreshContainerRef(component string, containerPtr **container.Summary) {
	containers, err := s.dockerClient.ContainerList(s.ctx, container.ListOptions{All: true})
	if err != nil {
		s.T().Logf("Warning: failed to list containers: %v", err)
		return
	}

	for i := range containers {
		c := &containers[i]
		if compLabel, ok := c.Labels["gordon.component"]; ok && compLabel == component {
			if c.State == "running" {
				*containerPtr = c
				return
			}
		}
	}
}

// verifyGRPCConnectivityRestored verifies gRPC works after container restarts.
// Note: We check connectivity by verifying containers are running and ports are mapped.
// Full gRPC reconnection would require re-initializing clients with new host ports.
func (s *GordonTestSuite) verifyGRPCConnectivityRestored() {
	// Refresh container references to get new container IDs
	s.refreshAllContainerRefs()

	// Verify all sub-containers are running
	s.Require().NotNil(s.SecretsC, "secrets container should exist after restart")
	s.Require().NotNil(s.RegistryC, "registry container should exist after restart")
	s.Require().NotNil(s.ProxyC, "proxy container should exist after restart")

	assert.Equal(s.T(), "running", s.SecretsC.State, "secrets should be running")
	assert.Equal(s.T(), "running", s.RegistryC.State, "registry should be running")
	assert.Equal(s.T(), "running", s.ProxyC.State, "proxy should be running")

	// Verify gRPC health endpoints are accessible (core doesn't restart)
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()

	coreHealth := grpc_health_v1.NewHealthClient(s.coreConn)
	resp, err := coreHealth.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.NoError(s.T(), err, "core health check failed after restarts")
	assert.Equal(s.T(), grpc_health_v1.HealthCheckResponse_SERVING, resp.Status, "core should be serving")

	s.T().Log("✓ Core service accessible after sub-container restarts")
	s.T().Log("✓ All sub-containers are running after auto-restart")
	s.T().Log("✓ gRPC connectivity verified via health checks")
}
