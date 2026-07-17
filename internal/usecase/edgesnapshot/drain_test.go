package edgesnapshot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingDrainRelay struct {
	mu   sync.Mutex
	acks []domain.RouteDrainAck
}

func (r *recordingDrainRelay) AcknowledgeRouteDrain(_ context.Context, acknowledgement domain.RouteDrainAck) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acks = append(r.acks, acknowledgement)
	return nil
}

func TestDrainCoordinatorRegistersTransitionAndRelaysOnce(t *testing.T) {
	hub := NewSnapshotHub()
	relay := &recordingDrainRelay{}
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{Runtime: relay, Now: func() time.Time { return time.Unix(50, 0) }})
	require.NoError(t, err)
	defer coordinator.Close()

	old := managedDrainEntry(t, 1, "private-old")
	newTarget := managedDrainEntry(t, 2, "private-new")
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}))
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{newTarget}}))

	report := domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: old.TargetKey}
	require.NoError(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", report))
	require.NoError(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", report))
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.Len(t, relay.acks, 1)
	assert.Equal(t, domain.RouteDrainStatusAcknowledged, relay.acks[0].Status)
	assert.Equal(t, time.Unix(50, 0).UTC(), relay.acks[0].AcknowledgedAt)
	assert.NotEqual(t, "private-old", string(relay.acks[0].OldTargetKey))
}

func TestDrainCoordinatorRejectsUnexpectedStaleAndUnknownReports(t *testing.T) {
	hub := NewSnapshotHub()
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{})
	require.NoError(t, err)
	defer coordinator.Close()
	old := managedDrainEntry(t, 1, "private-old")
	newTarget := managedDrainEntry(t, 2, "private-new")
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}))
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{newTarget}}))

	report := domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: old.TargetKey}
	assert.ErrorIs(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "other-edge", report), ErrDrainUnexpected)
	assert.ErrorIs(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: 1, OldTargetKey: old.TargetKey}), ErrDrainStale)
	unknown, err := domain.NewRouteTargetKey("rtk_abcdefghijklmnopqrstuvwxyz234567")
	require.NoError(t, err)
	assert.ErrorIs(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: unknown}), ErrDrainUnknown)
}

func TestDrainCoordinatorTimeoutRelaysExplicitControlReason(t *testing.T) {
	hub := NewSnapshotHub()
	relay := &recordingDrainRelay{}
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{Runtime: relay})
	require.NoError(t, err)
	defer coordinator.Close()
	old := managedDrainEntry(t, 1, "private-old")
	newTarget := managedDrainEntry(t, 2, "private-new")
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}))
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{newTarget}}))
	require.NoError(t, coordinator.Timeout(context.Background(), old.TargetKey))
	assert.NoError(t, coordinator.Timeout(context.Background(), old.TargetKey))
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.Len(t, relay.acks, 1)
	assert.Equal(t, domain.RouteDrainTimeoutReasonControl, relay.acks[0].TimeoutReason)
}

func managedDrainEntry(t *testing.T, generation domain.RouteTargetGeneration, backing string) domain.RouteTargetEntry {
	t.Helper()
	entry, err := domain.NewManagedReadyRouteTargetEntry("app.example.com", "gordon-target-app-example-com", 8080, "http", domain.RouteTargetProtocolHTTP1, generation, backing)
	require.NoError(t, err)
	return entry
}
