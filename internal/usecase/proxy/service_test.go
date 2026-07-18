package proxy

import (
	"context"
	"errors"
	"fmt"
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

type recordingEdgeDrainReporter struct {
	mu       sync.Mutex
	states   []domain.RouteDrainState
	failures int
}

func (r *recordingEdgeDrainReporter) ReportDrainState(_ context.Context, state domain.RouteDrainState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
	if r.failures > 0 {
		r.failures--
		return errors.New("temporary control failure")
	}
	return nil
}

func (r *recordingEdgeDrainReporter) snapshot() []domain.RouteDrainState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.RouteDrainState(nil), r.states...)
}

func TestService_AcquireTargetRegistersBeforeSnapshotTransitionCanReportZero(t *testing.T) {
	old := readyEntry(t, "app.example.com", "old.internal", 1)
	newTarget := readyEntry(t, "app.example.com", "new.internal", 2)
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 1, old), nil).Once()
	reporter := &recordingEdgeDrainReporter{}
	svc := NewSnapshotService(provider, Config{}, reporter)
	defer svc.Close()

	selected := make(chan struct{})
	continueAcquire := make(chan struct{})
	svc.acquireTargetSelected = func() {
		close(selected)
		<-continueAcquire
	}
	acquired := make(chan func(), 1)
	go func() {
		_, release, err := svc.AcquireTarget(testContext(), "app.example.com")
		if err != nil {
			t.Errorf("acquire target: %v", err)
			return
		}
		acquired <- release
	}()
	select {
	case <-selected:
	case <-time.After(time.Second):
		t.Fatal("request did not pause after target selection")
	}

	transitioned := make(chan struct{})
	go func() {
		svc.ObserveAcceptedRouteSnapshot(&domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}, routeSnapshot(t, 2, newTarget))
		close(transitioned)
	}()
	select {
	case <-transitioned:
		t.Fatal("transition reported before selected request was registered")
	default:
	}

	close(continueAcquire)
	var release func()
	select {
	case release = <-acquired:
	case <-time.After(time.Second):
		t.Fatal("request was not acquired")
	}
	select {
	case <-transitioned:
	case <-time.After(time.Second):
		t.Fatal("snapshot transition did not complete")
	}
	assert.Empty(t, reporter.snapshot(), "registered request prevents a zero-in-flight report")
	release()
	waitForProxy(t, func() bool { return len(reporter.snapshot()) == 1 })
}

func TestService_EdgeSnapshotTransitionRoutesNewTargetAndReportsOldAtZero(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	old := readyEntry(t, "app.example.com", "old.internal", 1)
	newTarget := readyEntry(t, "app.example.com", "new.internal", 2)
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 1, old), nil).Once()
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(routeSnapshot(t, 2, newTarget), nil).Once()
	reporter := &recordingEdgeDrainReporter{}
	svc := NewSnapshotService(provider, Config{}, reporter)
	defer svc.Close()

	oldTarget, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	releaseOld := svc.TrackInFlight(string(old.TargetKey))
	svc.ObserveAcceptedRouteSnapshot(&domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}, routeSnapshot(t, 2, newTarget))

	freshTarget, err := svc.GetTarget(testContext(), "app.example.com")
	require.NoError(t, err)
	assert.Equal(t, "old.internal", oldTarget.Host)
	assert.Equal(t, "new.internal", freshTarget.Host, "fresh traffic must use the new target")
	assert.Empty(t, reporter.snapshot(), "old target remains pending while it has traffic")
	releaseOld()
	waitForProxy(t, func() bool { return len(reporter.snapshot()) == 1 })
	state := reporter.snapshot()[0]
	assert.Equal(t, old.TargetKey, state.OldTargetKey)
	assert.Equal(t, domain.RouteTargetGeneration(2), state.TransitionGeneration)
	assert.Zero(t, state.InFlight)
	assert.Equal(t, domain.RouteDrainTimeoutReasonNone, state.TimeoutReason)
}

func TestService_EdgeSnapshotTransitionReportsZeroOnceAndIgnoresEqualReconnect(t *testing.T) {
	old := readyEntry(t, "app.example.com", "old.internal", 1)
	newTarget := readyEntry(t, "app.example.com", "new.internal", 2)
	reporter := &recordingEdgeDrainReporter{}
	svc := NewSnapshotService(nil, Config{}, reporter)
	defer svc.Close()
	previous := &domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}
	current := routeSnapshot(t, 2, newTarget)

	svc.ObserveAcceptedRouteSnapshot(previous, current)
	waitForProxy(t, func() bool { return len(reporter.snapshot()) == 1 })
	svc.ObserveAcceptedRouteSnapshot(previous, current)
	time.Sleep(20 * time.Millisecond)
	assert.Len(t, reporter.snapshot(), 1, "equal reconnect must not create another logical report")
}

