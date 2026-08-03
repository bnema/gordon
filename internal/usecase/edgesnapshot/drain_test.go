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
	coordinator.observeTransition(context.Background(), &domain.RouteTargetSnapshot{Generation: 1, Entries: oldEntries}, domain.RouteTargetSnapshot{Generation: 2})
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

type blockingDrainRuntime struct {
	entered  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func newBlockingDrainRuntime() *blockingDrainRuntime {
	return &blockingDrainRuntime{entered: make(chan struct{}), finished: make(chan struct{})}
}

func (r *blockingDrainRuntime) PrepareRouteDrain(ctx context.Context, _ string, _ domain.RouteTargetGeneration, _ domain.RouteTargetKey) error {
	r.once.Do(func() { close(r.entered) })
	<-ctx.Done()
	close(r.finished)
	return ctx.Err()
}

func (r *blockingDrainRuntime) AcknowledgeRouteDrain(context.Context, domain.RouteDrainAck) error {
	return nil
}

func TestDrainCoordinatorCloseCancelsRegistrationBeforeSnapshotVisibility(t *testing.T) {
	hub := NewSnapshotHub()
	runtime := newBlockingDrainRuntime()
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{Runtime: runtime, RegistrationTimeout: time.Second})
	require.NoError(t, err)
	defer coordinator.Close()

	old, next := managedDrainEntry(t, 1, "old"), managedDrainEntry(t, 2, "next")
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}))
	subscriptionCtx, cancelSubscription := context.WithCancel(context.Background())
	defer cancelSubscription()
	updates, err := hub.Subscribe(subscriptionCtx)
	require.NoError(t, err)
	require.Equal(t, domain.RouteTargetGeneration(1), (<-updates).Generation)

	published := make(chan error, 1)
	go func() {
		published <- hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{next}})
	}()
	select {
	case <-runtime.entered:
	case <-time.After(time.Second):
		t.Fatal("runtime registration was not attempted")
	}
	select {
	case update := <-updates:
		t.Fatalf("snapshot became visible before registration completed: generation %d", update.Generation)
	default:
	}

	currentDone := make(chan domain.RouteTargetSnapshot, 1)
	go func() {
		current, currentErr := hub.Current(context.Background())
		if currentErr == nil {
			currentDone <- current
		}
	}()
	select {
	case current := <-currentDone:
		assert.Equal(t, domain.RouteTargetGeneration(1), current.Generation)
	case <-time.After(time.Second):
		t.Fatal("Current was blocked by runtime registration")
	}
	closeDone := make(chan struct{})
	go func() {
		coordinator.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("coordinator Close waited for registration")
	}
	select {
	case <-runtime.finished:
	case <-time.After(time.Second):
		t.Fatal("registration did not observe coordinator cancellation")
	}
	require.NoError(t, <-published)
	require.Equal(t, domain.RouteTargetGeneration(2), (<-updates).Generation)
	assert.ErrorIs(t, coordinator.Timeout(context.Background(), old.TargetKey), ErrDrainUnknown)
}

func TestDrainCoordinatorRegistrationTimeoutPublishesWithoutPendingDrain(t *testing.T) {
	hub := NewSnapshotHub()
	runtime := newBlockingDrainRuntime()
	coordinator, err := NewDrainCoordinator(hub, DrainCoordinatorOptions{Runtime: runtime, RegistrationTimeout: 10 * time.Millisecond})
	require.NoError(t, err)
	defer coordinator.Close()
	old, next := managedDrainEntry(t, 1, "old"), managedDrainEntry(t, 2, "next")
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{old}}))

	started := time.Now()
	require.NoError(t, hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{next}}))
	assert.Less(t, time.Since(started), time.Second)
	select {
	case <-runtime.finished:
	case <-time.After(time.Second):
		t.Fatal("registration did not respect its timeout")
	}
	assert.ErrorIs(t, coordinator.Timeout(context.Background(), old.TargetKey), ErrDrainUnknown)
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
