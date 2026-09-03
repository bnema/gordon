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

func TestBuildManagedRoutesUseOnlyCustomSmartTCPEntrypoint(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{"public": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP}},
		Routes:      []domain.Route{{Domain: "app.example.com", HTTPS: true}},
	})
	require.NoError(t, err)
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "route:app.example.com", EntryPoint: "public", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"})
}

func TestBuildRouteEntrypointPrefersEdge(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{
			"edge":   {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP},
			"public": {Address: ":8443", Protocol: domain.EntryPointProtocolSmartTCP},
		},
		Routes: []domain.Route{{Domain: "app.example.com", HTTPS: true}},
	})
	require.NoError(t, err)
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "route:app.example.com", EntryPoint: "edge", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"})
}

func TestBuildRoutesRejectAmbiguousRouteEntrypoints(t *testing.T) {
	_, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{
			"public-a": {Address: ":443", Protocol: domain.EntryPointProtocolSmartTCP},
			"public-b": {Address: ":8443", Protocol: domain.EntryPointProtocolTLSMux},
		},
		Routes: []domain.Route{{Domain: "app.example.com", HTTPS: true}},
	})
	require.ErrorContains(t, err, "multiple route-capable entrypoints")
}

func TestBuildExternalRoutesUseResolvedRouteEntryPoint(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints:    map[string]EntryPointConfig{"public": {Address: ":443", Protocol: domain.EntryPointProtocolTLSMux}},
		ExternalRoutes: map[string]string{"ext.example.com": "backend.local:8080"},
	})
	require.NoError(t, err)
	require.Contains(t, graph.Routers, domain.TrafficRouter{Name: "external_route:ext.example.com", EntryPoint: "public", Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: "ext.example.com"}, Service: "external_route:ext.example.com"})
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
		Traffic:         Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "ssh-fallback", EntryPoint: "edge", NetworkService: "ssh", Port: "ssh"}}}},
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
			TCP: TCPConfig{Routers: []RouterConfig{{Name: "db", EntryPoint: "tcp-db", NetworkService: "postgres", Port: "sql"}}},
			UDP: UDPConfig{Routers: []RouterConfig{{Name: "dns", EntryPoint: "udp-dns", NetworkService: "coredns", Port: "dns"}}},
			TLS: TLSConfig{Routers: []RouterConfig{{Name: "tlsdb", EntryPoint: "tls-db", SNI: "db.example.com", NetworkService: "postgres", Port: "sql"}}},
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
		Traffic:         Config{TLS: TLSConfig{Routers: []RouterConfig{{Name: "passthrough", EntryPoint: "edge", SNI: "app.example.com", NetworkService: "app", Port: "https"}}}},
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

func TestBuildResolvesStandaloneServiceBackend(t *testing.T) {
	graph, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{"udp": {Address: ":28015", Protocol: domain.EntryPointProtocolUDP}},
		Traffic:     Config{UDP: UDPConfig{Routers: []RouterConfig{{Name: "gameplay", EntryPoint: "udp", Service: "game", Port: "gameplay"}}}},
		Services: []domain.StandaloneService{{
			Name:    "game",
			Image:   "localhost/game-server:latest",
			Enabled: true,
			Ports:   []domain.StandaloneServicePort{{Name: "gameplay", Container: 28015, Protocol: domain.ServicePortProtocolUDP}},
		}},
		ServiceBackends: []domain.ServicePortBackend{{Service: "game", PortName: "gameplay", Host: "127.0.0.1", Port: 38015, Protocol: domain.ServicePortProtocolUDP}},
	})
	require.NoError(t, err)
	require.Contains(t, graph.Services, domain.TrafficService{Name: "service:game:gameplay", Backends: []domain.TrafficBackend{{Name: "game:gameplay", Host: "127.0.0.1", Port: 38015, Protocol: domain.NetworkProtocolUDP}}})
}

