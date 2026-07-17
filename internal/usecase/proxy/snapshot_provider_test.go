package proxy

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// TestLocalSnapshotProviderParity characterizes the route discovery contract in
// snapshot form. New cases belong here rather than in Service.GetTarget tests.
func TestLocalSnapshotProviderParity(t *testing.T) {
	ctx := testContext()
	for _, tc := range []struct {
		name     string
		routes   []domain.Route
		external map[string]string
		setup    func(*outmocks.MockContainerRuntime, *inmocks.MockContainerService)
		want     domain.RouteTargetEntry
	}{
		{
			name:     "managed mapped port with proxy label and h2c",
			routes:   []domain.Route{{Domain: "app.example.com", Image: "app:latest"}},
			external: map[string]string{},
			setup: func(runtime *outmocks.MockContainerRuntime, containers *inmocks.MockContainerService) {
				containers.EXPECT().Get(mock.Anything, "app.example.com").Return(&domain.Container{ID: "private-id", Image: "app:latest"}, true)
				runtime.EXPECT().GetImageLabels(mock.Anything, "app:latest").Return(map[string]string{domain.LabelProxyPort: "8080", domain.LabelProxyProtocol: "h2c"}, nil)
				runtime.EXPECT().GetContainerPort(mock.Anything, "private-id", 8080).Return(32080, nil)
			},
			want: domain.RouteTargetEntry{CanonicalDomain: "app.example.com", TargetHost: "localhost", TargetPort: 32080, Scheme: "http", Protocol: domain.RouteTargetProtocolH2C, Status: domain.RouteTargetStatusReady, Attachment: domain.RouteTargetAttachmentAttached},
		},
		{
			name:     "deprecated label then exposed port fallback",
			routes:   []domain.Route{{Domain: "old.example.com", Image: "old:latest"}, {Domain: "plain.example.com", Image: "plain:latest"}},
			external: map[string]string{},
			setup: func(runtime *outmocks.MockContainerRuntime, containers *inmocks.MockContainerService) {
				containers.EXPECT().Get(mock.Anything, "old.example.com").Return(&domain.Container{ID: "old-id", Image: "old:latest"}, true)
				containers.EXPECT().Get(mock.Anything, "plain.example.com").Return(&domain.Container{ID: "plain-id", Image: "plain:latest"}, true)
				runtime.EXPECT().GetImageLabels(mock.Anything, "old:latest").Return(map[string]string{domain.LabelPort: "3000"}, nil)
				runtime.EXPECT().GetContainerPort(mock.Anything, "old-id", 3000).Return(33000, nil)
				runtime.EXPECT().GetImageLabels(mock.Anything, "plain:latest").Return(map[string]string{}, nil)
				runtime.EXPECT().GetImageExposedPorts(mock.Anything, "plain:latest").Return([]int{8080}, nil)
				runtime.EXPECT().GetContainerPort(mock.Anything, "plain-id", 8080).Return(34000, nil)
			},
			want: domain.RouteTargetEntry{},
		},
		{
			name:     "external preserves original host",
			external: map[string]string{"external.example.com": "203.0.113.10:8443"},
			setup:    func(_ *outmocks.MockContainerRuntime, _ *inmocks.MockContainerService) {},
			want:     domain.RouteTargetEntry{CanonicalDomain: "external.example.com", TargetHost: "203.0.113.10", TargetPort: 8443, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1, Status: domain.RouteTargetStatusReady, Attachment: domain.RouteTargetAttachmentNotRequired},
		},
		{
			name:     "missing managed target is unavailable",
			routes:   []domain.Route{{Domain: "missing.example.com", Image: "missing:latest"}},
			external: map[string]string{},
			setup: func(_ *outmocks.MockContainerRuntime, containers *inmocks.MockContainerService) {
				containers.EXPECT().Get(mock.Anything, "missing.example.com").Return(nil, false)
			},
			want: domain.RouteTargetEntry{CanonicalDomain: "missing.example.com", Status: domain.RouteTargetStatusUnavailable, UnavailableReason: domain.RouteTargetUnavailableReasonNoTarget, Attachment: domain.RouteTargetAttachmentUnavailable},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := outmocks.NewMockContainerRuntime(t)
			containers := inmocks.NewMockContainerService(t)
			config := inmocks.NewMockConfigService(t)
			config.EXPECT().GetRoutes(ctx).Return(tc.routes)
			config.EXPECT().GetExternalRoutes().Return(tc.external)
			tc.setup(runtime, containers)

			provider := newHostSnapshotProvider(runtime, containers, config, Config{})
			snapshot, err := provider.CurrentSnapshot(ctx)
			require.NoError(t, err)
			require.NoError(t, snapshot.Validate())
			if tc.want.CanonicalDomain != "" {
				entry := snapshotEntry(t, snapshot, tc.want.CanonicalDomain)
				assert.Equal(t, tc.want.TargetHost, entry.TargetHost)
				assert.Equal(t, tc.want.TargetPort, entry.TargetPort)
				assert.Equal(t, tc.want.Scheme, entry.Scheme)
				assert.Equal(t, tc.want.Protocol, entry.Protocol)
				assert.Equal(t, tc.want.Status, entry.Status)
				assert.Equal(t, tc.want.UnavailableReason, entry.UnavailableReason)
				assert.Equal(t, tc.want.Attachment, entry.Attachment)
				assert.NotContains(t, entry.TargetKey, "private-id")
			}
		})
	}
}

