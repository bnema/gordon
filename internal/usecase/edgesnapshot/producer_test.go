package edgesnapshot

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type producerSubscriber struct {
	snapshots chan domain.RuntimeActualStateSnapshot
}

func (s producerSubscriber) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	return s.snapshots, nil
}

func TestProducerPublishesSanitizedAttachedRoutesAndLatestUpdate(t *testing.T) {
	snapshots := make(chan domain.RuntimeActualStateSnapshot, 2)
	hub := NewSnapshotHub()
	producer, err := NewProducer(producerSubscriber{snapshots}, hub, ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)

	first := producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	first.Containers = []domain.RuntimeContainerState{{Name: "private-container", Labels: map[string]string{"not-safe": "secret"}}}
	// An invalid full state must not be trusted, even though the producer never
	// forwards its container metadata.
	snapshots <- first
	snapshots <- producerRuntimeSnapshot(2, "app.example.com", "gordon-target-app-example-com")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, producer.Start(ctx))

	current, err := hub.Current(context.Background())
	require.NoError(t, err)
	require.Len(t, current.Entries, 1)
	assert.Equal(t, domain.RouteTargetGeneration(2), current.Generation)
	assert.Equal(t, "gordon-target-app-example-com", current.Entries[0].TargetHost)
	assert.NotContains(t, string(current.Entries[0].TargetKey), "private-container")
	assert.Empty(t, current.Entries[0].UpstreamHost)

	snapshots <- producerRuntimeSnapshot(3, "app.example.com", "gordon-target-app-example-com")
	require.Eventually(t, func() bool {
		snapshot, getErr := hub.Current(context.Background())
		return getErr == nil && snapshot.Generation == 3
	}, time.Second, time.Millisecond)
}

func TestProducerFailsClosedWhenAttachmentDoesNotMatchControlContract(t *testing.T) {
	snapshots := make(chan domain.RuntimeActualStateSnapshot, 1)
	hub := NewSnapshotHub()
	producer, err := NewProducer(producerSubscriber{snapshots}, hub, ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)

	snapshot := producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	snapshot.EdgeAttachments[0].EdgeAlias = "other-edge"
	snapshots <- snapshot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, producer.Start(ctx))

	current, err := hub.Current(context.Background())
	require.NoError(t, err)
	require.Len(t, current.Entries, 1)
	assert.True(t, current.Entries[0].Unavailable())
	assert.Equal(t, domain.RouteTargetUnavailableReasonNoTarget, current.Entries[0].UnavailableReason)
}

func TestProducerPublishesResolvedExternalRouteWithRuntimeGeneration(t *testing.T) {
	external, err := domain.NewExternalReadyRouteTargetEntry("external.example.com", "198.51.100.10", "upstream.example.com", 8443, "http", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	snapshots := make(chan domain.RuntimeActualStateSnapshot, 1)
	snapshots <- producerRuntimeSnapshot(7, "app.example.com", "gordon-target-app-example-com")
	hub := NewSnapshotHub()
	producer, err := NewProducer(producerSubscriber{snapshots}, hub, ProducerOptions{EdgeAlias: "gordon-edge", External: []domain.RouteTargetEntry{external}})
	require.NoError(t, err)
	require.NoError(t, producer.Start(t.Context()))

	snapshot, err := hub.Current(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Entries, 2)
	assert.Equal(t, domain.RouteTargetGeneration(7), snapshot.Entries[0].Generation)
	assert.Equal(t, domain.RouteTargetGeneration(7), snapshot.Entries[1].Generation)
	assert.Equal(t, "upstream.example.com", snapshot.Entries[1].UpstreamHost)
	assert.NotContains(t, fmt.Sprintf("%#v", snapshot), "private-container")
}

func TestProducerRejectsExternalConflictWithManagedRoute(t *testing.T) {
	external, err := domain.NewExternalReadyRouteTargetEntry("app.example.com", "198.51.100.10", "upstream.example.com", 8443, "http", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	snapshots := make(chan domain.RuntimeActualStateSnapshot, 1)
	snapshots <- producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	producer, err := NewProducer(producerSubscriber{snapshots}, NewSnapshotHub(), ProducerOptions{EdgeAlias: "gordon-edge", External: []domain.RouteTargetEntry{external}})
	require.NoError(t, err)
	assert.ErrorContains(t, producer.Start(t.Context()), "external target duplicates route domain")
}

func TestProducerRejectsUnsafeRegistryAlias(t *testing.T) {
	_, err := NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, NewSnapshotHub(), ProducerOptions{
		EdgeAlias: "gordon-edge",
		Registry:  &RegistryTarget{Domain: "registry.example.com", Alias: "127.0.0.1", Port: 5000},
	})
	require.Error(t, err)
}

type scriptedProducerSubscriber struct {
	mu       sync.Mutex
	channels []<-chan domain.RuntimeActualStateSnapshot
	calls    int
}

func (s *scriptedProducerSubscriber) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.channels) == 0 {
		return closedRuntimeStateChannel(), nil
	}
	channel := s.channels[0]
	s.channels = s.channels[1:]
	return channel, nil
}

