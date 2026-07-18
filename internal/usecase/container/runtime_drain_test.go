package container

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeDrainRegistryUsesSnapshotProducerTargetKey(t *testing.T) {
	container := &domain.Container{Name: "private-old-container", Status: string(domain.ContainerStatusRunning), Ports: []int{8080}}
	state, ok := runtimeReadyRouteState(17, "App.Example.com", container)
	require.True(t, ok)
	producerKey, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
	require.NoError(t, err)

	registry := NewRuntimeDrainRegistry(func(id string) (domain.RuntimeRouteState, bool) {
		assert.Equal(t, "old-id", id)
		return state, true
	})
	registry.PrepareDrain("old-id")
	require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, producerKey, 4)))
	assert.True(t, registry.WaitForNoInFlight(context.Background(), "old-id", time.Second))
}

func TestRuntimeDrainRegistryAcceptsAckBeforeWaitAndDuplicate(t *testing.T) {
	state, key := testRuntimeDrainState(t)
	registry := testRuntimeDrainRegistry(state)
	registry.PrepareDrain("old-id")
	ack := cleanRuntimeDrainAck(t, key, 7)
	require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), ack))
	require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), ack), "duplicate control relay is idempotent")
	assert.True(t, registry.WaitForNoInFlight(context.Background(), "old-id", time.Second))
}

func TestRuntimeDrainRegistryWaitBeforeAckRejectsWrongAndStale(t *testing.T) {
	state, key := testRuntimeDrainState(t)
	registry := testRuntimeDrainRegistry(state)
	registry.PrepareDrain("old-id")

	result := make(chan bool, 1)
	go func() { result <- registry.WaitForNoInFlight(context.Background(), "old-id", time.Second) }()

	wrongKey, err := domain.NewRouteTargetKey("rtk_abcdefghijklmnopqrstuvwxyz234567")
	require.NoError(t, err)
	assert.Error(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, wrongKey, 3)))
	select {
	case <-result:
		t.Fatal("wrong acknowledgement released drain")
	case <-time.After(20 * time.Millisecond):
	}

	ack := cleanRuntimeDrainAck(t, key, 3)
	require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), ack))
	assert.True(t, <-result)
	stale := cleanRuntimeDrainAck(t, key, 2)
	assert.Error(t, registry.AcknowledgeRouteDrain(context.Background(), stale), "older transition cannot be replayed")
}

func TestRuntimeDrainRegistryCancelRejectsOldAcknowledgementsUntilNewRegistration(t *testing.T) {
	for _, registrationBeforeReprepare := range []bool{false, true} {
		t.Run(fmt.Sprintf("registration-before-reprepare=%t", registrationBeforeReprepare), func(t *testing.T) {
			state, key := testRuntimeDrainState(t)
			registry := testRuntimeDrainRegistry(state)
			require.True(t, registry.PrepareDrain("old-id"))
			require.NoError(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 7, key))
			registry.CancelDrain("old-id")

			oldAck := cleanRuntimeDrainAck(t, key, 7)
			if registrationBeforeReprepare {
				assert.ErrorIs(t, registry.AcknowledgeRouteDrain(context.Background(), oldAck), ErrRuntimeDrainStale)
				require.NoError(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 8, key))
				require.True(t, registry.PrepareDrain("old-id"))
			} else {
				require.True(t, registry.PrepareDrain("old-id"))
				assert.ErrorIs(t, registry.AcknowledgeRouteDrain(context.Background(), oldAck), ErrRuntimeDrainStale)
				require.NoError(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 8, key))
			}
			assert.ErrorIs(t, registry.AcknowledgeRouteDrain(context.Background(), oldAck), ErrRuntimeDrainStale)

			result := make(chan bool, 1)
			go func() { result <- registry.WaitForNoInFlight(context.Background(), "old-id", time.Second) }()
			select {
			case <-result:
				t.Fatal("old acknowledgement released replacement")
			case <-time.After(20 * time.Millisecond):
			}
			require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, key, 8)))
			assert.True(t, <-result)
		})
	}
}