func TestLocalSnapshotProviderAliasRegistryAndSSRFParity(t *testing.T) {
	t.Run("container mode uses stable alias and monolith registry loopback", func(t *testing.T) {
		ctx := testContext()
		runtime := outmocks.NewMockContainerRuntime(t)
		containers := inmocks.NewMockContainerService(t)
		config := inmocks.NewMockConfigService(t)
		config.EXPECT().GetRoutes(ctx).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}})
		config.EXPECT().GetExternalRoutes().Return(map[string]string{})
		containers.EXPECT().Get(mock.Anything, "app.example.com").Return(&domain.Container{ID: "must-not-leak", Image: "app:latest"}, true)
		runtime.EXPECT().GetImageLabels(mock.Anything, "app:latest").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil)
		provider := NewLocalSnapshotProvider(runtime, containers, config, Config{RegistryDomain: "registry.example.com", RegistryPort: 5000})
		provider.inContainer = func() bool { return true }

		snapshot, err := provider.CurrentSnapshot(ctx)
		require.NoError(t, err)
		entry := snapshotEntry(t, snapshot, "app.example.com")
		assert.Equal(t, "gordon-target-app-example-com", entry.TargetHost)
		assert.NotContains(t, entry.TargetHost, "must-not-leak")
		require.NotNil(t, snapshot.RegistryForwardingTarget)
		assert.Equal(t, "registry.example.com", snapshot.RegistryForwardingTarget.CanonicalDomain)
		assert.Equal(t, "localhost", snapshot.RegistryForwardingTarget.TargetHost)
	})

	t.Run("external SSRF protection fails only the affected route closed", func(t *testing.T) {
		ctx := testContext()
		config := inmocks.NewMockConfigService(t)
		config.EXPECT().GetRoutes(ctx).Return(nil)
		config.EXPECT().GetExternalRoutes().Return(map[string]string{"blocked.example.com": "127.0.0.1:8080"})
		provider := NewLocalSnapshotProvider(nil, nil, config, Config{})
		snapshot, err := provider.CurrentSnapshot(ctx)
		require.NoError(t, err)
		entry := snapshotEntry(t, snapshot, "blocked.example.com")
		assert.Equal(t, domain.RouteTargetStatusUnavailable, entry.Status)
		assert.Equal(t, domain.RouteTargetUnavailableReasonPolicyBlocked, entry.UnavailableReason)

		snapshots := outmocks.NewMockRouteSnapshotProvider(t)
		snapshots.EXPECT().CurrentSnapshot(mock.Anything).Return(snapshot, nil)
		_, err = NewSnapshotService(snapshots, Config{}).GetTarget(ctx, "blocked.example.com")
		require.ErrorIs(t, err, domain.ErrSSRFBlocked)
	})
}

