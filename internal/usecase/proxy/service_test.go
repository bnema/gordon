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

	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false)

	result, err := svc.GetTarget(ctx, "app.example.com")

	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
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
