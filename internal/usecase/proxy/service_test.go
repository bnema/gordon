package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
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

func TestService_GetTarget_CanonicalizesHostForLookup(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)
	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false)

	result, err := svc.GetTarget(ctx, "App.Example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_RejectsInvalidHostAuthority(t *testing.T) {
	svc := NewService(outmocks.NewMockContainerRuntime(t), inmocks.NewMockContainerService(t), inmocks.NewMockConfigService(t), Config{})
	result, err := svc.GetTarget(testContext(), "app.example.com:8080")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
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

	// Mock external routes - use public IP to pass SSRF check
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
		"reg.example.com": "203.0.113.10:5000", // TEST-NET-3 (RFC 5737) - documentation range
	})

	result, err := svc.GetTarget(ctx, "reg.example.com")

	assert.NoError(t, err)
	assert.Equal(t, "203.0.113.10", result.Host)
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

	// First call - should resolve external route (use public IP)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
		"reg.example.com": "203.0.113.10:5000",
	}).Once()

	result1, err := svc.GetTarget(ctx, "reg.example.com")
	assert.NoError(t, err)
	assert.Equal(t, "203.0.113.10", result1.Host)

	// Second call - should return from cache (no mock call needed)
	result2, err := svc.GetTarget(ctx, "reg.example.com")
	assert.NoError(t, err)
	assert.Equal(t, result1, result2)
}

func TestService_GetTarget_ExternalRoute_SSRFBlocked(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	tests := []struct {
		name   string
		target string
	}{
		{"localhost", "localhost:5000"},
		{"loopback IP", "127.0.0.1:5000"},
		{"private network 10.x", "10.0.0.1:5000"},
		{"private network 172.x", "172.16.0.1:5000"},
		{"private network 192.168.x", "192.168.1.1:5000"},
		{"AWS metadata", "169.254.169.254:80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear cache
			svc.targets = make(map[string]*domain.ProxyTarget)

			configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
				"ssrf.example.com": tt.target,
			}).Once()

			result, err := svc.GetTarget(ctx, "ssrf.example.com")

			assert.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrSSRFBlocked)
			assert.Nil(t, result)
		})
	}
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
	tests := []struct {
		name   string
		target string
	}{
		{name: "not a number", target: "localhost:abc"},
		{name: "zero", target: "localhost:0"},
		{name: "too high", target: "localhost:65536"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := outmocks.NewMockContainerRuntime(t)
			containerSvc := inmocks.NewMockContainerService(t)
			configSvc := inmocks.NewMockConfigService(t)

			svc := NewService(runtime, containerSvc, configSvc, Config{})
			ctx := testContext()

			configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{
				"invalid.example.com": tt.target,
			})

			result, err := svc.GetTarget(ctx, "invalid.example.com")

			assert.Error(t, err)
			assert.Nil(t, result)
		})
	}
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

	err := svc.RegisterTarget(ctx, "App.Example.com", target)

	assert.NoError(t, err)

	// Verify target is cached under the canonical domain.
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

	err := svc.UnregisterTarget(ctx, "App.Example.com")

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

	// Invalidate the target using mixed-case input.
	svc.InvalidateTarget(ctx, "App.Example.com")

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

	err := handler.Handle(context.Background(), event)

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
	err := handler.Handle(context.Background(), event)
	assert.NoError(t, err)
}

func TestRegistryInFlightTracking(t *testing.T) {
	svc := &Service{
		inFlight: make(map[string]int),
	}

	if got := svc.registryInFlight.Load(); got != 0 {
		t.Fatalf("expected 0 in-flight, got %d", got)
	}

	svc.registryInFlight.Add(1)
	if got := svc.registryInFlight.Load(); got != 1 {
		t.Fatalf("expected 1 in-flight after Add, got %d", got)
	}

	svc.registryInFlight.Add(-1)
	if got := svc.registryInFlight.Load(); got != 0 {
		t.Fatalf("expected 0 in-flight after release, got %d", got)
	}
}