func TestLocalSnapshotProvider_BrokenRouteDoesNotAbortReadyRoute(t *testing.T) {
	ctx := testContext()
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	config.EXPECT().GetRoutes(ctx).Return([]domain.Route{
		{Domain: "broken.example.com", Image: "broken:latest"},
		{Domain: "ready.example.com", Image: "ready:latest"},
	})
	config.EXPECT().GetExternalRoutes().Return(map[string]string{})
	containers.EXPECT().Get(mock.Anything, "broken.example.com").Return(&domain.Container{ID: "private-broken", Image: "broken:latest"}, true)
	containers.EXPECT().Get(mock.Anything, "ready.example.com").Return(&domain.Container{ID: "private-ready", Image: "ready:latest"}, true)
	runtime.EXPECT().GetImageLabels(mock.Anything, "broken:latest").Return(nil, assert.AnError)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "broken:latest").Return(nil, assert.AnError)
	runtime.EXPECT().GetImageLabels(mock.Anything, "ready:latest").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil)
	runtime.EXPECT().GetContainerPort(mock.Anything, "private-ready", 8080).Return(18080, nil)

	snapshot, err := newHostSnapshotProvider(runtime, containers, config, Config{}).CurrentSnapshot(ctx)
	require.NoError(t, err)
	broken := snapshotEntry(t, snapshot, "broken.example.com")
	assert.Equal(t, domain.RouteTargetStatusUnavailable, broken.Status)
	assert.Equal(t, domain.RouteTargetUnavailableReasonDeployment, broken.UnavailableReason)
	assert.Empty(t, broken.TargetHost)
	assert.NotContains(t, fmt.Sprintf("%#v", snapshot), "private-broken")
	require.Equal(t, domain.RouteTargetStatusReady, snapshotEntry(t, snapshot, "ready.example.com").Status)

	snapshots := outmocks.NewMockRouteSnapshotProvider(t)
	snapshots.EXPECT().CurrentSnapshot(mock.Anything).Return(snapshot, nil).Twice()
	svc := NewSnapshotService(snapshots, Config{})
	ready, err := svc.GetTarget(ctx, "ready.example.com")
	require.NoError(t, err)
	assert.Equal(t, "localhost", ready.Host)
	_, err = svc.GetTarget(ctx, "broken.example.com")
	assert.ErrorIs(t, err, domain.ErrNoTargetAvailable)
}

func TestLocalSnapshotProviderDeterministicGenerationAndCloneIsolation(t *testing.T) {
	ctx := testContext()
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	routes := []domain.Route{{Domain: "z.example.com", Image: "z:latest"}, {Domain: "a.example.com", Image: "a:latest"}}
	config.EXPECT().GetRoutes(ctx).Return(routes).Times(3)
	config.EXPECT().GetExternalRoutes().Return(map[string]string{}).Times(3)
	for range 3 {
		containers.EXPECT().Get(mock.Anything, "a.example.com").Return(&domain.Container{ID: "a", Image: "a:latest"}, true)
		containers.EXPECT().Get(mock.Anything, "z.example.com").Return(&domain.Container{ID: "z", Image: "z:latest"}, true)
		runtime.EXPECT().GetImageLabels(mock.Anything, "a:latest").Return(map[string]string{domain.LabelProxyPort: "80"}, nil)
		runtime.EXPECT().GetImageLabels(mock.Anything, "z:latest").Return(map[string]string{domain.LabelProxyPort: "80"}, nil)
	}
	runtime.EXPECT().GetContainerPort(mock.Anything, "a", 80).Return(30001, nil).Twice()
	runtime.EXPECT().GetContainerPort(mock.Anything, "a", 80).Return(30002, nil).Once()
	runtime.EXPECT().GetContainerPort(mock.Anything, "z", 80).Return(30003, nil).Times(3)

	provider := newHostSnapshotProvider(runtime, containers, config, Config{})
	first, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	second, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.Generation, second.Generation)
	assert.Equal(t, first.Entries[0].TargetKey, second.Entries[0].TargetKey)
	assert.Equal(t, []string{"a.example.com", "z.example.com"}, entryDomains(second))

	first.Entries[0].TargetHost = "mutated"
	third, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	assert.True(t, third.Generation.After(second.Generation))
	assert.NotEqual(t, first.Entries[0].TargetKey, third.Entries[0].TargetKey)
	assert.NotEqual(t, "mutated", third.Entries[0].TargetHost)
}

