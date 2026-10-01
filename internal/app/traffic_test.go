package app

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	"github.com/bnema/gordon/internal/usecase/traffic"
)

func TestTrafficRuntimeGraphKeepsSmartTCPHTTPAndTLSPassthroughRouters(t *testing.T) {
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{
			{Name: traffic.DefaultEdgeEntryPointName, Address: "127.0.0.1:8443", Protocol: domain.EntryPointProtocolSmartTCP, RawFallback: "ssh", RawFallbackTrustedCIDRs: []string{"127.0.0.0/8"}},
		},
		Routers: []domain.TrafficRouter{
			{Name: "app", EntryPoint: traffic.DefaultEdgeEntryPointName, Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"},
			{Name: "tls-raw", EntryPoint: traffic.DefaultEdgeEntryPointName, Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: "raw.example.com"}, Service: "network_service:raw:tls"},
			{Name: "ssh", EntryPoint: traffic.DefaultEdgeEntryPointName, Protocol: domain.RouterProtocolTCP, Service: "network_service:ssh:ssh"},
		},
		Services: []domain.TrafficService{
			{Name: "route:app.example.com"},
			{Name: "network_service:raw:tls", Backends: []domain.TrafficBackend{{Name: "raw:tls", Host: "127.0.0.1", Port: 443, Protocol: domain.NetworkProtocolTCP}}},
			{Name: "network_service:ssh:ssh", Backends: []domain.TrafficBackend{{Name: "ssh:ssh", Host: "127.0.0.1", Port: 22, Protocol: domain.NetworkProtocolTCP}}},
		},
	}

	filtered, err := trafficRuntimeGraph(graph)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{traffic.DefaultEdgeEntryPointName}, entryPointNames(filtered.EntryPoints))
	assert.ElementsMatch(t, []string{"app", "tls-raw", "ssh"}, routerNames(filtered.Routers))
	assert.ElementsMatch(t, []string{"route:app.example.com", "network_service:raw:tls", "network_service:ssh:ssh"}, serviceNames(filtered.Services))
}

func TestTrafficRuntimeGraphHTTPEntrypointIsInvalidRatherThanSilentlyFiltered(t *testing.T) {
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: "edge", Address: "127.0.0.1:8443", Protocol: domain.EntryPointProtocolHTTP}},
		Routers:     []domain.TrafficRouter{{Name: "app", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Service: "route:app.example.com"}},
		Services:    []domain.TrafficService{{Name: "route:app.example.com"}},
	}

	require.ErrorContains(t, graph.Validate(), "unsupported traffic entrypoint protocol \"http\"")
}

func TestTrafficRuntimeGraphKeepsDefaultTLSEntrypoint(t *testing.T) {
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{
			{Name: "websecure", Address: "127.0.0.1:8443", Protocol: domain.EntryPointProtocolTLSMux},
			{Name: "postgres", Address: "127.0.0.1:15432", Protocol: domain.EntryPointProtocolTCP},
		},
		Routers: []domain.TrafficRouter{
			{Name: "app-secure", EntryPoint: "websecure", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"},
			{Name: "postgres", EntryPoint: "postgres", Protocol: domain.RouterProtocolTCP, Service: "network_service:postgres:db"},
		},
		Services: []domain.TrafficService{
			{Name: "route:app.example.com"},
			{Name: "network_service:postgres:db", Backends: []domain.TrafficBackend{{Name: "db", Host: "127.0.0.1", Port: 5432, Protocol: domain.NetworkProtocolTCP}}},
		},
	}

	filtered, err := trafficRuntimeGraph(graph)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"websecure", "postgres"}, entryPointNames(filtered.EntryPoints))
	assert.ElementsMatch(t, []string{"app-secure", "postgres"}, routerNames(filtered.Routers))
	assert.ElementsMatch(t, []string{"route:app.example.com", "network_service:postgres:db"}, serviceNames(filtered.Services))
}

func TestTrafficRuntimeGraphAllowsTLSPassthroughOnDefaultTLS(t *testing.T) {
	graph := domain.TrafficGraph{
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: "127.0.0.1:8443", Protocol: domain.EntryPointProtocolTLSMux}},
		Routers:     []domain.TrafficRouter{{Name: "raw", EntryPoint: "websecure", Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: "raw.example.com"}, Service: "network_service:raw:tls"}},
		Services:    []domain.TrafficService{{Name: "network_service:raw:tls", Backends: []domain.TrafficBackend{{Name: "raw:tls", Host: "127.0.0.1", Port: 443, Protocol: domain.NetworkProtocolTCP}}}},
	}

	filtered, err := trafficRuntimeGraph(graph)
	require.NoError(t, err)
	require.Len(t, filtered.EntryPoints, 1)
	assert.Equal(t, "websecure", filtered.EntryPoints[0].Name)
	require.Len(t, filtered.Routers, 1)
	assert.Equal(t, "raw", filtered.Routers[0].Name)
}

