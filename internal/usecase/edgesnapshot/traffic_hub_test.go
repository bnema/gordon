package edgesnapshot

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficGraphHubLatestWinsMonotonicImmutable(t *testing.T) {
	hub := NewTrafficGraphHub()
	first := trafficHubSnapshot(1, "first.internal")
	require.NoError(t, hub.Publish(first))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := hub.SubscribeTrafficGraphs(ctx)
	require.NoError(t, err)
	require.NoError(t, hub.Publish(trafficHubSnapshot(2, "second.internal")))
	require.NoError(t, hub.Publish(trafficHubSnapshot(3, "third.internal")))
	select {
	case update := <-updates:
		assert.Equal(t, domain.TrafficGraphGeneration(3), update.Generation)
		update.Graph.Services[0].Backends[0].Host = "mutated.internal"
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for graph")
	}
	current, err := hub.CurrentTrafficGraph(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "third.internal", current.Graph.Services[0].Backends[0].Host)
	assert.Error(t, hub.Publish(trafficHubSnapshot(3, "third.internal")))
}

func trafficHubSnapshot(generation domain.TrafficGraphGeneration, host string) domain.TrafficGraphSnapshot {
	return domain.TrafficGraphSnapshot{Generation: generation, Graph: domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: "tcp", Address: ":9000", Protocol: domain.EntryPointProtocolTCP}},
		Routers:     []domain.TrafficRouter{{Name: "app", EntryPoint: "tcp", Protocol: domain.RouterProtocolTCP, Service: "network_service:app:http"}},
		Services:    []domain.TrafficService{{Name: "network_service:app:http", Backends: []domain.TrafficBackend{{Host: host, Port: 8080, Protocol: domain.NetworkProtocolTCP}}}},
	}}
}