func TestLocalSnapshotProviderStatusChangeIncrementsGeneration(t *testing.T) {
	ctx := testContext()
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	config.EXPECT().GetRoutes(ctx).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}}).Twice()
	config.EXPECT().GetExternalRoutes().Return(map[string]string{}).Twice()
	containers.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false).Once()
	containers.EXPECT().Get(mock.Anything, "app.example.com").Return(&domain.Container{ID: "private", Image: "app:latest"}, true).Once()
	runtime.EXPECT().GetImageLabels(mock.Anything, "app:latest").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil)
	runtime.EXPECT().GetContainerPort(mock.Anything, "private", 8080).Return(32080, nil)
	provider := newHostSnapshotProvider(runtime, containers, config, Config{})

	unavailable, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	ready, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.RouteTargetStatusUnavailable, unavailable.Entries[0].Status)
	assert.Equal(t, domain.RouteTargetStatusReady, ready.Entries[0].Status)
	assert.True(t, ready.Generation.After(unavailable.Generation))
}

func TestLocalSnapshotProviderContextAndConcurrentCalls(t *testing.T) {
	cancelled, cancel := context.WithCancel(testContext())
	cancel()
	provider := NewLocalSnapshotProvider(nil, nil, nil, Config{})
	_, err := provider.CurrentSnapshot(cancelled)
	require.ErrorIs(t, err, context.Canceled)

	// A provider serializes refreshes and returns independent snapshots to callers.
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	config.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}}).Maybe()
	config.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	containers.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false).Maybe()
	provider = newHostSnapshotProvider(runtime, containers, config, Config{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { _, _ = provider.CurrentSnapshot(testContext()) })
	}
	wg.Wait()
}

func TestLocalSnapshotProvider_RetainsDrainingTargetKeyAcrossReplacement(t *testing.T) {
	ctx := testContext()
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	var phase atomic.Int32
	config.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}}).Maybe()
	config.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	containers.EXPECT().Get(mock.Anything, "app.example.com").RunAndReturn(func(context.Context, string) (*domain.Container, bool) {
		switch phase.Load() {
		case 1:
			return &domain.Container{ID: "replacement-real-container-id", Image: "app:latest"}, true
		case 2:
			return &domain.Container{ID: "next-real-container-id", Image: "app:latest"}, true
		default:
			return &domain.Container{ID: "old-real-container-id", Image: "app:latest"}, true
		}
	}).Maybe()
	runtime.EXPECT().GetImageLabels(mock.Anything, "app:latest").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil).Maybe()
	runtime.EXPECT().GetContainerPort(mock.Anything, mock.Anything, 8080).RunAndReturn(func(_ context.Context, containerID string, _ int) (int, error) {
		switch containerID {
		case "replacement-real-container-id":
			return 18081, nil
		case "next-real-container-id":
			return 18082, nil
		default:
			return 18080, nil
		}
	}).Maybe()
	provider := newHostSnapshotProvider(runtime, containers, config, Config{})

	oldSnapshot, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	oldKey := snapshotEntry(t, oldSnapshot, "app.example.com").TargetKey
	require.NotEmpty(t, oldKey)
	phase.Store(1)
	newSnapshot, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	newKey := snapshotEntry(t, newSnapshot, "app.example.com").TargetKey
	require.NotEqual(t, oldKey, newKey)
	assert.NotContains(t, fmt.Sprintf("%#v", newSnapshot), "old-real-container-id")
	assert.NotContains(t, fmt.Sprintf("%#v", newSnapshot), "replacement-real-container-id")

	mappedOldKey, found := provider.TargetKeyForContainer("old-real-container-id")
	assert.True(t, found)
	assert.Equal(t, oldKey, mappedOldKey)
	mappedNewKey, found := provider.TargetKeyForContainer("replacement-real-container-id")
	assert.True(t, found)
	assert.Equal(t, newKey, mappedNewKey)

	// A subsequent activation bounds abandoned associations to the previous
	// lifecycle generation; normal per-domain deployment serialization means a
	// live drain always releases before this can occur.
	phase.Store(2)
	_, err = provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	_, found = provider.TargetKeyForContainer("old-real-container-id")
	assert.False(t, found)
	_, found = provider.TargetKeyForContainer("replacement-real-container-id")
	assert.True(t, found)
	_, found = provider.TargetKeyForContainer("next-real-container-id")
	assert.True(t, found)

	service := NewSnapshotService(nil, Config{})
	timedOutRelease := service.TrackInFlight(string(newKey))
	waiter := NewLocalSnapshotDrainWaiter(provider, service)
	assert.False(t, waiter.WaitForNoInFlight(ctx, "replacement-real-container-id", time.Nanosecond))
	timedOutRelease()
	_, found = provider.TargetKeyForContainer("replacement-real-container-id")
	assert.False(t, found, "timed out drain must release the retired association")
	_, found = provider.TargetKeyForContainer("next-real-container-id")
	assert.True(t, found, "releasing an old association must preserve the current replacement")
}

