package edgesnapshot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	trafficusecase "github.com/bnema/gordon/internal/usecase/traffic"
)

func TestTrafficGraphProducerPublishesInitialRouteGraphAndUpdate(t *testing.T) {
	routes := NewSnapshotHub()
	graphs := NewTrafficGraphHub()
	require.NoError(t, routes.Publish(trafficProducerRouteSnapshot(t, 1, "app.example.com")))
	producer, err := NewTrafficGraphProducer(routes, graphs, trafficProducerOptions())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, producer.Start(ctx))

	initial, err := graphs.CurrentTrafficGraph(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, initial.Generation)
	assert.Equal(t, "route:app.example.com", initial.Graph.Routers[0].Name)

	require.NoError(t, routes.Publish(trafficProducerRouteSnapshot(t, 2, "next.example.com")))
	require.Eventually(t, func() bool {
		update, currentErr := graphs.CurrentTrafficGraph(context.Background())
		return currentErr == nil && update.Generation > initial.Generation && len(update.Graph.Routers) == 1 && update.Graph.Routers[0].Name == "route:next.example.com"
	}, time.Second, time.Millisecond)
}

func TestTrafficGraphProducerReloadsWithNewMonotonicGeneration(t *testing.T) {
	routes := NewSnapshotHub()
	graphs := NewTrafficGraphHub()
	require.NoError(t, routes.Publish(trafficProducerRouteSnapshot(t, 4, "app.example.com")))
	producer, err := NewTrafficGraphProducer(routes, graphs, trafficProducerOptions())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, producer.Start(ctx))
	before, err := graphs.CurrentTrafficGraph(context.Background())
	require.NoError(t, err)

	options := trafficProducerOptions()
	options.Traffic.TCP.MaxConnections = 17
	require.NoError(t, producer.Reload(context.Background(), options))
	after, err := graphs.CurrentTrafficGraph(context.Background())
	require.NoError(t, err)
	assert.Greater(t, after.Generation, before.Generation)
	assert.Equal(t, 17, after.Graph.Options.TCP.MaxConnections)
}

func TestTrafficGraphProducerInvalidL4BackendPreventsInitialPublish(t *testing.T) {
	routes := NewSnapshotHub()
	graphs := NewTrafficGraphHub()
	require.NoError(t, routes.Publish(trafficProducerRouteSnapshot(t, 1, "app.example.com")))
	options := TrafficGraphProducerOptions{
		EntryPoints: map[string]trafficusecase.EntryPointConfig{
			"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP},
			"tcp":  {Address: ":15432", Protocol: domain.EntryPointProtocolTCP},
		},
		Traffic:  trafficusecase.Config{TCP: trafficusecase.TCPConfig{Routers: []trafficusecase.RouterConfig{{Name: "db", EntryPoint: "tcp", Service: "service:db:sql"}}}},
		Services: []domain.StandaloneService{{Name: "db", Image: "example/db:latest", Enabled: true, Ports: []domain.StandaloneServicePort{{Name: "sql", Container: 5432, Protocol: domain.NetworkProtocolTCP, Publish: "127.0.0.1:5432", Public: true}}}},
	}
	producer, err := NewTrafficGraphProducer(routes, graphs, options)
	require.NoError(t, err)
	err = producer.Start(context.Background())
	require.ErrorContains(t, err, "not split reachable")
	_, err = graphs.CurrentTrafficGraph(context.Background())
	require.ErrorIs(t, err, ErrNoTrafficGraph)
}

func trafficProducerOptions() TrafficGraphProducerOptions {
	return TrafficGraphProducerOptions{EntryPoints: map[string]trafficusecase.EntryPointConfig{"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}}}
}

func trafficProducerRouteSnapshot(t *testing.T, generation domain.RouteTargetGeneration, host string) domain.RouteTargetSnapshot {
	t.Helper()
	entry, err := domain.NewReadyRouteTargetEntry(host, "target-"+host[:3], 8080, "http", domain.RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return domain.RouteTargetSnapshot{Generation: generation, Entries: []domain.RouteTargetEntry{entry}}
}