func TestService_EdgeSnapshotTransitionTimeoutRetriesAndIsolatesRegistry(t *testing.T) {
	old := readyEntry(t, "app.example.com", "old.internal", 1)
	newTarget := readyEntry(t, "app.example.com", "new.internal", 2)
	oldRegistry := readyEntry(t, "registry.example.com", "registry-old", 1)
	newRegistry := readyEntry(t, "registry.example.com", "registry-new", 2)
	reporter := &recordingEdgeDrainReporter{failures: 2}
	svc := NewSnapshotService(nil, Config{EdgeDrainTimeout: 15 * time.Millisecond}, reporter)
	defer svc.Close()
	releaseOld := svc.TrackInFlight(string(old.TargetKey))
	defer releaseOld()
	// New target and registry work must not change the retired app key's count.
	releaseNew := svc.TrackInFlight(string(newTarget.TargetKey))
	defer releaseNew()
	svc.TrackRegistryRequest()
	defer svc.ReleaseRegistryRequest()
	previous := &domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}, RegistryForwardingTarget: &oldRegistry}
	current := routeSnapshot(t, 2, newTarget)
	current.RegistryForwardingTarget = &newRegistry
	svc.ObserveAcceptedRouteSnapshot(previous, current)

	waitForProxy(t, func() bool { return len(reporter.snapshot()) >= 3 })
	states := reporter.snapshot()
	assert.Len(t, states, 3, "two bounded retries followed by success")
	for _, state := range states {
		assert.Equal(t, old.TargetKey, state.OldTargetKey)
		assert.Equal(t, uint64(1), state.InFlight)
		assert.Equal(t, domain.RouteDrainTimeoutReasonEdge, state.TimeoutReason)
	}
}

func TestService_EdgeSnapshotTransitionConcurrentCompletionReportsOnce(t *testing.T) {
	old := readyEntry(t, "app.example.com", "old.internal", 1)
	newTarget := readyEntry(t, "app.example.com", "new.internal", 2)
	reporter := &recordingEdgeDrainReporter{}
	svc := NewSnapshotService(nil, Config{}, reporter)
	defer svc.Close()
	const requests = 32
	releases := make([]func(), 0, requests)
	for range requests {
		releases = append(releases, svc.TrackInFlight(string(old.TargetKey)))
	}
	svc.ObserveAcceptedRouteSnapshot(&domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}, routeSnapshot(t, 2, newTarget))
	var wg sync.WaitGroup
	for _, release := range releases {
		wg.Go(release)
	}
	wg.Wait()
	waitForProxy(t, func() bool { return len(reporter.snapshot()) == 1 })
	assert.Len(t, reporter.snapshot(), 1)
}

func waitForProxy(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func TestService_RegistryClassificationComesFromSnapshotDespiteConfigMismatch(t *testing.T) {
	provider := outmocks.NewMockRouteSnapshotProvider(t)
	registry := readyEntry(t, "registry.example.com", "registry.internal", 1)
	snapshot := routeSnapshot(t, 1, readyEntry(t, "app.example.com", "198.51.100.1", 1))
	snapshot.RegistryForwardingTarget = &registry
	provider.EXPECT().CurrentSnapshot(mock.Anything).Return(snapshot, nil).Times(3)
	svc := NewSnapshotService(provider, Config{RegistryDomain: "wrong-registry.example.com"})

	assert.True(t, svc.IsKnownHost(testContext(), "App.Example.com"))
	assert.True(t, svc.IsKnownHost(testContext(), "registry.example.com"))
	target, err := svc.GetTarget(testContext(), "registry.example.com")
	require.NoError(t, err)
	assert.True(t, target.Registry)
	assert.Equal(t, "registry.internal", target.Host)
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

type blockingEdgeDrainReporter struct {
	started chan struct{}
	release <-chan struct{}
}

func (r *blockingEdgeDrainReporter) ReportDrainState(ctx context.Context, _ domain.RouteDrainState) error {
	select {
	case r.started <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestService_EdgeDrainLedgersBoundOverLimit(t *testing.T) {
	t.Run("pending admission cap", func(t *testing.T) {
		release := make(chan struct{})
		reporter := &blockingEdgeDrainReporter{started: make(chan struct{}, maxPendingEdgeDrains), release: release}
		svc := NewSnapshotService(nil, Config{}, reporter)
		defer svc.Close()
		for index := range maxPendingEdgeDrains + 1 {
			entry := readyEntry(t, fmt.Sprintf("app-%d.example.com", index), fmt.Sprintf("old-%d.internal", index), 1)
			svc.startEdgeDrain(entry.CanonicalDomain, 2, entry.TargetKey)
		}
		waitForProxy(t, func() bool { return len(reporter.started) == maxPendingEdgeDrains })
		svc.drainMu.Lock()
		assert.Len(t, svc.pendingDrains, maxPendingEdgeDrains)
		svc.drainMu.Unlock()
		close(release)
		waitForProxy(t, func() bool {
			svc.drainMu.Lock()
			defer svc.drainMu.Unlock()
			return len(svc.pendingDrains) == 0
		})
	})

	t.Run("completed FIFO cap", func(t *testing.T) {
		reporter := &recordingEdgeDrainReporter{}
		svc := NewSnapshotService(nil, Config{}, reporter)
		defer svc.Close()
		var first edgeDrainIdentity
		for index := range maxPendingEdgeDrains + 1 {
			entry := readyEntry(t, fmt.Sprintf("done-%d.example.com", index), fmt.Sprintf("done-%d.internal", index), 1)
			identity := edgeDrainIdentity{domain: entry.CanonicalDomain, generation: 2, key: entry.TargetKey}
			if index == 0 {
				first = identity
			}
			svc.startEdgeDrain(identity.domain, identity.generation, identity.key)
			waitForProxy(t, func() bool { return len(reporter.snapshot()) == index+1 })
		}
		svc.drainMu.Lock()
		defer svc.drainMu.Unlock()
		assert.Empty(t, svc.pendingDrains, "accepted reports are removed from active pending state")
		assert.Len(t, svc.completedDrains, maxPendingEdgeDrains)
		assert.Len(t, svc.completedDrainOrder, maxPendingEdgeDrains)
		_, retained := svc.completedDrains[first]
		assert.False(t, retained, "oldest terminal identity is pruned deterministically")
	})
}
