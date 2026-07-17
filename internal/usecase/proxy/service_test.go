package proxy

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func routeSnapshot(t *testing.T, generation domain.RouteTargetGeneration, entries ...domain.RouteTargetEntry) domain.RouteTargetSnapshot {
	t.Helper()
	snapshot := domain.RouteTargetSnapshot{Generation: generation, Entries: entries}
	require.NoError(t, snapshot.Validate())
	return snapshot
}

func readyEntry(t *testing.T, domainName, host string, generation domain.RouteTargetGeneration) domain.RouteTargetEntry {
	t.Helper()
	entry, err := domain.NewReadyRouteTargetEntry(domainName, host, 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return entry
}

func drainingEntry(t *testing.T, domainName, host string, generation domain.RouteTargetGeneration) domain.RouteTargetEntry {
	t.Helper()
	entry, err := domain.NewDrainingRouteTargetEntry(domainName, host, 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return entry
}

func unavailableEntry(t *testing.T, domainName string, generation domain.RouteTargetGeneration) domain.RouteTargetEntry {
	t.Helper()
	entry, err := domain.NewUnavailableRouteTargetEntry(domainName, domain.RouteTargetUnavailableReasonNoTarget, generation)
	require.NoError(t, err)
	return entry
}

func TestService_DiscoveryFieldsAreSnapshotOnly(t *testing.T) {
	serviceType := reflect.TypeFor[Service]()
	providerType := reflect.TypeFor[out.RouteSnapshotProvider]()
	forbiddenDiscoveryDependencies := []reflect.Type{
		reflect.TypeFor[out.ContainerRuntime](),
		reflect.TypeFor[in.ContainerService](),
		reflect.TypeFor[in.ConfigService](),
	}

	providerFields := 0
	for index := range serviceType.NumField() {
		field := serviceType.Field(index)
		if field.Name == "snapshotProvider" {
			providerFields++
			assert.Equal(t, providerType, field.Type)
		}
		for _, forbidden := range forbiddenDiscoveryDependencies {
			assert.NotEqual(t, forbidden, field.Type, "service must not retain %s", forbidden)
		}
	}
	assert.Equal(t, 1, providerFields, "service must retain its only discovery dependency as a route snapshot provider")
}

func TestService_GetTarget_SnapshotReadyCanonicalAndCached(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 1, readyEntry(t, "app.example.com", "198.51.100.1", 1)), nil).Once()
	svc := NewSnapshotService(provider, Config{})

	first, err := svc.GetTarget(testContext(), "App.Example.com")
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", first.RouteHost)
	assert.Equal(t, "198.51.100.1", first.Host)

	second, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	assert.Same(t, first, second)
}

func TestService_GetTarget_SnapshotUnavailablePreservesNoTarget(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	snapshot := routeSnapshot(t, 1, unavailableEntry(t, "app.example.com", 1))
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(snapshot, nil).Twice()
	svc := NewSnapshotService(provider, Config{})

	for range 2 {
		target, err := svc.GetTarget(testContext(), "app.example.com")
		assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
		assert.Nil(t, target)
	}
}

func TestService_GetTarget_SnapshotDrainingMapsButDoesNotCache(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	snapshot := routeSnapshot(t, 1, drainingEntry(t, "app.example.com", "198.51.100.1", 1))
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(snapshot, nil).Twice()
	svc := NewSnapshotService(provider, Config{})

	for range 2 {
		target, err := svc.GetTarget(testContext(), "app.example.com")
		require.NoError(t, err)
		assert.Equal(t, "198.51.100.1", target.Host)
	}
}

func TestService_GetTarget_RejectsProviderAndInvalidSnapshot(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		provider := outmocks.NewMockRouteSnapshotProvider(t)
		provider.EXPECT().CurrentSnapshot(mock.Anything).Return(domain.RouteTargetSnapshot{}, errors.New("unavailable"))

		target, err := NewSnapshotService(provider, Config{}).GetTarget(testContext(), "app.example.com")
		assert.ErrorContains(t, err, "get route snapshot")
		assert.Nil(t, target)
	})
	t.Run("invalid snapshot", func(t *testing.T) {
		provider := outmocks.NewMockRouteSnapshotProvider(t)
		provider.EXPECT().CurrentSnapshot(mock.Anything).Return(domain.RouteTargetSnapshot{}, nil)

		target, err := NewSnapshotService(provider, Config{}).GetTarget(testContext(), "app.example.com")
		assert.ErrorContains(t, err, "invalid route snapshot")
		assert.Nil(t, target)
	})
}

func TestService_GetTarget_StaleSnapshotCannotReplaceCurrentGeneration(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 2, readyEntry(t, "app.example.com", "198.51.100.2", 2)), nil).Once()
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 1, readyEntry(t, "app.example.com", "198.51.100.1", 1)), nil).Once()
	svc := NewSnapshotService(provider, Config{})

	target, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.2", target.Host)

	svc.InvalidateTarget(testContext(), "app.example.com")
	target, err = svc.GetTarget(testContext(), "app.example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
	assert.Nil(t, target)
}

