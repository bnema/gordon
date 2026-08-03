package traffic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestBuildEdgeGraphUsesOnlySanitizedSnapshotDomainsAndOrdersThem(t *testing.T) {
	first, err := domain.NewReadyRouteTargetEntry("z.example.com", "z-target", 8080, "http", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	second, err := domain.NewReadyRouteTargetEntry("a.example.com", "a-target", 8080, "http", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	graph, err := BuildEdgeGraph(EdgeInput{
		EntryPoints:   map[string]EntryPointConfig{"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}},
		RouteSnapshot: domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{first, second}},
	})
	require.NoError(t, err)
	require.Len(t, graph.Routers, 2)
	assert.Equal(t, []string{"route:a.example.com", "route:z.example.com"}, []string{graph.Routers[0].Name, graph.Routers[1].Name})
	assert.Empty(t, graph.Services[0].Backends)
	assert.Empty(t, graph.Services[1].Backends)
}

func TestBuildEdgeGraphFailsClosedForLoopbackL4Backend(t *testing.T) {
	snapshot := domain.RouteTargetSnapshot{Generation: 1}
	_, err := BuildEdgeGraph(EdgeInput{
		EntryPoints:   map[string]EntryPointConfig{"tcp": {Address: ":5432", Protocol: domain.EntryPointProtocolTCP}},
		Traffic:       Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "db", EntryPoint: "tcp", Service: "service:db:sql"}}}},
		RouteSnapshot: snapshot,
		Services:      []domain.StandaloneService{{Name: "db", Image: "example/db:latest", Enabled: true, Ports: []domain.StandaloneServicePort{{Name: "sql", Container: 5432, Protocol: domain.NetworkProtocolTCP, Publish: "127.0.0.1:5432", Public: true}}}},
	})
	require.ErrorContains(t, err, "not split reachable")
}

func TestBuildEdgeGraphAcceptsAliasL4BackendAndPinsExternalTarget(t *testing.T) {
	external, err := domain.NewExternalReadyRouteTargetEntry("external.example.com", "198.51.100.20", "upstream.example.net", 443, "https", domain.RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	graph, err := BuildEdgeGraph(EdgeInput{
		EntryPoints: map[string]EntryPointConfig{
			"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP},
			"tcp":  {Address: ":5432", Protocol: domain.EntryPointProtocolTCP},
		},
		Traffic:              Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "db", EntryPoint: "tcp", Service: "network_service:db:sql"}}}},
		RouteSnapshot:        domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{external}},
		ExternalRouteTargets: []domain.RouteTargetEntry{external},
		NetworkServices:      []NetworkServiceConfig{{Name: "db", Ports: []PortConfig{{Name: "sql", Container: 5432, Protocol: domain.NetworkProtocolTCP}}}},
	})
	require.NoError(t, err)
	var db domain.TrafficService
	for _, service := range graph.Services {
		if service.Name == "network_service:db:sql" {
			db = service
		}
	}
	require.Len(t, db.Backends, 1)
	assert.Equal(t, "db", db.Backends[0].Host)

	wrong := external
	wrong.TargetHost = "198.51.100.21"
	_, err = BuildEdgeGraph(EdgeInput{RouteSnapshot: domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{external}}, ExternalRouteTargets: []domain.RouteTargetEntry{wrong}})
	require.ErrorContains(t, err, "does not match pinned")
}