func TestBuildRejectsInvalidStandaloneServiceRefs(t *testing.T) {
	base := Input{
		EntryPoints: map[string]EntryPointConfig{"tcp": {Address: ":1234", Protocol: domain.EntryPointProtocolTCP}},
		Services: []domain.StandaloneService{{
			Name:    "game",
			Image:   "localhost/game-server:latest",
			Enabled: true,
			Ports:   []domain.StandaloneServicePort{{Name: "gameplay", Container: 28015, Protocol: domain.ServicePortProtocolUDP}, {Name: "admin", Container: 28016, Protocol: domain.ServicePortProtocolTCP, Private: true, TrustedCIDRs: []string{"100.64.0.0/10"}}},
		}},
		ServiceBackends: []domain.ServicePortBackend{
			{Service: "game", PortName: "gameplay", Host: "127.0.0.1", Port: 38015, Protocol: domain.ServicePortProtocolUDP},
			{Service: "game", PortName: "admin", Host: "127.0.0.1", Port: 38016, Protocol: domain.ServicePortProtocolTCP},
		},
	}

	t.Run("unknown service", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "missing", Port: "admin"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "router \"bad\": unknown service \"missing\"")
	})

	t.Run("unknown port", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "game", Port: "missing"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "unknown port \"missing\" on service \"game\"")
	})

	t.Run("disabled service", func(t *testing.T) {
		input := base
		input.Services = append([]domain.StandaloneService(nil), base.Services...)
		input.Services[0].Enabled = false
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "game", Port: "admin"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "unknown service \"game\"")
	})

	t.Run("protocol mismatch", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "game", Port: "gameplay"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "service \"game\" port \"gameplay\" protocol udp does not match router protocol tcp")
	})

	t.Run("missing runtime backend", func(t *testing.T) {
		input := base
		input.ServiceBackends = nil
		input.Services[0].Ports[1].Private = false
		input.Services[0].Ports[1].Public = true
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", Service: "game", Port: "admin"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "has no resolved runtime backend")
	})
}

func TestBuildWrapsStandaloneServiceValidationErrorsWithServiceName(t *testing.T) {
	_, err := Build(Input{
		EntryPoints: map[string]EntryPointConfig{"tcp": {Address: ":1234", Protocol: domain.EntryPointProtocolTCP}},
		Services: []domain.StandaloneService{{
			Name:    "game",
			Image:   "localhost/game-server:latest",
			Enabled: true,
			Ports:   []domain.StandaloneServicePort{{Name: " admin ", Container: 28016, Protocol: domain.ServicePortProtocolTCP}},
		}},
	})

	require.ErrorContains(t, err, "standalone service \"game\"")
	require.ErrorContains(t, err, "leading or trailing whitespace")
}