func TestDrainRegistryInFlight(t *testing.T) {
	svc := &Service{
		inFlight: make(map[string]int),
	}

	svc.registryInFlight.Add(2)

	result := make(chan bool, 1)
	go func() {
		result <- svc.DrainRegistryInFlight(50 * time.Millisecond)
	}()

	time.Sleep(5 * time.Millisecond)
	svc.registryInFlight.Add(-1)
	svc.registryInFlight.Add(-1)

	select {
	case drained := <-result:
		if !drained {
			t.Fatalf("DrainRegistryInFlight returned false; expected true after all requests completed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("DrainRegistryInFlight did not return within timeout")
	}
}

func TestDrainRegistryInFlightTimeout(t *testing.T) {
	svc := &Service{
		inFlight: make(map[string]int),
	}

	// Add a request and never release it
	svc.registryInFlight.Add(1)

	drained := svc.DrainRegistryInFlight(30 * time.Millisecond)
	if drained {
		t.Fatal("expected DrainRegistryInFlight to return false on timeout, got true")
	}
}

func TestService_ProxyConfig_ReflectsUpdates(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{
		RegistryDomain:     "old.registry.com",
		RegistryPort:       5000,
		MaxBodySize:        1024,
		MaxResponseSize:    2048,
		MaxConcurrentConns: 10,
	})

	cfg := svc.ProxyConfig()
	assert.Equal(t, "old.registry.com", cfg.RegistryDomain)
	assert.Equal(t, 5000, cfg.RegistryPort)
	assert.Equal(t, int64(1024), cfg.MaxBodySize)
	assert.Equal(t, int64(2048), cfg.MaxResponseSize)
	assert.Equal(t, 10, cfg.MaxConcurrentConns)

	svc.UpdateConfig(Config{
		RegistryDomain:     "new.registry.com",
		RegistryPort:       5001,
		MaxBodySize:        4096,
		MaxResponseSize:    8192,
		MaxConcurrentConns: 50,
	})

	cfg = svc.ProxyConfig()
	assert.Equal(t, "new.registry.com", cfg.RegistryDomain)
	assert.Equal(t, 5001, cfg.RegistryPort)
	assert.Equal(t, int64(4096), cfg.MaxBodySize)
	assert.Equal(t, int64(8192), cfg.MaxResponseSize)
	assert.Equal(t, 50, cfg.MaxConcurrentConns)
}

func TestService_IsRegistryDomain(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{
		RegistryDomain: "registry.example.com",
	})

	assert.True(t, svc.IsRegistryDomain("registry.example.com"))
	assert.False(t, svc.IsRegistryDomain("other.example.com"))
	assert.False(t, svc.IsRegistryDomain(""))
}

func TestService_IsRegistryDomain_EmptyConfig(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})

	assert.False(t, svc.IsRegistryDomain("registry.example.com"))
	assert.False(t, svc.IsRegistryDomain(""))
}

func TestService_TrackInFlight(t *testing.T) {
	svc := &Service{
		inFlight: make(map[string]int),
	}

	// Empty container ID returns noop
	release := svc.TrackInFlight("")
	release() // should not panic

	// Track a container
	release1 := svc.TrackInFlight("c-1")
	svc.inFlightMu.Lock()
	assert.Equal(t, 1, svc.inFlight["c-1"])
	svc.inFlightMu.Unlock()

	// Track same container again
	release2 := svc.TrackInFlight("c-1")
	svc.inFlightMu.Lock()
	assert.Equal(t, 2, svc.inFlight["c-1"])
	svc.inFlightMu.Unlock()

	// Release one
	release2()
	svc.inFlightMu.Lock()
	assert.Equal(t, 1, svc.inFlight["c-1"])
	svc.inFlightMu.Unlock()

	// Release last — should delete key
	release1()
	svc.inFlightMu.Lock()
	_, exists := svc.inFlight["c-1"]
	assert.False(t, exists, "container should be removed from inFlight map when count reaches 0")
	svc.inFlightMu.Unlock()
}

func TestService_TrackRegistryRequest(t *testing.T) {
	svc := &Service{
		inFlight: make(map[string]int),
	}

	assert.Equal(t, int64(0), svc.RegistryInFlight())

	svc.TrackRegistryRequest()
	assert.Equal(t, int64(1), svc.RegistryInFlight())

	svc.TrackRegistryRequest()
	assert.Equal(t, int64(2), svc.RegistryInFlight())

	svc.ReleaseRegistryRequest()
	assert.Equal(t, int64(1), svc.RegistryInFlight())

	svc.ReleaseRegistryRequest()
	assert.Equal(t, int64(0), svc.RegistryInFlight())
}

func TestService_GetTarget_HostMode_UsesProxyPortLabel(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	// No external routes
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})

	// Container exists for this domain
	container := &domain.Container{
		ID:    "c-gitea",
		Image: "gitea/gitea:latest",
	}
	containerSvc.EXPECT().Get(mock.Anything, "git.example.com").Return(container, true)

	// Route exists
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "git.example.com", Image: "gitea/gitea:latest"},
	})

	// Image has gordon.proxy.port=3000 label
	runtime.EXPECT().GetImageLabels(mock.Anything, "gitea/gitea:latest").Return(map[string]string{
		domain.LabelProxyPort: "3000",
	}, nil)

	// Host port mapping for internal port 3000
	runtime.EXPECT().GetContainerPort(mock.Anything, "c-gitea", 3000).Return(32000, nil)

	result, err := svc.GetTarget(ctx, "git.example.com")

	assert.NoError(t, err)
	assert.Equal(t, "localhost", result.Host)
	assert.Equal(t, 32000, result.Port)
	assert.Equal(t, "c-gitea", result.ContainerID)
}

