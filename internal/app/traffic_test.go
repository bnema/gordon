package app

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/traffic"
)

func TestTrafficRuntimeGraphFiltersLegacyHTTPEntrypoints(t *testing.T) {
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{
			{Name: traffic.DefaultHTTPEntryPointName, Address: "127.0.0.1:8080", Protocol: domain.EntryPointProtocolHTTP},
			{Name: traffic.DefaultTLSEntryPointName, Address: "127.0.0.1:8443", Protocol: domain.EntryPointProtocolTLSMux},
			{Name: "postgres", Address: "127.0.0.1:15432", Protocol: domain.EntryPointProtocolTCP},
		},
		Routers: []domain.TrafficRouter{
			{Name: "app", EntryPoint: traffic.DefaultHTTPEntryPointName, Protocol: domain.RouterProtocolHTTP, Service: "route:app.example.com"},
			{Name: "postgres", EntryPoint: "postgres", Protocol: domain.RouterProtocolTCP, Service: "network_service:postgres:db"},
		},
		Services: []domain.TrafficService{
			{Name: "route:app.example.com"},
			{Name: "network_service:postgres:db", Backends: []domain.TrafficBackend{{Name: "db", Host: "127.0.0.1", Port: 5432, Protocol: domain.NetworkProtocolTCP}}},
		},
	}

	filtered, err := trafficRuntimeGraph(graph)
	require.NoError(t, err)
	require.Len(t, filtered.EntryPoints, 1)
	assert.Equal(t, "postgres", filtered.EntryPoints[0].Name)
	require.Len(t, filtered.Routers, 1)
	assert.Equal(t, "postgres", filtered.Routers[0].Name)
	require.Len(t, filtered.Services, 1)
	assert.Equal(t, "network_service:postgres:db", filtered.Services[0].Name)
}

func TestTrafficRuntimeGraphRejectsL4RouterOnLegacyEntrypoint(t *testing.T) {
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: traffic.DefaultTLSEntryPointName, Address: "127.0.0.1:8443", Protocol: domain.EntryPointProtocolTLSMux}},
		Routers:     []domain.TrafficRouter{{Name: "raw", EntryPoint: traffic.DefaultTLSEntryPointName, Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: "raw.example.com"}, Service: "network_service:raw:tls"}},
		Services:    []domain.TrafficService{{Name: "network_service:raw:tls", Backends: []domain.TrafficBackend{{Name: "raw:tls", Host: "127.0.0.1", Port: 443, Protocol: domain.NetworkProtocolTCP}}}},
	}

	_, err := trafficRuntimeGraph(graph)
	require.ErrorContains(t, err, "not owned by the traffic manager")
}

func TestApplyTrafficRuntimeConfigAppliesCustomL4Entrypoint(t *testing.T) {
	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	cfg := Config{
		EntryPoints: map[string]traffic.EntryPointConfig{
			"postgres": {Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTCP},
		},
		NetworkServices: []traffic.NetworkServiceConfig{{
			Name:  "postgres",
			Ports: []traffic.PortConfig{{Name: "db", Container: 5432, Protocol: domain.NetworkProtocolTCP}},
		}},
	}
	cfg.Traffic.TCP.Routers = []traffic.RouterConfig{{Name: "postgres", EntryPoint: "postgres", Service: "network_service:postgres:db"}}

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetRoutes(context.Background()).Return(nil)
	configSvc.EXPECT().GetExternalRoutes().Return(nil)

	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc))
	status := manager.Status()
	require.Len(t, status.EntryPoints, 1)
	assert.Equal(t, "postgres", status.EntryPoints[0].Name)
	assert.True(t, status.EntryPoints[0].Active)
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}