func closedRuntimeStateChannel() <-chan domain.RuntimeActualStateSnapshot {
	channel := make(chan domain.RuntimeActualStateSnapshot)
	close(channel)
	return channel
}

type producerSubscriptionFunc func(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error)

func (f producerSubscriptionFunc) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	return f(ctx)
}

func TestProducerReturnsNonRetryableInitialSourceError(t *testing.T) {
	sourceErr := &out.RuntimeStateSubscriptionError{Err: fmt.Errorf("secret transport detail")}
	producer, err := NewProducer(producerSubscriptionFunc(func(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
		return nil, sourceErr
	}), NewSnapshotHub(), ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)
	producer.retryWait = func(context.Context, time.Duration) error {
		t.Fatal("non-retryable initial source error must not wait")
		return nil
	}

	err = producer.Start(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, sourceErr)
	assert.NotContains(t, err.Error(), "secret transport detail")
}

func TestProducerRetriesTransientInitialSourceError(t *testing.T) {
	valid := make(chan domain.RuntimeActualStateSnapshot, 1)
	valid <- producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	calls := 0
	producer, err := NewProducer(producerSubscriptionFunc(func(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
		calls++
		if calls == 1 {
			return nil, &out.RuntimeStateSubscriptionError{Retryable: true}
		}
		return valid, nil
	}), NewSnapshotHub(), ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)
	var delays []time.Duration
	producer.retryWait = func(context.Context, time.Duration) error {
		delays = append(delays, producerRetryBackoff)
		return nil
	}

	require.NoError(t, producer.Start(t.Context()))
	assert.Equal(t, 2, calls)
	assert.Equal(t, []time.Duration{producerRetryBackoff}, delays)
}

func TestProducerClosedSubscriptionsBackOffUntilValidUpdate(t *testing.T) {
	initial := make(chan domain.RuntimeActualStateSnapshot, 1)
	initial <- producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	close(initial)
	invalid := make(chan domain.RuntimeActualStateSnapshot, 1)
	invalid <- domain.RuntimeActualStateSnapshot{}
	close(invalid)
	valid := make(chan domain.RuntimeActualStateSnapshot, 1)
	valid <- producerRuntimeSnapshot(2, "app.example.com", "gordon-target-app-example-com")
	close(valid)
	subscriber := &scriptedProducerSubscriber{channels: []<-chan domain.RuntimeActualStateSnapshot{initial, invalid, valid}}
	producer, err := NewProducer(subscriber, NewSnapshotHub(), ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var delays []time.Duration
	retryExited := make(chan struct{})
	producer.retryWait = func(ctx context.Context, delay time.Duration) error {
		mu.Lock()
		delays = append(delays, delay)
		count := len(delays)
		mu.Unlock()
		if count < 3 {
			return nil
		}
		<-ctx.Done()
		close(retryExited)
		return ctx.Err()
	}
	require.NoError(t, producer.Start(ctx))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(delays) == 3
	}, time.Second, time.Millisecond)
	mu.Lock()
	assert.Equal(t, []time.Duration{producerRetryBackoff, 2 * producerRetryBackoff, producerRetryBackoff}, delays)
	mu.Unlock()
	cancel()
	select {
	case <-retryExited:
	case <-time.After(time.Second):
		t.Fatal("producer leaked while waiting to retry")
	}
}

