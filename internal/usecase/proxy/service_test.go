package proxy

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "gordon/internal/boundaries/in/mocks"
	outmocks "gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestService_GetTarget_FromCache(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	config := Config{
		RegistryDomain: "registry.example.com",
		RegistryPort:   5000,
	}
	svc := NewService(runtime, containerSvc, configSvc, config)
	ctx := testContext()

	// Pre-populate cache
	cachedTarget := &domain.ProxyTarget{
		Host:        "192.168.1.100",
		Port:        8080,
		ContainerID: "container-123",
		Scheme:      "http",
	}
	svc.targets["app.example.com"] = cachedTarget

	// No mock calls expected - should return from cache

	result, err := svc.GetTarget(ctx, "app.example.com")

	assert.NoError(t, err)
	assert.Equal(t, cachedTarget, result)
}

func TestService_GetTarget_ContainerNotFound(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	config := Config{}
	svc := NewService(runtime, containerSvc, configSvc, config)
	ctx := testContext()

	// Mock external routes (empty - no match)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false)

	result, err := svc.GetTarget(ctx, "app.example.com")

	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_ExternalRoute(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Mock external routes
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
		"reg.example.com": "localhost:5000",
	})

	result, err := svc.GetTarget(ctx, "reg.example.com")

	assert.NoError(t, err)
	assert.Equal(t, "localhost", result.Host)
	assert.Equal(t, 5000, result.Port)
	assert.Equal(t, "", result.ContainerID)
	assert.Equal(t, "http", result.Scheme)
}

func TestService_GetTarget_ExternalRoute_Cached(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// First call - should resolve external route
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
		"reg.example.com": "localhost:5000",
	}).Once()

	result1, err := svc.GetTarget(ctx, "reg.example.com")
	assert.NoError(t, err)
	assert.Equal(t, "localhost", result1.Host)

	// Second call - should return from cache (no mock call needed)
	result2, err := svc.GetTarget(ctx, "reg.example.com")
	assert.NoError(t, err)
	assert.Equal(t, result1, result2)
}

func TestService_GetTarget_ExternalRoute_InvalidTarget(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Mock external routes with invalid format (missing port)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
		"invalid.example.com": "not-valid-format",
	})

	result, err := svc.GetTarget(ctx, "invalid.example.com")

	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestService_GetTarget_ExternalRoute_InvalidPort(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Mock external routes with invalid port
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
		"invalid.example.com": "localhost:abc",
	})

	result, err := svc.GetTarget(ctx, "invalid.example.com")

	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestService_RegisterTarget(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	target := &domain.ProxyTarget{
		Host:        "192.168.1.100",
		Port:        8080,
		ContainerID: "container-123",
		Scheme:      "http",
	}

	err := svc.RegisterTarget(ctx, "app.example.com", target)

	assert.NoError(t, err)

	// Verify target is cached
	svc.mu.RLock()
	cached := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.Equal(t, target, cached)
}

func TestService_UnregisterTarget(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Pre-populate
	svc.targets["app.example.com"] = &domain.ProxyTarget{
		Host: "192.168.1.100",
		Port: 8080,
	}

	err := svc.UnregisterTarget(ctx, "app.example.com")

	assert.NoError(t, err)

	// Verify target is removed
	svc.mu.RLock()
	_, exists := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.False(t, exists)
}

func TestService_RefreshTargets(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Pre-populate with some targets
	svc.targets["app1.example.com"] = &domain.ProxyTarget{Host: "192.168.1.100"}
	svc.targets["app2.example.com"] = &domain.ProxyTarget{Host: "192.168.1.101"}

	err := svc.RefreshTargets(ctx)

	assert.NoError(t, err)

	// Verify all targets are cleared
	svc.mu.RLock()
	count := len(svc.targets)
	svc.mu.RUnlock()

	assert.Equal(t, 0, count)
}

func TestService_UpdateConfig(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{
		RegistryDomain: "old.registry.com",
		RegistryPort:   5000,
	})

	newConfig := Config{
		RegistryDomain: "new.registry.com",
		RegistryPort:   5001,
	}

	svc.UpdateConfig(newConfig)

	assert.Equal(t, "new.registry.com", svc.config.RegistryDomain)
	assert.Equal(t, 5001, svc.config.RegistryPort)
}

func TestService_isRunningInContainer(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})

	// This test just verifies the method doesn't panic
	// The actual result depends on the environment
	result := svc.isRunningInContainer()
	assert.IsType(t, true, result) // Just verify it returns a bool
}

func TestService_InvalidateTarget(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Pre-populate cache with target
	svc.targets["app.example.com"] = &domain.ProxyTarget{
		Host:        "192.168.1.100",
		Port:        8080,
		ContainerID: "old-container",
	}

	// Invalidate the target
	svc.InvalidateTarget(ctx, "app.example.com")

	// Verify target is removed from cache
	svc.mu.RLock()
	_, exists := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.False(t, exists, "target should be removed from cache after invalidation")
}

func TestContainerDeployedHandler_CanHandle(t *testing.T) {
	handler := NewContainerDeployedHandler(testContext(), nil)

	assert.True(t, handler.CanHandle(domain.EventContainerDeployed))
	assert.False(t, handler.CanHandle(domain.EventImagePushed))
	assert.False(t, handler.CanHandle(domain.EventConfigReload))
}

func TestContainerDeployedHandler_Handle_InvalidatesCache(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// Pre-populate cache
	svc.targets["app.example.com"] = &domain.ProxyTarget{
		Host:        "192.168.1.100",
		Port:        8080,
		ContainerID: "old-container",
	}

	// Create handler with service as invalidator
	handler := NewContainerDeployedHandler(ctx, svc)

	// Simulate container deployed event
	event := domain.Event{
		ID:    "event-123",
		Type:  domain.EventContainerDeployed,
		Route: "app.example.com",
		Data: &domain.ContainerEventPayload{
			ContainerID: "new-container",
			Domain:      "app.example.com",
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)

	// Verify target was invalidated
	svc.mu.RLock()
	_, exists := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.False(t, exists, "cache should be invalidated after container deployed event")
}

func TestContainerDeployedHandler_Handle_NoDomain(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	handler := NewContainerDeployedHandler(ctx, svc)

	// Event with no domain
	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventContainerDeployed,
		Data: &domain.ContainerEventPayload{
			ContainerID: "new-container",
		},
	}

	// Should not error, just skip
	err := handler.Handle(event)
	assert.NoError(t, err)
}
