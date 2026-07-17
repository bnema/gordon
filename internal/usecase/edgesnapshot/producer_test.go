package edgesnapshot

import (
	"context"
	"fmt"
	"testing"
	"time"

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
