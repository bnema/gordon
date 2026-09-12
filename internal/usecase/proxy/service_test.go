package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

// stubProvider is a fixed TargetProvider for proxy tests.
type stubProvider struct {
	backends map[string]domain.AppBackend
}

func (s *stubProvider) LookupHost(host string) (domain.AppBackend, bool) {
	backend, ok := s.backends[host]
	return backend, ok
}

func testService(t *testing.T, configSvc *inmocks.MockConfigService, provider TargetProvider) *Service {
	if configSvc == nil {
		configSvc = inmocks.NewMockConfigService(t)
	}
	svc := NewService(configSvc, Config{})
	if provider != nil {
		svc.WithAppTargets(provider)
	}
	return svc
}

func appBackend(host string, port int) domain.AppBackend {
	return domain.AppBackend{
		Host:          "127.0.0.1",
		Port:          port,
		ContainerPort: 8080,
		ContainerID:   "c-app",
	}
}

func TestService_GetTarget_FromCache(t *testing.T) {
	svc := testService(t, nil, nil)
	ctx := testContext()

	// Pre-populate cache
	cached := &domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        18080,
		ContainerID: "container-123",
		Scheme:      "http",
	}
	svc.targets["app.example.com"] = cachedTarget{target: cached}

	// No provider needed - should return from cache
	result, err := svc.GetTarget(ctx, "app.example.com")

	assert.NoError(t, err)
	assert.Equal(t, cached, result)
}

