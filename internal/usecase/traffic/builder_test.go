package traffic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestBuildSmartTCPRoutesDefaultToEdge(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}},
		Routes:      []domain.Route{{Domain: "app.example.com"}},
	})
	require.NoError(t, err)
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "route:app.example.com", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"})
}

func TestBuildRawFallbackConfigMapsToEntryPoint(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{"edge": {
			Address:                 ":443",
			Protocol:                domain.EntryPointProtocolSmartTCP,
			RawFallback:             "ssh-fallback",
			RawFallbackTrustedCIDRs: []string{"100.64.0.0/10"},
			AllowPublicRawFallback:  true,
		}},
		Traffic:         Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "ssh-fallback", EntryPoint: "edge", Service: "network_service:ssh:ssh"}}}},
		NetworkServices: []NetworkServiceConfig{{Name: "ssh", Ports: []PortConfig{{Name: "ssh", Container: 22, Protocol: domain.NetworkProtocolTCP}}}},
	})
	require.NoError(t, err)
	require.Equal(t, []domain.EntryPoint{{
		Name:                    "edge",
		Address:                 ":443",
		Protocol:                domain.EntryPointProtocolSmartTCP,
		RawFallback:             "ssh-fallback",
		RawFallbackTrustedCIDRs: []string{"100.64.0.0/10"},
		AllowPublicRawFallback:  true,
	}}, graph.EntryPoints)
}

func TestBuildSmartTCPRoutesRequireCompatibleEdge(t *testing.T) {
	_, err := Build(Input{Routes: []domain.Route{{Domain: "app.example.com"}}})
	require.ErrorContains(t, err, "entrypoint")
}

func TestBuildDefaultsAndHTTPRoutes(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints:    map[string]EntryPointConfig{"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}},
		Routes:         []domain.Route{{Domain: "app.example.com"}, {Domain: "secure.example.com", HTTPS: true}},
		ExternalRoutes: map[string]string{"ext.example.com": "backend.local:8080"},
	})
	require.NoError(t, err)

	require.Equal(t, []domain.EntryPoint{{Name: "edge", Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}}, graph.EntryPoints)
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "route:app.example.com", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"})
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "route:secure.example.com", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "secure.example.com"}, Service: "route:secure.example.com"})
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "external_route:ext.example.com", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "ext.example.com"}, Service: "external_route:ext.example.com"})
	require.Contains(t, graph.Services, domain.TrafficService{Name: "external_route:ext.example.com", Backends: []domain.TrafficBackend{{Name: "ext.example.com", Host: "backend.local", Port: 8080, Protocol: domain.NetworkProtocolTCP}}})
}

func TestBuildCustomEntryPointTrustedCIDRs(t *testing.T) {
	graph, err := Build(Input{EntryPoints: map[string]EntryPointConfig{"tcp-public": {Address: ":5432", Protocol: domain.EntryPointProtocolTCP, TrustedCIDRs: []string{"10.0.0.0/8"}}}})
	require.NoError(t, err)
	require.Equal(t, []domain.EntryPoint{{Name: "tcp-public", Address: ":5432", Protocol: domain.EntryPointProtocolTCP, TrustedCIDRs: []string{"10.0.0.0/8"}}}, graph.EntryPoints)
}

func TestBuildExplicitTCPUDPTLSRouterResolution(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{
			"tcp-db":  {Address: ":5432", Protocol: domain.EntryPointProtocolTCP},
			"udp-dns": {Address: ":5353", Protocol: domain.EntryPointProtocolUDP},
			"tls-db":  {Address: ":15432", Protocol: domain.EntryPointProtocolTLSMux},
		},
		Traffic: Config{
			TCP: TCPConfig{Routers: []RouterConfig{{Name: "db", EntryPoint: "tcp-db", Service: "network_service:postgres:sql"}}},
			UDP: UDPConfig{Routers: []RouterConfig{{Name: "dns", EntryPoint: "udp-dns", Service: "network_service:coredns:dns"}}},
			TLS: TLSConfig{Routers: []RouterConfig{{Name: "tlsdb", EntryPoint: "tls-db", SNI: "db.example.com", Service: "network_service:postgres:sql"}}},
		},
		NetworkServices: []NetworkServiceConfig{
			{Name: "postgres", Ports: []PortConfig{{Name: "sql", Container: 5432, Protocol: domain.NetworkProtocolTCP}}},
			{Name: "coredns", Ports: []PortConfig{{Name: "dns", Container: 53, Protocol: domain.NetworkProtocolUDP}}},
		},
	})
	require.NoError(t, err)
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "db", EntryPoint: "tcp-db", Protocol: domain.RouterProtocolTCP, Service: "network_service:postgres:sql"})
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "dns", EntryPoint: "udp-dns", Protocol: domain.RouterProtocolUDP, Service: "network_service:coredns:dns"})
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "tlsdb", EntryPoint: "tls-db", Protocol: domain.RouterProtocolTLSPassthrough, Rule: domain.TrafficRule{SNI: "db.example.com"}, Service: "network_service:postgres:sql"})
	require.Contains(t, graph.Services, domain.TrafficService{Name: "network_service:postgres:sql", Backends: []domain.TrafficBackend{{Name: "postgres:sql", Host: "postgres", Port: 5432, Protocol: domain.NetworkProtocolTCP}}})
}

