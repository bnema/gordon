package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
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

func TestDrainCoordinatorRetriesFailedRelayBeforeTerminal(t *testing.T) {
	hub := NewSnapshotHub()
	relay := &failOnceDrainRelay{}
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{Runtime: relay})
	require.NoError(t, err)
	defer coordinator.Close()
	old, next := managedDrainEntry(t, 1, "relay-old"), managedDrainEntry(t, 2, "relay-new")
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}))
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{next}}))
	report := domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: old.TargetKey}
	require.Error(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", report))
	require.NoError(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", report))
	assert.Equal(t, 2, relay.calls)
	assert.Equal(t, domain.RouteDrainStatusAcknowledged, coordinator.completed[old.TargetKey].status)
}

type failOnceDrainRelay struct{ calls int }

func (r *failOnceDrainRelay) AcknowledgeRouteDrain(context.Context, domain.RouteDrainAck) error {
	r.calls++
	if r.calls == 1 {
		return errors.New("temporary")
	}
	return nil
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

func TestDrainCoordinatorBoundsPendingAndCompletedLedgersOverLimit(t *testing.T) {
	hub := NewSnapshotHub()
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{})
	require.NoError(t, err)
	defer coordinator.Close()

	oldEntries := make([]domain.RouteTargetEntry, 0, maxDrainLedgerEntries+1)
	for index := range maxDrainLedgerEntries + 1 {
		oldEntries = append(oldEntries, managedDrainEntryForDomain(t, 1, fmt.Sprintf("app-%d.example.com", index), fmt.Sprintf("private-%d", index)))
	}
	coordinator.observeTransition(&domain.RouteTargetSnapshot{Generation: 1, Entries: oldEntries}, domain.RouteTargetSnapshot{Generation: 2})
	assert.Len(t, coordinator.pending, maxDrainLedgerEntries, "admission refuses the over-limit active drain")
	assert.Len(t, coordinator.seen, maxDrainLedgerEntries)

	for _, entry := range oldEntries[:maxDrainLedgerEntries] {
		require.NoError(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", domain.RouteDrainState{CanonicalDomain: entry.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: entry.TargetKey}))
	}
	assert.Empty(t, coordinator.pending)
	assert.Len(t, coordinator.completed, maxDrainLedgerEntries)

	extra := oldEntries[maxDrainLedgerEntries]
	coordinator.pending[extra.TargetKey] = &pendingDrain{state: domain.RouteDrainState{CanonicalDomain: extra.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: extra.TargetKey}, status: domain.RouteDrainStatusPending}
	require.NoError(t, coordinator.ReportAuthenticatedDrainState(context.Background(), "gordon-edge", domain.RouteDrainState{CanonicalDomain: extra.CanonicalDomain, TransitionGeneration: 2, OldTargetKey: extra.TargetKey}))
	assert.Len(t, coordinator.completed, maxDrainLedgerEntries, "FIFO terminal ledger prunes after the limit")
	assert.LessOrEqual(t, len(coordinator.seen), maxDrainLedgerEntries)
	_, retained := coordinator.completed[oldEntries[0].TargetKey]
	assert.False(t, retained, "oldest terminal entry is pruned deterministically")
}

func managedDrainEntry(t *testing.T, generation domain.RouteTargetGeneration, backing string) domain.RouteTargetEntry {
	t.Helper()
	return managedDrainEntryForDomain(t, generation, "app.example.com", backing)
}

func managedDrainEntryForDomain(t *testing.T, generation domain.RouteTargetGeneration, domainName, backing string) domain.RouteTargetEntry {
	t.Helper()
	entry, err := domain.NewManagedReadyRouteTargetEntry(domainName, "gordon-target-app-example-com", 8080, "http", domain.RouteTargetProtocolHTTP1, generation, backing)
	require.NoError(t, err)
	return entry
}