func TestRuntimeDrainRegistryAcceptsGapGenerationOnlyAfterReplacement(t *testing.T) {
	state, key := testRuntimeDrainState(t)
	registry := testRuntimeDrainRegistry(state)

	require.True(t, registry.PrepareDrain("old-id"))
	require.NoError(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 7, key))
	registry.CancelDrain("old-id")
	require.True(t, registry.PrepareDrain("old-id"))

	// Equal cannot replace a cancelled registration. Control generations are
	// monotonic epochs, not sequence numbers, so the safe replacement may skip.
	equal := registry.PrepareRouteDrain(context.Background(), "app.example.com", 7, key)
	assert.ErrorIs(t, equal, ErrRuntimeDrainStale)
	require.NoError(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 42, key))

	assert.ErrorIs(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, key, 7)), ErrRuntimeDrainStale)
	assert.ErrorIs(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, key, 41)), ErrRuntimeDrainStale)

	result := make(chan bool, 1)
	go func() { result <- registry.WaitForNoInFlight(context.Background(), "old-id", time.Second) }()
	select {
	case <-result:
		t.Fatal("old or intermediate acknowledgement released replacement")
	case <-time.After(20 * time.Millisecond):
	}
	require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, key, 42)))
	assert.True(t, <-result)
}

func TestRuntimeDrainRegistryCapsConcurrentPendingAndReleasesCapacity(t *testing.T) {
	type prepared struct {
		id  string
		key domain.RouteTargetKey
		ok  bool
	}
	states := make(map[string]domain.RuntimeRouteState, runtimeDrainPendingLimit+1)
	keys := make(map[string]domain.RouteTargetKey, runtimeDrainPendingLimit+1)
	for index := range runtimeDrainPendingLimit + 1 {
		id := fmt.Sprintf("old-%d", index)
		state, ok := runtimeReadyRouteState(1, "app.example.com", &domain.Container{Name: fmt.Sprintf("private-%d", index), Status: string(domain.ContainerStatusRunning), Ports: []int{8080}})
		require.True(t, ok)
		key, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
		require.NoError(t, err)
		states[id], keys[id] = state, key
	}
	registry := NewRuntimeDrainRegistry(func(id string) (domain.RuntimeRouteState, bool) { state, ok := states[id]; return state, ok })

	start := make(chan struct{})
	results := make(chan prepared, runtimeDrainPendingLimit+1)
	for index := range runtimeDrainPendingLimit + 1 {
		id := fmt.Sprintf("old-%d", index)
		go func() {
			<-start
			results <- prepared{id: id, key: keys[id], ok: registry.PrepareDrain(id)}
		}()
	}
	close(start)

	var successful prepared
	var rejected string
	successes := 0
	for range runtimeDrainPendingLimit + 1 {
		result := <-results
		if result.ok {
			successful = result
			successes++
		} else {
			rejected = result.id
		}
	}
	require.Equal(t, runtimeDrainPendingLimit, successes)
	require.NotEmpty(t, rejected)
	assert.Len(t, registry.pending, runtimeDrainPendingLimit, "active waiters are never evicted")

	require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), cleanRuntimeDrainAck(t, successful.key, 1)))
	assert.True(t, registry.WaitForNoInFlight(context.Background(), successful.id, time.Second))
	assert.Len(t, registry.pending, runtimeDrainPendingLimit-1)
	assert.True(t, registry.PrepareDrain(rejected), "completed waiter releases one admission slot")
}

func TestRuntimeDrainRegistryTombstonesBoundAndFailClosed(t *testing.T) {
	states := make(map[string]domain.RuntimeRouteState, runtimeDrainLedgerLimit+1)
	keys := make(map[string]domain.RouteTargetKey, runtimeDrainLedgerLimit+1)
	for index := range runtimeDrainLedgerLimit + 1 {
		id := fmt.Sprintf("old-%d", index)
		state, ok := runtimeReadyRouteState(1, "app.example.com", &domain.Container{Name: fmt.Sprintf("private-%d", index), Status: string(domain.ContainerStatusRunning), Ports: []int{8080}})
		require.True(t, ok)
		key, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
		require.NoError(t, err)
		states[id], keys[id] = state, key
	}
	registry := NewRuntimeDrainRegistry(func(id string) (domain.RuntimeRouteState, bool) { state, ok := states[id]; return state, ok })
	for index := range runtimeDrainLedgerLimit {
		id := fmt.Sprintf("old-%d", index)
		require.True(t, registry.PrepareDrain(id))
		require.NoError(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 1, keys[id]))
		registry.CancelDrain(id)
	}
	assert.Len(t, registry.awaitingNewRegistration, runtimeDrainLedgerLimit)
	assert.Len(t, registry.lastGeneration, runtimeDrainLedgerLimit)

	id := fmt.Sprintf("old-%d", runtimeDrainLedgerLimit)
	require.True(t, registry.PrepareDrain(id))
	assert.ErrorIs(t, registry.PrepareRouteDrain(context.Background(), "app.example.com", 1, keys[id]), ErrRuntimeDrainUnknown)
	registry.CancelDrain(id)
	assert.Len(t, registry.awaitingNewRegistration, runtimeDrainLedgerLimit)
	assert.Len(t, registry.lastGeneration, runtimeDrainLedgerLimit)
	assert.True(t, registry.awaitingOverflow)
}

