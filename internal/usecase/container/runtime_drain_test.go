package container

import (
	"context"
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