// stubHosts builds an ACTIVE-derived host source for traffic tests.
func stubHosts(domains ...string) *stubAppRoutes {
	hosts := make([]out.AppHost, 0, len(domains))
	for _, d := range domains {
		hosts = append(hosts, out.AppHost{Host: d})
	}
	return &stubAppRoutes{hosts: hosts}
}

func TestApplyTrafficRuntimeConfigAppliesSmartTCPEntrypointWithRawFallbackPolicy(t *testing.T) {
	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	cfg := Config{
		EntryPoints: map[string]traffic.EntryPointConfig{
			traffic.DefaultEdgeEntryPointName: {
				Address:                 freeTCPAddress(t),
				Protocol:                domain.EntryPointProtocolSmartTCP,
				RawFallback:             "ssh",
				RawFallbackTrustedCIDRs: []string{"127.0.0.0/8"},
			},
		},
		NetworkServices: []traffic.NetworkServiceConfig{{
			Name:  "ssh",
			Ports: []traffic.PortConfig{{Name: "ssh", Container: 22, Protocol: domain.NetworkProtocolTCP}},
		}},
	}
	cfg.Traffic.TCP.Routers = []traffic.RouterConfig{{Name: "ssh", EntryPoint: traffic.DefaultEdgeEntryPointName, Service: "network_service:ssh:ssh"}}

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(nil)

	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc, stubHosts("app.example.com")))
	status := manager.Status()
	require.Len(t, status.EntryPoints, 1)
	assert.Equal(t, traffic.DefaultEdgeEntryPointName, status.EntryPoints[0].Name)
	assert.True(t, status.EntryPoints[0].Active)
	assert.Equal(t, domain.EntryPointProtocolSmartTCP, status.EntryPoints[0].Protocol)
}

func TestApplyTrafficRuntimeConfigPassesStandaloneServicesToBuilder(t *testing.T) {
	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	cfg := Config{
		EntryPoints: map[string]traffic.EntryPointConfig{
			"rust": {Address: freeUDPAddress(t), Protocol: domain.EntryPointProtocolUDP},
		},
		Services: []servicecfg.Config{{
			Name:    "rust",
			Image:   "localhost/rust:latest",
			Enabled: true,
			Ports:   []servicecfg.PortConfig{{Name: "game", Container: 28015, Protocol: domain.NetworkProtocolUDP, Publish: "127.0.0.1:38015"}},
		}},
	}
	cfg.Traffic.UDP.Routers = []traffic.RouterConfig{{Name: "rust-game", EntryPoint: "rust", Service: "service:rust:game"}}

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(nil)

	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc, stubHosts()))
	status := manager.Status()
	require.Len(t, status.EntryPoints, 1)
	assert.Equal(t, "rust", status.EntryPoints[0].Name)
	assert.True(t, status.EntryPoints[0].Active)
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
	configSvc.EXPECT().GetExternalRoutes().Return(nil)

	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc, stubHosts()))
	status := manager.Status()
	require.Len(t, status.EntryPoints, 1)
	assert.Equal(t, "postgres", status.EntryPoints[0].Name)
	assert.True(t, status.EntryPoints[0].Active)
}

// stubL4Hosts is an ACTIVE-derived source with resolved TCP+UDP entries.
type stubL4Hosts struct {
	stubAppRoutes
	entries []apptraffic.RouteEntry
}

func (s *stubL4Hosts) L4Entries() []apptraffic.RouteEntry { return s.entries }

