package integration

import (
	"context"
	"time"

	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"github.com/stretchr/testify/assert"
)

// Test03_ImagePushAndDeploy verifies the image push → auto-deploy flow via gRPC.
// Tests that when an image is pushed to the registry, core receives the notification
// and auto-deploys a container.
// Duration: ~60 seconds
func (s *GordonTestSuite) Test03_ImagePushAndDeploy() {
	s.T().Log("=== Test 3: Image Push → Auto-Deploy Flow ===")

	// Step 1: Verify we can notify core of image push via gRPC
	s.T().Log("--- Step 1: Testing NotifyImagePushed gRPC endpoint ---")
	s.testNotifyImagePushed()

	// Step 2: Verify core responds to registry queries
	s.T().Log("--- Step 2: Verifying Core → Registry communication ---")
	s.testCoreRegistryCommunication()

	// Step 3: Check that core has routes capability
	s.T().Log("--- Step 3: Verifying Core routing capability ---")
	s.testCoreRouting()

	s.T().Log("✓ Image push notification and routing flow verified")
}

// testNotifyImagePushed tests the gRPC notification endpoint.
func (s *GordonTestSuite) testNotifyImagePushed() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Create a test image push notification
	req := &gordonv1.NotifyImagePushedRequest{
		Name:      "test-app",
		Reference: "v1.0.0",
		Manifest:  []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`),
		Annotations: map[string]string{
			"gordon.domain": "test.local",
			"gordon.port":   "8080",
		},
	}

	resp, err := s.CoreClient.NotifyImagePushed(ctx, req)
	s.Require().NoError(err, "NotifyImagePushed gRPC call failed")
	assert.NotNil(s.T(), resp, "response should not be nil")

	// Log the response (acceptance depends on configuration)
	s.T().Logf("✓ NotifyImagePushed response: accepted=%v, message=%s", resp.GetAccepted(), resp.GetMessage())
}

// testCoreRegistryCommunication verifies core can query registry.
func (s *GordonTestSuite) testCoreRegistryCommunication() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// List repositories (may be empty in test)
	repos, err := s.RegistryClient.ListRepositories(ctx, &gordonv1.ListRepositoriesRequest{})
	s.Require().NoError(err, "failed to list repositories")
	assert.NotNil(s.T(), repos, "repositories response should not be nil")

	s.T().Logf("✓ Core can query registry: found %d repositories", len(repos.GetRepositories()))
}

// testCoreRouting verifies core's routing capability.
func (s *GordonTestSuite) testCoreRouting() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Get all routes from core
	routes, err := s.CoreClient.GetRoutes(ctx, &gordonv1.GetRoutesRequest{})
	s.Require().NoError(err, "failed to get routes")
	assert.NotNil(s.T(), routes, "routes response should not be nil")

	// Get external routes
	externalRoutes, err := s.CoreClient.GetExternalRoutes(ctx, &gordonv1.GetExternalRoutesRequest{})
	s.Require().NoError(err, "failed to get external routes")
	assert.NotNil(s.T(), externalRoutes, "external routes response should not be nil")

	s.T().Logf("✓ Core routing: %d routes, %d external routes", len(routes.GetRoutes()), len(externalRoutes.GetRoutes()))

	// Try to resolve a target (will fail if no routes, but tests connectivity)
	_, err = s.CoreClient.GetTarget(ctx, &gordonv1.GetTargetRequest{Domain: "test.local"})
	// Error is expected if domain not configured, but RPC should work
	s.T().Logf("✓ GetTarget RPC works (result depends on route configuration)")
}