func TestProducerRepeatedClosedSubscriptionsUseBoundedBackoff(t *testing.T) {
	initial := make(chan domain.RuntimeActualStateSnapshot, 1)
	initial <- producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	close(initial)
	subscriber := &scriptedProducerSubscriber{channels: []<-chan domain.RuntimeActualStateSnapshot{initial, closedRuntimeStateChannel(), closedRuntimeStateChannel()}}
	producer, err := NewProducer(subscriber, NewSnapshotHub(), ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var delays []time.Duration
	retryExited := make(chan struct{})
	producer.retryWait = func(ctx context.Context, delay time.Duration) error {
		mu.Lock()
		delays = append(delays, delay)
		count := len(delays)
		mu.Unlock()
		if count < 3 {
			return nil
		}
		<-ctx.Done()
		close(retryExited)
		return ctx.Err()
	}
	require.NoError(t, producer.Start(ctx))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(delays) == 3
	}, time.Second, time.Millisecond)
	mu.Lock()
	assert.Equal(t, []time.Duration{producerRetryBackoff, 2 * producerRetryBackoff, 4 * producerRetryBackoff}, delays)
	mu.Unlock()
	cancel()
	select {
	case <-retryExited:
	case <-time.After(time.Second):
		t.Fatal("producer leaked while waiting to retry")
	}
}