func TestService_InvalidateTargetRefreshesFromNewSnapshot(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 1, readyEntry(t, "app.example.com", "198.51.100.1", 1)), nil).Once()
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 2, readyEntry(t, "app.example.com", "198.51.100.2", 2)), nil).Once()
	svc := NewSnapshotService(provider, Config{})

	oldTarget, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	svc.InvalidateTarget(testContext(), "App.Example.com")
	newTarget, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.1", oldTarget.Host)
	assert.Equal(t, "198.51.100.2", newTarget.Host)
}

func TestService_GetTarget_ConcurrentMissSharesSnapshotLookup(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider.EXPECT().CurrentSnapshot(mock.Anything).RunAndReturn(func(context.Context) (domain.RouteTargetSnapshot, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return routeSnapshot(t, 1, readyEntry(t, "app.example.com", "198.51.100.1", 1)), nil
	}).Once()
	svc := NewSnapshotService(provider, Config{})

	const requests = 16
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for range requests {
		wg.Go(func() {
			target, err := svc.GetTarget(testContext(), "app.example.com")
			if err == nil && target.Host != "198.51.100.1" {
				errs <- errors.New("unexpected target")
				return
			}
			errs <- err
		})
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("snapshot lookup did not start")
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestService_IsKnownHostUsesSnapshotAndRegistryConfig(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	registry := readyEntry(t, "registry.example.com", "localhost", 1)
	snapshot := routeSnapshot(t, 1, readyEntry(t, "app.example.com", "198.51.100.1", 1))
	snapshot.RegistryForwardingTarget = &registry
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(snapshot, nil).Once()
	svc := NewSnapshotService(provider, Config{RegistryDomain: "registry.example.com"})

	assert.True(t, svc.IsRegistryDomain("Registry.Example.com"))
	assert.True(t, svc.IsKnownHost(testContext(), "App.Example.com"))
	assert.True(t, svc.IsKnownHost(testContext(), "registry.example.com"))
}

func TestLocalSnapshotDrainWaiter_MapsOldContainerIDToOpaqueInFlightKey(t *testing.T) {
	ctx := testContext()
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	config.EXPECT().GetRoutes(ctx).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}})
	config.EXPECT().GetExternalRoutes().Return(map[string]string{})
	containers.EXPECT().Get(mock.Anything, "app.example.com").Return(&domain.Container{ID: "old-real-container-id", Image: "app:latest"}, true)
	runtime.EXPECT().GetImageLabels(mock.Anything, "app:latest").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil)
	runtime.EXPECT().GetContainerPort(mock.Anything, "old-real-container-id", 8080).Return(18080, nil)
	provider := newHostSnapshotProvider(runtime, containers, config, Config{})
	snapshot, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	key := snapshotEntry(t, snapshot, "app.example.com").TargetKey
	require.NotEmpty(t, key)

	service := NewSnapshotService(nil, Config{})
	release := service.TrackInFlight(string(key))
	waiter := NewLocalSnapshotDrainWaiter(provider, service)

	waiting := make(chan struct{}, 1)
	service.waitForNoInFlightWait = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	result := make(chan bool, 1)
	go func() { result <- waiter.WaitForNoInFlight(context.Background(), "old-real-container-id", time.Second) }()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("drain waiter did not block for the opaque target key")
	}
	select {
	case <-result:
		t.Fatal("drain waiter returned before the request released")
	default:
	}
	release()
	require.True(t, <-result)
	_, found := provider.TargetKeyForContainer("old-real-container-id")
	assert.True(t, found, "releasing a current association must not remove it")
	assert.True(t, waiter.WaitForNoInFlight(context.Background(), "unknown-old-id", time.Nanosecond))
}

func TestService_RegisterUnregisterRefreshAndInFlight(t *testing.T) {
	svc := NewSnapshotService(nil, Config{})
	target := &domain.ProxyTarget{Host: "198.51.100.1", Port: 8080, Scheme: "http"}
	require.NoError(t, svc.RegisterTarget(testContext(), "App.Example.com", target))
	got, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	assert.Same(t, target, got)
	require.NoError(t, svc.UnregisterTarget(testContext(), "app.example.com"))
	_, err = svc.GetTarget(testContext(), "app.example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)

	require.NoError(t, svc.RegisterTarget(testContext(), "app.example.com", target))
	require.NoError(t, svc.RefreshTargets(testContext()))
	assert.Empty(t, svc.targets)

	release := svc.TrackInFlight("container-1")
	assert.False(t, svc.WaitForNoInFlight(testContext(), "container-1", time.Nanosecond))
	release()
	assert.True(t, svc.WaitForNoInFlight(testContext(), "container-1", time.Second))
	svc.TrackRegistryRequest()
	assert.Equal(t, int64(1), svc.RegistryInFlight())
	svc.ReleaseRegistryRequest()
	assert.True(t, svc.DrainRegistryInFlight(time.Second))
}