func TestLocalSnapshotProvider_ConcurrentRefreshAndDrainLookup(t *testing.T) {
	ctx := testContext()
	runtime := outmocks.NewMockContainerRuntime(t)
	containers := inmocks.NewMockContainerService(t)
	config := inmocks.NewMockConfigService(t)
	var replacement atomic.Bool
	config.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}}).Maybe()
	config.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	containers.EXPECT().Get(mock.Anything, "app.example.com").RunAndReturn(func(context.Context, string) (*domain.Container, bool) {
		if replacement.Load() {
			return &domain.Container{ID: "replacement-id", Image: "app:latest"}, true
		}
		return &domain.Container{ID: "old-id", Image: "app:latest"}, true
	}).Maybe()
	runtime.EXPECT().GetImageLabels(mock.Anything, "app:latest").Return(map[string]string{domain.LabelProxyPort: "8080"}, nil).Maybe()
	runtime.EXPECT().GetContainerPort(mock.Anything, mock.Anything, 8080).RunAndReturn(func(_ context.Context, id string, _ int) (int, error) {
		if id == "replacement-id" {
			return 18081, nil
		}
		return 18080, nil
	}).Maybe()
	provider := newHostSnapshotProvider(runtime, containers, config, Config{})
	oldSnapshot, err := provider.CurrentSnapshot(ctx)
	require.NoError(t, err)
	oldKey := snapshotEntry(t, oldSnapshot, "app.example.com").TargetKey
	service := NewSnapshotService(nil, Config{})
	release := service.TrackInFlight(string(oldKey))
	waiter := NewLocalSnapshotDrainWaiter(provider, service)
	replacement.Store(true)

	// Refresh before the delayed lifecycle drain starts: this is the original
	// replacement race. The old association must still resolve below.
	_, err = provider.CurrentSnapshot(ctx)
	require.NoError(t, err)

	waiting := make(chan struct{}, 1)
	service.waitForNoInFlightWait = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	result := make(chan bool, 1)
	go func() { result <- waiter.WaitForNoInFlight(ctx, "old-id", time.Second) }()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("old drain did not wait for the old opaque target key")
	}

	// Requests may refresh while the old lifecycle drain is in progress.
	var refreshes sync.WaitGroup
	for range 16 {
		refreshes.Go(func() { _, _ = provider.CurrentSnapshot(ctx) })
	}
	refreshes.Wait()
	select {
	case <-result:
		t.Fatal("old drain completed while the old target still had an in-flight request")
	default:
	}
	release()
	require.True(t, <-result)
	_, found := provider.TargetKeyForContainer("old-id")
	assert.False(t, found)
	_, found = provider.TargetKeyForContainer("replacement-id")
	assert.True(t, found)
}

func newHostSnapshotProvider(runtime *outmocks.MockContainerRuntime, containers *inmocks.MockContainerService, config *inmocks.MockConfigService, cfg Config) *LocalSnapshotProvider {
	provider := NewLocalSnapshotProvider(runtime, containers, config, cfg)
	provider.inContainer = func() bool { return false }
	return provider
}

func snapshotEntry(t *testing.T, snapshot domain.RouteTargetSnapshot, domainName string) domain.RouteTargetEntry {
	t.Helper()
	for _, entry := range snapshot.Entries {
		if entry.CanonicalDomain == domainName {
			return entry
		}
	}
	t.Fatalf("entry %q not found", domainName)
	return domain.RouteTargetEntry{}
}

func entryDomains(snapshot domain.RouteTargetSnapshot) []string {
	domains := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		domains = append(domains, entry.CanonicalDomain)
	}
	sort.Strings(domains)
	return domains
}