func TestProducerDerivesCompleteManagedStateWithoutLeakingRuntimeMetadata(t *testing.T) {
	external, err := domain.NewExternalReadyRouteTargetEntry("external.example.com", "198.51.100.10", "upstream.example.com", 8443, "https", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	hub := NewSnapshotHub()
	producer, err := NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, hub, ProducerOptions{
		EdgeAlias: "gordon-edge",
		Registry:  &RegistryTarget{Domain: "registry.example.com", Alias: "gordon-registry", Port: 5000},
		External:  []domain.RouteTargetEntry{external},
	})
	require.NoError(t, err)

	runtime := producerRuntimeSnapshot(7, "app.example.com", "gordon-target-app-example-com")
	runtime.Routes[0].BackingContainerName = "private-container-identity"
	runtime.Routes[0].RouteVersion = "private-route-version"
	runtime.Containers = []domain.RuntimeContainerState{{
		Name: "private-container-identity", Status: domain.ContainerStatusRunning,
		Labels: map[string]string{domain.LabelEnvHash: "private-environment-fingerprint"},
	}}
	published, err := producer.publish(runtime)
	require.NoError(t, err)
	require.True(t, published)

	snapshot, err := hub.Current(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Entries, 2)
	managed := snapshot.Entries[0]
	expectedKey, err := domain.ManagedRouteTargetKeyFromRuntimeState(runtime.Routes[0])
	require.NoError(t, err)
	assert.Equal(t, expectedKey, managed.TargetKey)
	assert.Equal(t, "gordon-target-app-example-com", managed.TargetHost)
	assert.Equal(t, domain.RouteTargetAttachmentAttached, managed.Attachment)
	assert.Empty(t, managed.UpstreamHost)
	assert.NotContains(t, fmt.Sprintf("%#v", snapshot), "private-container-identity")
	assert.NotContains(t, fmt.Sprintf("%#v", snapshot), "private-route-version")
	assert.NotContains(t, fmt.Sprintf("%#v", snapshot), "private-environment-fingerprint")

	externalEntry := snapshot.Entries[1]
	assert.Equal(t, "198.51.100.10", externalEntry.TargetHost)
	assert.Equal(t, "upstream.example.com", externalEntry.UpstreamHost)
	assert.Equal(t, domain.RouteTargetAttachmentNotRequired, externalEntry.Attachment)
	proxyTarget, err := externalEntry.ToProxyTarget()
	require.NoError(t, err)
	assert.Equal(t, "upstream.example.com", proxyTarget.OriginalHost)
	require.NotNil(t, snapshot.RegistryForwardingTarget)
	assert.Equal(t, "gordon-registry", snapshot.RegistryForwardingTarget.TargetHost)
	assert.Equal(t, domain.RouteTargetAttachmentAttached, snapshot.RegistryForwardingTarget.Attachment)

	// The WS05 producer consumes this same hub rather than a parallel state path.
	graphs := NewTrafficGraphHub()
	trafficOptions := trafficProducerOptions()
	trafficOptions.ExternalRouteTargets = []domain.RouteTargetEntry{external}
	trafficProducer, err := NewTrafficGraphProducer(hub, graphs, trafficOptions)
	require.NoError(t, err)
	require.NoError(t, trafficProducer.Start(t.Context()))
	graph, err := graphs.CurrentTrafficGraph(t.Context())
	require.NoError(t, err)
	assert.Equal(t, domain.TrafficGraphGeneration(7), graph.Generation)

	unsafeExternal, err := domain.NewExternalReadyRouteTargetEntry("unsafe.example.com", "127.0.0.1", "localhost", 8080, "http", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	_, err = NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, NewSnapshotHub(), ProducerOptions{External: []domain.RouteTargetEntry{unsafeExternal}})
	require.Error(t, err, "control refuses a loopback external target at the split routing boundary")
}

func TestProducerDerivesUnavailableReasonsAndDrainingTargetIdentity(t *testing.T) {
	for _, reason := range []domain.RouteTargetUnavailableReason{
		domain.RouteTargetUnavailableReasonNoTarget,
		domain.RouteTargetUnavailableReasonStarting,
		domain.RouteTargetUnavailableReasonHealthCheckFailed,
	} {
		t.Run(string(reason), func(t *testing.T) {
			hub := NewSnapshotHub()
			producer, err := NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, hub, ProducerOptions{EdgeAlias: "gordon-edge"})
			require.NoError(t, err)
			runtime := producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
			runtime.Routes = []domain.RuntimeRouteState{{Domain: "app.example.com", Generation: 1, Status: domain.RouteTargetStatusUnavailable, UnavailableReason: reason}}
			runtime.EdgeAttachments = nil
			published, err := producer.publish(runtime)
			require.NoError(t, err)
			require.True(t, published)
			snapshot, err := hub.Current(t.Context())
			require.NoError(t, err)
			require.Len(t, snapshot.Entries, 1)
			assert.True(t, snapshot.Entries[0].Unavailable())
			assert.Equal(t, reason, snapshot.Entries[0].UnavailableReason)
		})
	}

	hub := NewSnapshotHub()
	producer, err := NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, hub, ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)
	oldRuntime := producerRuntimeSnapshot(1, "app.example.com", "gordon-target-app-example-com")
	oldRuntime.Routes[0].BackingContainerName = "private-old"
	published, err := producer.publish(oldRuntime)
	require.NoError(t, err)
	require.True(t, published)
	old, err := hub.Current(t.Context())
	require.NoError(t, err)

	drainingRuntime := producerRuntimeSnapshot(2, "app.example.com", "gordon-target-app-example-com")
	drainingRuntime.Routes[0].BackingContainerName = "private-replacement"
	drainingRuntime.Routes[0].Status = domain.RouteTargetStatusDraining
	drainingRuntime.Routes[0].UnavailableReason = domain.RouteTargetUnavailableReasonDraining
	published, err = producer.publish(drainingRuntime)
	require.NoError(t, err)
	require.True(t, published)
	draining, err := hub.Current(t.Context())
	require.NoError(t, err)
	assert.True(t, draining.Entries[0].Draining())
	assert.Equal(t, domain.RouteTargetUnavailableReasonDraining, draining.Entries[0].UnavailableReason)
	assert.NotEqual(t, old.Entries[0].TargetKey, draining.Entries[0].TargetKey)
	assert.NotContains(t, string(draining.Entries[0].TargetKey), "private-replacement")
}

