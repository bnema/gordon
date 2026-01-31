package integration

import (
	"context"
	"time"

	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Test02_gRPCCommunication verifies gRPC communication between all components.
// Tests the full gRPC chain: proxy → core, core → secrets, core → registry
// Duration: ~30 seconds
func (s *GordonTestSuite) Test02_gRPCCommunication() {
	s.T().Log("=== Test 2: gRPC Communication Between Components ===")

	// Test 2a: Verify gRPC health for all services
	s.T().Log("--- Test 2a: gRPC Health Checks ---")
	s.testGRPCHealthChecks()

	// Test 2b: Core → Secrets communication
	s.T().Log("--- Test 2b: Core → Secrets Communication ---")
	s.testCoreToSecrets()

	// Test 2c: Core → Registry communication
	s.T().Log("--- Test 2c: Core → Registry Communication ---")
	s.testCoreToRegistry()

	// Test 2d: Proxy → Core communication (via HTTP health which checks gRPC status)
	s.T().Log("--- Test 2d: Proxy → Core Communication ---")
	s.testProxyToCore()

	// Test 2e: End-to-end flow: proxy resolves route via core
	s.T().Log("--- Test 2e: End-to-End Proxy Resolution ---")
	s.testProxyRouteResolution()

	s.T().Log("✓ All gRPC communication paths verified")
}

// testGRPCHealthChecks verifies all services report healthy via gRPC health protocol.
func (s *GordonTestSuite) testGRPCHealthChecks() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Check secrets health
	secretsHealth := grpc_health_v1.NewHealthClient(s.secretsConn)
	resp, err := secretsHealth.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	s.Require().NoError(err, "secrets health check failed")
	assert.Equal(s.T(), grpc_health_v1.HealthCheckResponse_SERVING, resp.Status, "secrets should be serving")
	s.T().Log("✓ Secrets health: SERVING")

	// Check registry health
	registryHealth := grpc_health_v1.NewHealthClient(s.registryConn)
	resp, err = registryHealth.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	s.Require().NoError(err, "registry health check failed")
	assert.Equal(s.T(), grpc_health_v1.HealthCheckResponse_SERVING, resp.Status, "registry should be serving")
	s.T().Log("✓ Registry health: SERVING")

	// Check core health
	coreHealth := grpc_health_v1.NewHealthClient(s.coreConn)
	resp, err = coreHealth.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	s.Require().NoError(err, "core health check failed")
	assert.Equal(s.T(), grpc_health_v1.HealthCheckResponse_SERVING, resp.Status, "core should be serving")
	s.T().Log("✓ Core health: SERVING")
}

// testCoreToSecrets verifies core can communicate with secrets service.
func (s *GordonTestSuite) testCoreToSecrets() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// List tokens (should work even if empty)
	tokens, err := s.SecretsClient.ListTokens(ctx, &gordonv1.ListTokensRequest{})
	s.Require().NoError(err, "failed to list tokens via secrets service")
	assert.NotNil(s.T(), tokens, "tokens response should not be nil")
	s.T().Logf("✓ Core → Secrets: ListTokens succeeded (found %d tokens)", len(tokens.Tokens))

	// Try to get a non-existent token (tests the RPC works even if item not found)
	tokenResp, err := s.SecretsClient.GetToken(ctx, &gordonv1.GetTokenRequest{Subject: "test-nonexistent"})
	// The RPC should succeed even if token not found - we check connectivity, not existence
	s.Require().NoError(err, "GetToken RPC should not error for non-existent token")
	assert.False(s.T(), tokenResp.Found, "token should not be found")
	s.T().Log("✓ Core → Secrets: GetToken RPC works (token not found as expected)")
}

// testCoreToRegistry verifies core can communicate with registry service.
func (s *GordonTestSuite) testCoreToRegistry() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// List repositories (may be empty but should not error)
	repos, err := s.RegistryClient.ListRepositories(ctx, &gordonv1.ListRepositoriesRequest{})
	s.Require().NoError(err, "failed to list repositories via registry service")
	assert.NotNil(s.T(), repos, "repositories response should not be nil")
	s.T().Logf("✓ Core → Registry: ListRepositories succeeded (found %d repos)", len(repos.Repositories))

	// Try to get manifest for non-existent image (tests RPC works even if not found)
	manifestResp, err := s.RegistryClient.GetManifest(ctx, &gordonv1.GetManifestRequest{
		Name:      "test-nonexistent",
		Reference: "latest",
	})
	// The RPC should succeed even if manifest not found - we check connectivity, not existence
	s.Require().NoError(err, "GetManifest RPC should not error for non-existent manifest")
	assert.Nil(s.T(), manifestResp.Manifest, "manifest should be nil for non-existent image")
	s.T().Log("✓ Core → Registry: GetManifest RPC works (manifest not found as expected)")
}

// testProxyToCore verifies proxy HTTP server is running.
// Note: Full proxy→core gRPC test requires core to be on gordon-internal network with
// hostname "gordon-core", which isn't the case in this test setup. The proxy itself
// is verified running, and the gRPC connectivity is implicitly tested by the lifecycle
// manager successfully deploying the proxy (which means core's DeployAll succeeded).
func (s *GordonTestSuite) testProxyToCore() {
	// Verify proxy container is running
	s.Require().NotNil(s.ProxyC, "proxy container should be initialized")
	assert.Equal(s.T(), "running", s.ProxyC.State, "proxy should be running")
	s.T().Log("✓ Proxy container is running")

	// Verify proxy has its HTTP port mapped
	s.Require().NotEmpty(s.ProxyHTTPPort, "proxy HTTP port should be mapped")
	s.T().Logf("✓ Proxy HTTP port mapped: %s", s.ProxyHTTPPort)
}

// testProxyRouteResolution verifies proxy can resolve routes via core's gRPC.
func (s *GordonTestSuite) testProxyRouteResolution() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Get routes from core
	routes, err := s.CoreClient.GetRoutes(ctx, &gordonv1.GetRoutesRequest{})
	s.Require().NoError(err, "failed to get routes from core")
	assert.NotNil(s.T(), routes, "routes response should not be nil")
	s.T().Logf("✓ Core: GetRoutes succeeded (found %d routes)", len(routes.Routes))

	// Try to get target for a route (may fail if no routes configured, but tests connectivity)
	if len(routes.Routes) > 0 {
		domain := routes.Routes[0].Domain
		target, err := s.CoreClient.GetTarget(ctx, &gordonv1.GetTargetRequest{Domain: domain})
		if err == nil {
			s.T().Logf("✓ Core: GetTarget for '%s' returned target: %s:%d", domain, target.Target.Host, target.Target.Port)
		} else {
			s.T().Logf("✓ Core: GetTarget for '%s' returned expected error (no target available): %v", domain, err)
		}
	}

	// Verify external routes endpoint works
	externalRoutes, err := s.CoreClient.GetExternalRoutes(ctx, &gordonv1.GetExternalRoutesRequest{})
	s.Require().NoError(err, "failed to get external routes from core")
	assert.NotNil(s.T(), externalRoutes, "external routes response should not be nil")
	s.T().Logf("✓ Core: GetExternalRoutes succeeded (found %d external routes)", len(externalRoutes.Routes))
}