func TestRuntimeDrainRegistryCompletedLedgerPrunesFIFOOverLimit(t *testing.T) {
	registry := NewRuntimeDrainRegistry(nil)
	for index := range runtimeDrainLedgerLimit + 1 {
		identity := runtimeDrainIdentity{domain: fmt.Sprintf("app-%d.example.com", index), key: domain.RouteTargetKey(fmt.Sprintf("key-%d", index))}
		registry.completed[identity] = runtimeDrainOutcome{generation: 1}
		registry.completedOrder = append(registry.completedOrder, identity)
	}
	registry.trimCompletedLocked()
	assert.Len(t, registry.completed, runtimeDrainLedgerLimit)
	_, retained := registry.completed[runtimeDrainIdentity{domain: "app-0.example.com", key: "key-0"}]
	assert.False(t, retained, "oldest inactive terminal entry is evicted first")
}

func TestRuntimeDrainRegistryTimeoutRollbackAndShutdownFallBack(t *testing.T) {
	state, key := testRuntimeDrainState(t)

	t.Run("control timeout", func(t *testing.T) {
		registry := testRuntimeDrainRegistry(state)
		registry.PrepareDrain("old-id")
		ack := cleanRuntimeDrainAck(t, key, 8)
		ack.Status = domain.RouteDrainStatusTimedOut
		ack.TimeoutReason = domain.RouteDrainTimeoutReasonEdge
		ack.InFlight = 1
		require.NoError(t, registry.AcknowledgeRouteDrain(context.Background(), ack))
		assert.False(t, registry.WaitForNoInFlight(context.Background(), "old-id", time.Second))
	})

	t.Run("local timeout and rollback", func(t *testing.T) {
		registry := testRuntimeDrainRegistry(state)
		registry.PrepareDrain("old-id")
		assert.False(t, registry.WaitForNoInFlight(context.Background(), "old-id", time.Millisecond))
		registry.PrepareDrain("old-id")
		registry.CancelDrain("old-id")
		assert.False(t, registry.WaitForNoInFlight(context.Background(), "old-id", time.Millisecond))
	})

	t.Run("shutdown", func(t *testing.T) {
		registry := testRuntimeDrainRegistry(state)
		registry.PrepareDrain("old-id")
		result := make(chan bool, 1)
		go func() { result <- registry.WaitForNoInFlight(context.Background(), "old-id", time.Second) }()
		registry.Close()
		assert.False(t, <-result)
	})
}

func testRuntimeDrainRegistry(state domain.RuntimeRouteState) *RuntimeDrainRegistry {
	return NewRuntimeDrainRegistry(func(string) (domain.RuntimeRouteState, bool) { return state, true })
}

func testRuntimeDrainState(t *testing.T) (domain.RuntimeRouteState, domain.RouteTargetKey) {
	t.Helper()
	state, ok := runtimeReadyRouteState(1, "app.example.com", &domain.Container{Name: "private-old-container", Status: string(domain.ContainerStatusRunning), Ports: []int{8080}})
	require.True(t, ok)
	key, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
	require.NoError(t, err)
	return state, key
}

func cleanRuntimeDrainAck(t *testing.T, key domain.RouteTargetKey, generation domain.RouteTargetGeneration) domain.RouteDrainAck {
	t.Helper()
	return domain.RouteDrainAck{RouteDrainState: domain.RouteDrainState{
		CanonicalDomain:      "app.example.com",
		TransitionGeneration: generation,
		OldTargetKey:         key,
		AcknowledgedAt:       time.Now().UTC(),
	}, Status: domain.RouteDrainStatusAcknowledged}
}
