package edgesnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotHubPublishesImmutableMonotonicSnapshots(t *testing.T) {
	hub := NewSnapshotHub()
	first := testSnapshot(t, 1)
	require.NoError(t, hub.Publish(first))

	current, err := hub.Current(context.Background())
	require.NoError(t, err)
	current.Entries[0].TargetHost = "changed.example"
	again, err := hub.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "target.example", again.Entries[0].TargetHost)

	require.Error(t, hub.Publish(first))
	require.Error(t, hub.Publish(domain.RouteTargetSnapshot{}))
	loopback, err := domain.NewReadyRouteTargetEntry("app.example.com", "127.0.0.1", 8080, "http", domain.RouteTargetProtocolHTTP1, 2)
	require.NoError(t, err)
	require.Error(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{loopback}}))
}

func TestSnapshotHubSubscribeImmediatelyAndKeepsLatestForSlowSubscriber(t *testing.T) {
	hub := NewSnapshotHub()
	require.NoError(t, hub.Publish(testSnapshot(t, 1)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := hub.Subscribe(ctx)
	require.NoError(t, err)

	initial := <-updates
	assert.Equal(t, domain.RouteTargetGeneration(1), initial.Generation)
	require.NoError(t, hub.Publish(testSnapshot(t, 2)))
	require.NoError(t, hub.Publish(testSnapshot(t, 3)))

	latest := <-updates
	assert.Equal(t, domain.RouteTargetGeneration(3), latest.Generation)
}

func TestSnapshotHubSubscriptionClosesOnCancellation(t *testing.T) {
	hub := NewSnapshotHub()
	ctx, cancel := context.WithCancel(context.Background())
	updates, err := hub.Subscribe(ctx)
	require.NoError(t, err)
	cancel()

	require.Eventually(t, func() bool {
		_, ok := <-updates
		return !ok
	}, time.Second, 10*time.Millisecond)

	_, err = hub.Current(context.Background())
	assert.ErrorIs(t, err, ErrNoSnapshot)
	_, err = hub.Subscribe(context.Background())
	require.NoError(t, err)
	assert.False(t, errors.Is(err, context.Canceled))
}

func testSnapshot(t *testing.T, generation domain.RouteTargetGeneration) domain.RouteTargetSnapshot {
	t.Helper()
	entry, err := domain.NewReadyRouteTargetEntry("app.example.com", "target.example", 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return domain.RouteTargetSnapshot{Generation: generation, Entries: []domain.RouteTargetEntry{entry}}
}