func TestService_GetTarget_HostMode_UsesDeprecatedPortLabel(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})

	container := &domain.Container{
		ID:    "c-app",
		Image: "myapp:latest",
	}
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(container, true)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:latest"},
	})

	// Image has deprecated gordon.port label — should still work
	runtime.EXPECT().GetImageLabels(mock.Anything, "myapp:latest").Return(map[string]string{
		domain.LabelPort: "8080",
	}, nil)

	runtime.EXPECT().GetContainerPort(mock.Anything, "c-app", 8080).Return(33000, nil)

	result, err := svc.GetTarget(ctx, "app.example.com")

	assert.NoError(t, err)
	assert.Equal(t, 33000, result.Port)
}

func TestService_GetTarget_HostMode_ProxyPortWinsOverPort(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})

	container := &domain.Container{
		ID:    "c-dual",
		Image: "dualapp:latest",
	}
	containerSvc.EXPECT().Get(mock.Anything, "dual.example.com").Return(container, true)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "dual.example.com", Image: "dualapp:latest"},
	})

	// Both labels set — gordon.proxy.port=9000 should win over gordon.port=3000
	runtime.EXPECT().GetImageLabels(mock.Anything, "dualapp:latest").Return(map[string]string{
		domain.LabelProxyPort: "9000",
		domain.LabelPort:      "3000",
	}, nil)

	// Should use 9000 (gordon.proxy.port), NOT 3000 (gordon.port)
	runtime.EXPECT().GetContainerPort(mock.Anything, "c-dual", 9000).Return(34000, nil)

	result, err := svc.GetTarget(ctx, "dual.example.com")

	assert.NoError(t, err)
	assert.Equal(t, 34000, result.Port)
}

func TestService_GetTarget_HostMode_FallsBackToExposedPort(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})

	container := &domain.Container{
		ID:    "c-plain",
		Image: "plain:latest",
	}
	containerSvc.EXPECT().Get(mock.Anything, "plain.example.com").Return(container, true)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "plain.example.com", Image: "plain:latest"},
	})

	// No port labels — should fall back to first exposed port
	runtime.EXPECT().GetImageLabels(mock.Anything, "plain:latest").Return(map[string]string{}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "plain:latest").Return([]int{8080}, nil)

	runtime.EXPECT().GetContainerPort(mock.Anything, "c-plain", 8080).Return(35000, nil)

	result, err := svc.GetTarget(ctx, "plain.example.com")

	assert.NoError(t, err)
	assert.Equal(t, 35000, result.Port)
}

func TestService_GetTarget_HostMode_H2CProtocolPropagated(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})

	container := &domain.Container{
		ID:    "c-grpc",
		Image: "grpc-app:latest",
	}
	containerSvc.EXPECT().Get(mock.Anything, "grpc.example.com").Return(container, true)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "grpc.example.com", Image: "grpc-app:latest"},
	})

	runtime.EXPECT().GetImageLabels(mock.Anything, "grpc-app:latest").Return(map[string]string{
		domain.LabelProxyPort:     "50051",
		domain.LabelProxyProtocol: "h2c",
	}, nil)

	runtime.EXPECT().GetContainerPort(mock.Anything, "c-grpc", 50051).Return(50051, nil)

	result, err := svc.GetTarget(ctx, "grpc.example.com")

	assert.NoError(t, err)
	assert.Equal(t, "h2c", result.Protocol)
	assert.Equal(t, 50051, result.Port)
}

func TestService_GetTarget_HostMode_DefaultProtocol(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	svc := NewService(runtime, containerSvc, configSvc, Config{})
	ctx := testContext()

	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})

	container := &domain.Container{
		ID:    "c-web",
		Image: "web:latest",
	}
	containerSvc.EXPECT().Get(mock.Anything, "web.example.com").Return(container, true)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "web.example.com", Image: "web:latest"},
	})

	runtime.EXPECT().GetImageLabels(mock.Anything, "web:latest").Return(map[string]string{
		domain.LabelProxyPort: "8080",
	}, nil)

	runtime.EXPECT().GetContainerPort(mock.Anything, "c-web", 8080).Return(8080, nil)

	result, err := svc.GetTarget(ctx, "web.example.com")

	assert.NoError(t, err)
	assert.Equal(t, "", result.Protocol)
}