func TestProducerIgnoresStaleRuntimeSnapshotsAndFailsClosedForBadAttachments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.RuntimeActualStateSnapshot)
	}{
		{name: "missing", mutate: func(snapshot *domain.RuntimeActualStateSnapshot) { snapshot.EdgeAttachments = nil }},
		{name: "wrong edge", mutate: func(snapshot *domain.RuntimeActualStateSnapshot) {
			snapshot.EdgeAttachments[0].EdgeAlias = "gordon-other-edge"
		}},
		{name: "stale attachment generation", mutate: func(snapshot *domain.RuntimeActualStateSnapshot) { snapshot.EdgeAttachments[0].Generation-- }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewSnapshotHub()
			producer, err := NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, hub, ProducerOptions{EdgeAlias: "gordon-edge"})
			require.NoError(t, err)
			runtime := producerRuntimeSnapshot(2, "app.example.com", "gordon-target-app-example-com")
			tt.mutate(&runtime)
			published, err := producer.publish(runtime)
			require.NoError(t, err)
			require.True(t, published)
			snapshot, err := hub.Current(t.Context())
			require.NoError(t, err)
			assert.True(t, snapshot.Entries[0].Unavailable())
			assert.Equal(t, domain.RouteTargetUnavailableReasonNoTarget, snapshot.Entries[0].UnavailableReason)
		})
	}

	hub := NewSnapshotHub()
	producer, err := NewProducer(producerSubscriber{make(chan domain.RuntimeActualStateSnapshot)}, hub, ProducerOptions{EdgeAlias: "gordon-edge"})
	require.NoError(t, err)
	newer := producerRuntimeSnapshot(3, "app.example.com", "gordon-target-app-example-com")
	published, err := producer.publish(newer)
	require.NoError(t, err)
	require.True(t, published)
	stale := producerRuntimeSnapshot(2, "stale.example.com", "gordon-target-stale-example-com")
	published, err = producer.publish(stale)
	require.NoError(t, err)
	assert.False(t, published)
	snapshot, err := hub.Current(t.Context())
	require.NoError(t, err)
	assert.Equal(t, domain.RouteTargetGeneration(3), snapshot.Generation)
	assert.Equal(t, "app.example.com", snapshot.Entries[0].CanonicalDomain)
}

func producerRuntimeSnapshot(generation uint64, domainName, alias string) domain.RuntimeActualStateSnapshot {
	return domain.RuntimeActualStateSnapshot{
		Generation: generation, StateVersion: "version", SourceComponentID: "runtime-1",
		Routes: []domain.RuntimeRouteState{{
			Domain: domainName, Generation: generation, RouteVersion: "private-version", BackingContainerName: "private-container",
			ContainerAlias: alias, EdgeTargetAlias: alias, TargetPort: 8080, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1, Status: domain.RouteTargetStatusReady,
		}},
		EdgeAttachments: []domain.RuntimeEdgeNetworkAttachmentState{{
			RouteDomain: domainName, NetworkName: "private-network", EdgeAlias: "gordon-edge", RuntimeAlias: alias,
			TargetAlias: alias, TargetPort: 8080, Attached: true, Generation: generation, SourceComponent: "runtime-1",
		}},
	}
}