func TestBuildEnforcesPrivateStandaloneServicePortRouting(t *testing.T) {
	base := func() Input {
		return Input{
			EntryPoints: map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP, TrustedCIDRs: []string{"100.64.0.0/10"}}},
			Traffic:     Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "game-admin", EntryPoint: "tcp", Service: "game", Port: "admin"}}}},
			Services: []domain.StandaloneService{{
				Name:    "game",
				Image:   "localhost/game-server:latest",
				Enabled: true,
				Ports:   []domain.StandaloneServicePort{{Name: "admin", Container: 28016, Protocol: domain.ServicePortProtocolTCP, Private: true, TrustedCIDRs: []string{"100.64.0.0/10"}}},
			}},
			ServiceBackends: []domain.ServicePortBackend{{Service: "game", PortName: "admin", Host: "127.0.0.1", Port: 38016, Protocol: domain.ServicePortProtocolTCP}},
		}
	}

	t.Run("rcon defaults private and allows exact trusted cidrs", func(t *testing.T) {
		_, err := Build(base())
		require.NoError(t, err)
	})

	t.Run("private port rejects service port without trusted cidrs", func(t *testing.T) {
		input := base()
		input.Services[0].Ports[0].TrustedCIDRs = nil
		_, err := Build(input)
		require.ErrorContains(t, err, "requires non-empty service port trusted_cidrs")
	})

	t.Run("rcon rejects both service and entrypoint without trusted cidrs", func(t *testing.T) {
		input := base()
		input.EntryPoints = map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP}}
		input.Services[0].Ports[0].TrustedCIDRs = nil
		_, err := Build(input)
		require.ErrorContains(t, err, "requires non-empty service port trusted_cidrs")
	})

	t.Run("explicit private non-rcon rejects service port without trusted cidrs", func(t *testing.T) {
		input := base()
		input.Services[0].Ports[0].Name = "management"
		input.Services[0].Ports[0].Private = true
		input.Services[0].Ports[0].TrustedCIDRs = nil
		input.Traffic.TCP.Routers[0].Port = "management"
		input.ServiceBackends[0].PortName = "management"
		_, err := Build(input)
		require.ErrorContains(t, err, "requires non-empty service port trusted_cidrs")
	})

	t.Run("private port rejects entrypoint without trusted cidrs", func(t *testing.T) {
		input := base()
		input.EntryPoints = map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP}}
		_, err := Build(input)
		require.ErrorContains(t, err, "entrypoint \"tcp\" requires non-empty trusted_cidrs")
	})

	t.Run("private port rejects non-matching trusted cidrs", func(t *testing.T) {
		input := base()
		input.EntryPoints = map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP, TrustedCIDRs: []string{"10.0.0.0/8"}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "must exactly match")
	})

	t.Run("private port compares canonical trusted cidrs", func(t *testing.T) {
		input := base()
		input.EntryPoints = map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP, TrustedCIDRs: []string{"100.64.0.1/10"}}}
		_, err := Build(input)
		require.NoError(t, err)
	})

	t.Run("explicit public admin is allowed without trusted cidrs", func(t *testing.T) {
		input := base()
		input.EntryPoints = map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP}}
		input.Services[0].Ports[0].Private = false
		input.Services[0].Ports[0].Public = true
		input.Services[0].Ports[0].TrustedCIDRs = nil
		_, err := Build(input)
		require.NoError(t, err)
	})

	t.Run("explicit public non-rcon is allowed without trusted cidrs", func(t *testing.T) {
		input := base()
		input.EntryPoints = map[string]EntryPointConfig{"tcp": {Address: ":28016", Protocol: domain.EntryPointProtocolTCP}}
		input.Services[0].Ports[0].Name = "management"
		input.Services[0].Ports[0].Private = false
		input.Services[0].Ports[0].Public = true
		input.Services[0].Ports[0].TrustedCIDRs = nil
		input.Traffic.TCP.Routers[0].Port = "management"
		input.ServiceBackends[0].PortName = "management"
		_, err := Build(input)
		require.NoError(t, err)
	})
}

func TestBuildPlanResolvesHTTPServiceRouteWithoutRouteContainer(t *testing.T) {
	plan, err := BuildPlan(Input{
		ServiceRoutes: []domain.HTTPServiceRoute{{Domain: "Play.Example.com", Service: "game", PortName: "web"}},
		Services: []domain.StandaloneService{{
			Name: "game", Enabled: true, Image: "example/game-server:latest",
			Ports: []domain.StandaloneServicePort{{Name: "web", Container: 8080, Protocol: domain.ServicePortProtocolHTTP}},
		}},
		ServiceBackends: []domain.ServicePortBackend{{Service: "game", PortName: "web", Host: "127.0.0.1", Port: 18080, Protocol: domain.ServicePortProtocolHTTP}},
	})

	require.NoError(t, err)
	require.Empty(t, plan.Graph.Routers, "service routes are HTTP proxy targets, not traffic-manager routers")
	require.Equal(t, &domain.ProxyTarget{Host: "127.0.0.1", Port: 18080, Scheme: "http", RouteHost: "play.example.com"}, plan.ServiceTargets["play.example.com"])
}

func TestBuildRejectsInvalidServiceRefs(t *testing.T) {
	base := Input{
		EntryPoints:     map[string]EntryPointConfig{"tcp": {Address: ":1234", Protocol: domain.EntryPointProtocolTCP}},
		NetworkServices: []NetworkServiceConfig{{Name: "svc", Ports: []PortConfig{{Name: "udp", Container: 53, Protocol: domain.NetworkProtocolUDP}}}},
	}

	t.Run("unknown service", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", NetworkService: "missing", Port: "tcp"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "unknown network service")
	})

	t.Run("unknown port", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", NetworkService: "svc", Port: "missing"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "unknown port")
	})

	t.Run("protocol mismatch", func(t *testing.T) {
		input := base
		input.Traffic = Config{TCP: TCPConfig{Routers: []RouterConfig{{Name: "bad", EntryPoint: "tcp", NetworkService: "svc", Port: "udp"}}}}
		_, err := Build(input)
		require.ErrorContains(t, err, "does not match")
	})
}