func TestBuildRejectsStaticServiceRef(t *testing.T) {
	_, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{"tcp": {Address: ":1234", Protocol: domain.EntryPointProtocolTCP}},
		Traffic:     Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "static:legacy"}}}},
	})
	require.ErrorContains(t, err, "static")
}

func TestBuildRejectsTLSHTTPRouteConflict(t *testing.T) {
	_, err := Build(Input{
		EntryPoints:     map[string]EntryPointConfig{"edge": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}},
		Routes:          []domain.Route{{Domain: "app.example.com", HTTPS: true}},
		Traffic:         Config{TLS: TLSConfig{Routers: []RouterConfig{{Name: "passthrough", EntryPoint: "edge", SNI: "app.example.com", Service: "network_service:app:https"}}}},
		NetworkServices: []NetworkServiceConfig{{Name: "app", Ports: []PortConfig{{Name: "https", Container: 443, Protocol: domain.NetworkProtocolTCP}}}},
	})
	require.ErrorContains(t, err, "http host conflicts")
}

func TestBuildOptionsParsing(t *testing.T) {
	graph, err := Build(Input{Traffic: Config{TCP: TCPConfig{DialTimeout: "1s", IdleTimeout: "2m", DrainTimeout: "3s", MaxConnections: 12}, UDP: UDPConfig{IdleTimeout: "4s", DrainTimeout: "5s", MaxSessions: 34}}})
	require.NoError(t, err)
	require.Equal(t, time.Second, graph.Options.TCP.DialTimeout)
	require.Equal(t, 2*time.Minute, graph.Options.TCP.IdleTimeout)
	require.Equal(t, 3*time.Second, graph.Options.TCP.DrainTimeout)
	require.Equal(t, 12, graph.Options.TCP.MaxConnections)
	require.Equal(t, 4*time.Second, graph.Options.UDP.IdleTimeout)
	require.Equal(t, 5*time.Second, graph.Options.UDP.DrainTimeout)
	require.Equal(t, 34, graph.Options.UDP.MaxSessions)

	defaults, err := Build(Input{})
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, defaults.Options.TCP.DialTimeout)
	require.Equal(t, 5*time.Minute, defaults.Options.TCP.IdleTimeout)
	require.Equal(t, 30*time.Second, defaults.Options.TCP.DrainTimeout)
	require.Equal(t, 30*time.Second, defaults.Options.UDP.IdleTimeout)
	require.Equal(t, 30*time.Second, defaults.Options.UDP.DrainTimeout)

	_, err = Build(Input{Traffic: Config{TCP: TCPConfig{DialTimeout: "bogus"}}})
	require.ErrorContains(t, err, "traffic.tcp.dial_timeout")
}

func TestBuildRejectsAmbiguousNetworkServices(t *testing.T) {
	t.Run("duplicate service name", func(t *testing.T) {
		_, err := Build(Input{NetworkServices: []NetworkServiceConfig{
			{Name: "db", Ports: []PortConfig{{Name: "sql", Container: 5432, Protocol: domain.NetworkProtocolTCP}}},
			{Name: "db", Ports: []PortConfig{{Name: "sql", Container: 5433, Protocol: domain.NetworkProtocolTCP}}},
		}})
		require.ErrorContains(t, err, "duplicate network service")
	})

	t.Run("duplicate port name", func(t *testing.T) {
		_, err := Build(Input{NetworkServices: []NetworkServiceConfig{
			{Name: "db", Ports: []PortConfig{
				{Name: "sql", Container: 5432, Protocol: domain.NetworkProtocolTCP},
				{Name: "sql", Container: 5433, Protocol: domain.NetworkProtocolTCP},
			}},
		}})
		require.ErrorContains(t, err, "duplicate port")
	})

	t.Run("unused invalid port rejected", func(t *testing.T) {
		_, err := Build(Input{NetworkServices: []NetworkServiceConfig{
			{Name: "db", Ports: []PortConfig{{Name: "unused", Container: 0, Protocol: domain.NetworkProtocolTCP}}},
		}})
		require.ErrorContains(t, err, "invalid container port")
	})

	t.Run("unused invalid protocol rejected", func(t *testing.T) {
		_, err := Build(Input{NetworkServices: []NetworkServiceConfig{
			{Name: "db", Ports: []PortConfig{{Name: "unused", Container: 5432, Protocol: domain.NetworkProtocol("sctp")}}},
		}})
		require.ErrorContains(t, err, "invalid protocol")
	})
}

func TestBuildRejectsInvalidServiceRefs(t *testing.T) {
	base := Input{
		EntryPoints:     map[string]EntryPointConfig{"tcp": {Address: ":1234", Protocol: domain.EntryPointProtocolTCP}},
		NetworkServices: []NetworkServiceConfig{{Name: "svc", Ports: []PortConfig{{Name: "udp", Container: 53, Protocol: domain.NetworkProtocolUDP}}}},
	}

	t.Run("unknown service", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "network_service:missing:tcp"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "unknown network service")
	})

	t.Run("unknown port", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "network_service:svc:missing"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "unknown port")
	})

	t.Run("protocol mismatch", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "network_service:svc:udp"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "does not match")
	})
}