func TestService_GetTarget_CanonicalizesHostForLookup(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	svc := testService(t, configSvc, &stubProvider{backends: map[string]domain.AppBackend{}})
	ctx := testContext()

	result, err := svc.GetTarget(ctx, "App.Example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_RejectsInvalidHostAuthority(t *testing.T) {
	svc := testService(t, nil, nil)
	result, err := svc.GetTarget(testContext(), "app.example.com:8080")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_AppBackend(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	svc := testService(t, configSvc, &stubProvider{backends: map[string]domain.AppBackend{
		"app.example.com": appBackend("127.0.0.1", 18080),
	}})
	ctx := testContext()

	result, err := svc.GetTarget(ctx, "app.example.com")

	assert.NoError(t, err)
	assert.Equal(t, "127.0.0.1", result.Host)
	assert.Equal(t, 18080, result.Port)
	assert.Equal(t, "c-app", result.ContainerID)
	assert.Equal(t, "http", result.Scheme)
	assert.Equal(t, "app.example.com", result.RouteHost)
}

func TestService_GetTarget_AppBackendUnresolved(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	// Zero backend: recorded but unbound (fail closed).
	svc := testService(t, configSvc, &stubProvider{backends: map[string]domain.AppBackend{
		"app.example.com": {ContainerPort: 8080},
	}})
	ctx := testContext()

	result, err := svc.GetTarget(ctx, "app.example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_NoProvider(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	svc := testService(t, configSvc, nil)
	ctx := testContext()

	result, err := svc.GetTarget(ctx, "app.example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_NoStaleAppBackendAfterReplacement(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{})
	provider := &stubProvider{backends: map[string]domain.AppBackend{
		"app.example.com": appBackend("127.0.0.1", 32768),
	}}
	svc := testService(t, configSvc, provider)
	ctx := testContext()

	// First resolution observes the v1 bind.
	result, err := svc.GetTarget(ctx, "app.example.com")
	assert.NoError(t, err)
	assert.Equal(t, 32768, result.Port)

	// Deploy replacement: the index now records the v2 bind. No
	// invalidation call happens; the next lookup must observe v2.
	provider.backends["app.example.com"] = appBackend("127.0.0.1", 32769)
	result, err = svc.GetTarget(ctx, "app.example.com")
	assert.NoError(t, err)
	assert.Equal(t, 32769, result.Port)

	// App resolutions never populate the target cache.
	svc.mu.RLock()
	_, exists := svc.targets["app.example.com"]
	svc.mu.RUnlock()
	assert.False(t, exists)

	// Withdrawal fails closed: no stale-target fallback.
	delete(provider.backends, "app.example.com")
	result, err = svc.GetTarget(ctx, "app.example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, result)
}

func TestService_GetTarget_ExternalRoute(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	svc := testService(t, configSvc, nil)
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
	configSvc := inmocks.NewMockConfigService(t)
	svc := testService(t, configSvc, nil)
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
	configSvc := inmocks.NewMockConfigService(t)
	svc := testService(t, configSvc, nil)
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
			svc.targets = make(map[string]cachedTarget)

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
	configSvc := inmocks.NewMockConfigService(t)
	svc := testService(t, configSvc, nil)
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
			configSvc := inmocks.NewMockConfigService(t)
			svc := testService(t, configSvc, nil)
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
	svc := testService(t, nil, nil)
	ctx := testContext()

	target := &domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        18080,
		ContainerID: "container-123",
		Scheme:      "http",
	}

	err := svc.RegisterTarget(ctx, "App.Example.com", target)

	assert.NoError(t, err)

	// Verify target is cached under the canonical domain.
	svc.mu.RLock()
	cached := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.Equal(t, target, cached.target)
}

func TestService_UnregisterTarget(t *testing.T) {
	svc := testService(t, nil, nil)
	ctx := testContext()

	// Pre-populate
	svc.targets["app.example.com"] = cachedTarget{target: &domain.ProxyTarget{
		Host: "127.0.0.1",
		Port: 18080,
	}}

	err := svc.UnregisterTarget(ctx, "App.Example.com")

	assert.NoError(t, err)

	// Verify target is removed
	svc.mu.RLock()
	_, exists := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.False(t, exists)
}

func TestService_RefreshTargets(t *testing.T) {
	svc := testService(t, nil, nil)
	ctx := testContext()

	// Pre-populate with some targets
	svc.targets["app1.example.com"] = cachedTarget{target: &domain.ProxyTarget{Host: "127.0.0.1"}}
	svc.targets["app2.example.com"] = cachedTarget{target: &domain.ProxyTarget{Host: "127.0.0.1"}}

	err := svc.RefreshTargets(ctx)

	assert.NoError(t, err)

	// Verify all targets are cleared
	svc.mu.RLock()
	count := len(svc.targets)
	svc.mu.RUnlock()

	assert.Equal(t, 0, count)
}

func TestService_UpdateConfig(t *testing.T) {
	svc := testService(t, nil, nil)
	svc.UpdateConfig(Config{
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

func TestService_InvalidateTarget(t *testing.T) {
	svc := testService(t, nil, nil)
	ctx := testContext()

	// Pre-populate cache with target
	svc.targets["app.example.com"] = cachedTarget{target: &domain.ProxyTarget{
		Host:        "127.0.0.1",
		Port:        18080,
		ContainerID: "old-container",
	}}

	// Invalidate the target using mixed-case input.
	svc.InvalidateTarget(ctx, "App.Example.com")

	// Verify target is removed from cache
	svc.mu.RLock()
	_, exists := svc.targets["app.example.com"]
	svc.mu.RUnlock()

	assert.False(t, exists, "target should be removed from cache after invalidation")
}

func TestService_IsKnownHost_AppBackend(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	svc := testService(t, configSvc, &stubProvider{backends: map[string]domain.AppBackend{
		"app.example.com": appBackend("127.0.0.1", 18080),
	}})
	ctx := testContext()

	assert.True(t, svc.IsKnownHost(ctx, "app.example.com"))
	assert.False(t, svc.IsKnownHost(ctx, "unknown.example.com"))
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
	svc := testService(t, nil, nil)
	svc.UpdateConfig(Config{
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
	svc := testService(t, nil, nil)
	svc.UpdateConfig(Config{
		RegistryDomain: "registry.example.com",
	})

	assert.True(t, svc.IsRegistryDomain("registry.example.com"))
	assert.False(t, svc.IsRegistryDomain("other.example.com"))
	assert.False(t, svc.IsRegistryDomain(""))
}

func TestService_IsRegistryDomain_EmptyConfig(t *testing.T) {
	svc := testService(t, nil, nil)
	svc.UpdateConfig(Config{})

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