// TestApplyTrafficRuntimeConfigFusesAppL4Routes proves ACTIVE TCP+UDP
// projections join the applied graph as validated router/service pairs
// alongside historical routes, with exact-protocol loopback backends.
func TestApplyTrafficRuntimeConfigFusesAppL4Routes(t *testing.T) {
	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	tcpAddress := freeTCPAddress(t)
	udpAddress := freeUDPAddress(t)
	tcpHost, tcpPort, err := domain.SplitListenerAddress(tcpAddress)
	require.NoError(t, err)
	udpHost, udpPort, err := domain.SplitListenerAddress(udpAddress)
	require.NoError(t, err)
	cfg := Config{
		EntryPoints: map[string]traffic.EntryPointConfig{
			"tcp": {Address: tcpAddress, Protocol: domain.EntryPointProtocolTCP},
			"udp": {Address: udpAddress, Protocol: domain.EntryPointProtocolUDP},
		},
	}
	hosts := &stubL4Hosts{
		entries: []apptraffic.RouteEntry{
			{
				Kind: "tcp", RouterName: "app-game--server--tcp-9000", Entrypoint: "tcp",
				BindIP: tcpHost, BindPort: tcpPort,
				App: "game", Service: "server",
				Backend: domain.AppBackend{Host: "127.0.0.1", Port: 19000, ContainerPort: 9000, ContainerID: "c-game"},
			},
			{
				Kind: "udp", RouterName: "app-game--server--udp-9000", Entrypoint: "udp",
				BindIP: udpHost, BindPort: udpPort,
				App: "game", Service: "server",
				Backend: domain.AppBackend{Host: "127.0.0.1", Port: 19001, ContainerPort: 9000, ContainerID: "c-game"},
			},
		},
	}

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(nil)

	require.NoError(t, applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc, hosts))
	status := manager.Status()
	assert.Equal(t, "ok", status.LastReloadStatus)
	routers := map[string]domain.TrafficRouterStatus{}
	for _, router := range status.Routers {
		routers[router.Name] = router
	}
	require.Contains(t, routers, "app-game--server--tcp-9000")
	require.Contains(t, routers, "app-game--server--udp-9000")
	assert.Equal(t, domain.RouterProtocolTCP, routers["app-game--server--tcp-9000"].Protocol)
	assert.Equal(t, domain.RouterProtocolUDP, routers["app-game--server--udp-9000"].Protocol)
	assert.True(t, routers["app-game--server--tcp-9000"].Active)
	assert.True(t, routers["app-game--server--udp-9000"].Active)
	services := map[string]domain.TrafficServiceStatus{}
	for _, service := range status.Services {
		services[service.Name] = service
	}
	tcpSvc, ok := services["service:game--server:tcp-9000"]
	require.True(t, ok)
	require.Len(t, tcpSvc.Backends, 1)
	assert.Equal(t, "127.0.0.1", tcpSvc.Backends[0].Host)
	assert.Equal(t, 19000, tcpSvc.Backends[0].Port)
	assert.Equal(t, domain.NetworkProtocolTCP, tcpSvc.Backends[0].Protocol)
	udpSvc, ok := services["service:game--server:udp-9000"]
	require.True(t, ok)
	require.Len(t, udpSvc.Backends, 1)
	assert.Equal(t, "127.0.0.1", udpSvc.Backends[0].Host)
	assert.Equal(t, 19001, udpSvc.Backends[0].Port)
	assert.Equal(t, domain.NetworkProtocolUDP, udpSvc.Backends[0].Protocol)
}

// TestApplyTrafficRuntimeConfigRejectsMismatchedAppL4Bind proves the graph
// stage refuses a projected bind that differs from the entrypoint listener,
// so a mismatched declaration can never be silently widened.
func TestApplyTrafficRuntimeConfigRejectsMismatchedAppL4Bind(t *testing.T) {
	manager := trafficadapter.NewManager()
	defer func() { require.NoError(t, manager.Shutdown(context.Background())) }()
	tcpAddress := freeTCPAddress(t)
	cfg := Config{
		EntryPoints: map[string]traffic.EntryPointConfig{
			"tcp": {Address: tcpAddress, Protocol: domain.EntryPointProtocolTCP},
		},
	}
	hosts := &stubL4Hosts{
		entries: []apptraffic.RouteEntry{{
			Kind: "tcp", RouterName: "app-game--server--tcp-9000", Entrypoint: "tcp",
			BindIP: "127.0.0.1", BindPort: 1,
			App: "game", Service: "server",
			Backend: domain.AppBackend{Host: "127.0.0.1", Port: 19000, ContainerPort: 9000, ContainerID: "c-game"},
		}},
	}
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(nil)

	err := applyTrafficRuntimeConfig(context.Background(), manager, cfg, configSvc, hosts)
	require.Error(t, err)
}

func entryPointNames(entries []domain.EntryPoint) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func routerNames(routers []domain.TrafficRouter) []string {
	names := make([]string, 0, len(routers))
	for _, router := range routers {
		names = append(names, router.Name)
	}
	return names
}

func serviceNames(services []domain.TrafficService) []string {
	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Name)
	}
	return names
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func freeUDPAddress(t *testing.T) string {
	t.Helper()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	address := packetConn.LocalAddr().String()
	require.NoError(t, packetConn.Close())
	return address
}
