package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficGraphValidateEntryPoints(t *testing.T) {
	tests := []struct {
		name    string
		graph   TrafficGraph
		wantErr string
	}{
		{
			name: "duplicate entrypoint names",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "web", Address: ":80", Protocol: EntryPointProtocolHTTP},
				{Name: "web", Address: ":81", Protocol: EntryPointProtocolHTTP},
			}},
			wantErr: "duplicate entrypoint name",
		},
		{
			name: "invalid entrypoint address",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "web", Address: "not-a-listen-address", Protocol: EntryPointProtocolHTTP},
			}},
			wantErr: "invalid entrypoint address",
		},
		{
			name: "invalid entrypoint protocol",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "web", Address: ":80", Protocol: EntryPointProtocol("smtp")},
			}},
			wantErr: "invalid entrypoint protocol",
		},
		{
			name: "duplicate tcp address rejected",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "web", Address: ":443", Protocol: EntryPointProtocolTLSMux},
				{Name: "tcp", Address: ":443", Protocol: EntryPointProtocolTCP},
			}},
			wantErr: "duplicate tcp entrypoint address",
		},
		{
			name: "duplicate udp address rejected",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "dns1", Address: ":53", Protocol: EntryPointProtocolUDP},
				{Name: "dns2", Address: ":53", Protocol: EntryPointProtocolUDP},
			}},
			wantErr: "duplicate udp entrypoint address",
		},
		{
			name: "tcp wildcard address conflicts with ipv4 specific bind",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "wild", Address: ":443", Protocol: EntryPointProtocolTCP},
				{Name: "specific", Address: "127.0.0.1:443", Protocol: EntryPointProtocolTCP},
			}},
			wantErr: "conflicting tcp entrypoint address",
		},
		{
			name: "tcp wildcard address conflicts with ipv6 wildcard bind",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "wild", Address: ":443", Protocol: EntryPointProtocolTCP},
				{Name: "specific", Address: "[::]:443", Protocol: EntryPointProtocolTCP},
			}},
			wantErr: "conflicting tcp entrypoint address",
		},
		{
			name: "tcp and udp same numeric port allowed",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "tcp", Address: ":53", Protocol: EntryPointProtocolTCP},
				{Name: "udp", Address: ":53", Protocol: EntryPointProtocolUDP},
			}},
		},
		{
			name: "invalid trusted cidr rejected",
			graph: TrafficGraph{EntryPoints: []EntryPoint{
				{Name: "tcp", Address: ":5432", Protocol: EntryPointProtocolTCP, TrustedCIDRs: []string{"not-a-cidr"}},
			}},
			wantErr: "invalid trusted_cidrs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.graph.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestTrafficGraphValidateRouterEntryPointProtocolCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		routerProtocol RouterProtocol
		entryProtocol  EntryPointProtocol
		wantErr        bool
	}{
		{name: "http router accepts http entrypoint", routerProtocol: RouterProtocolHTTP, entryProtocol: EntryPointProtocolHTTP},
		{name: "http router accepts tls mux entrypoint", routerProtocol: RouterProtocolHTTP, entryProtocol: EntryPointProtocolTLSMux},
		{name: "tls passthrough router accepts tls mux entrypoint", routerProtocol: RouterProtocolTLSPassthrough, entryProtocol: EntryPointProtocolTLSMux},
		{name: "tcp router accepts tcp entrypoint", routerProtocol: RouterProtocolTCP, entryProtocol: EntryPointProtocolTCP},
		{name: "udp router accepts udp entrypoint", routerProtocol: RouterProtocolUDP, entryProtocol: EntryPointProtocolUDP},
		{name: "http router rejects udp entrypoint", routerProtocol: RouterProtocolHTTP, entryProtocol: EntryPointProtocolUDP, wantErr: true},
		{name: "http router rejects tcp entrypoint", routerProtocol: RouterProtocolHTTP, entryProtocol: EntryPointProtocolTCP, wantErr: true},
		{name: "udp router rejects tcp entrypoint", routerProtocol: RouterProtocolUDP, entryProtocol: EntryPointProtocolTCP, wantErr: true},
		{name: "udp router rejects http entrypoint", routerProtocol: RouterProtocolUDP, entryProtocol: EntryPointProtocolHTTP, wantErr: true},
		{name: "udp router rejects tls mux entrypoint", routerProtocol: RouterProtocolUDP, entryProtocol: EntryPointProtocolTLSMux, wantErr: true},
		{name: "tcp router rejects udp entrypoint", routerProtocol: RouterProtocolTCP, entryProtocol: EntryPointProtocolUDP, wantErr: true},
		{name: "tcp router rejects http entrypoint", routerProtocol: RouterProtocolTCP, entryProtocol: EntryPointProtocolHTTP, wantErr: true},
		{name: "tcp router rejects tls mux entrypoint", routerProtocol: RouterProtocolTCP, entryProtocol: EntryPointProtocolTLSMux, wantErr: true},
		{name: "tls passthrough router rejects http entrypoint", routerProtocol: RouterProtocolTLSPassthrough, entryProtocol: EntryPointProtocolHTTP, wantErr: true},
		{name: "tls passthrough router rejects udp entrypoint", routerProtocol: RouterProtocolTLSPassthrough, entryProtocol: EntryPointProtocolUDP, wantErr: true},
		{name: "tls passthrough router rejects tcp entrypoint per tls mux spec", routerProtocol: RouterProtocolTLSPassthrough, entryProtocol: EntryPointProtocolTCP, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceName := "route:app.example.com"
			backendProtocol := NetworkProtocolTCP
			rule := TrafficRule{Host: "app.example.com"}
			switch tt.routerProtocol {
			case RouterProtocolUDP:
				serviceName = "network_service:app:game"
				backendProtocol = NetworkProtocolUDP
				rule = TrafficRule{}
			case RouterProtocolTCP, RouterProtocolTLSPassthrough:
				serviceName = "network_service:app:tcp"
				rule = TrafficRule{SNI: "app.example.com"}
			}

			graph := TrafficGraph{
				EntryPoints: []EntryPoint{{Name: "entry", Address: ":12345", Protocol: tt.entryProtocol}},
				Routers:     []TrafficRouter{{Name: "router", EntryPoint: "entry", Protocol: tt.routerProtocol, Rule: rule, Service: serviceName}},
				Services:    []TrafficService{{Name: serviceName, Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 8080, Protocol: backendProtocol}}}},
			}
			err := graph.Validate()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "router \"router\" protocol "+string(tt.routerProtocol)+" is incompatible with entrypoint \"entry\" protocol "+string(tt.entryProtocol))
		})
	}
}

func TestTrafficGraphValidateRouterNames(t *testing.T) {
	service := TrafficService{Name: "route:app.example.com", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 8080, Protocol: NetworkProtocolTCP}}}
	tests := []struct {
		name    string
		routers []TrafficRouter
		wantErr string
	}{
		{
			name:    "router name is required",
			routers: []TrafficRouter{{EntryPoint: "web", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: service.Name}},
			wantErr: "traffic router name is required",
		},
		{
			name: "duplicate router names rejected",
			routers: []TrafficRouter{
				{Name: "web", EntryPoint: "web", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: service.Name},
				{Name: "web", EntryPoint: "web", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "api.example.com"}, Service: service.Name},
			},
			wantErr: "duplicate traffic router name \"web\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := TrafficGraph{
				EntryPoints: []EntryPoint{{Name: "web", Address: ":80", Protocol: EntryPointProtocolHTTP}},
				Routers:     tt.routers,
				Services:    []TrafficService{service},
			}
			require.ErrorContains(t, graph.Validate(), tt.wantErr)
		})
	}
}

func TestTrafficGraphValidateDuplicateHTTPHosts(t *testing.T) {
	service := TrafficService{Name: "route:app.example.com", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 8080, Protocol: NetworkProtocolTCP}}}
	tests := []struct {
		name          string
		entryProtocol EntryPointProtocol
		entryAddress  string
	}{
		{name: "http entrypoint", entryProtocol: EntryPointProtocolHTTP, entryAddress: ":80"},
		{name: "tls mux entrypoint", entryProtocol: EntryPointProtocolTLSMux, entryAddress: ":443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := TrafficGraph{
				EntryPoints: []EntryPoint{{Name: "web", Address: tt.entryAddress, Protocol: tt.entryProtocol}},
				Routers: []TrafficRouter{
					{Name: "one", EntryPoint: "web", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "App.Example.Com."}, Service: service.Name},
					{Name: "two", EntryPoint: "web", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: service.Name},
				},
				Services: []TrafficService{service},
			}
			require.ErrorContains(t, graph.Validate(), "duplicate http host \"app.example.com\" on entrypoint \"web\"")
		})
	}
}

func TestTrafficGraphValidateAmbiguousL4Routers(t *testing.T) {
	tests := []struct {
		name           string
		entryProtocol  EntryPointProtocol
		routerProtocol RouterProtocol
		service        TrafficService
		wantErr        string
	}{
		{
			name:           "multiple tcp routers on same entrypoint rejected",
			entryProtocol:  EntryPointProtocolTCP,
			routerProtocol: RouterProtocolTCP,
			service:        TrafficService{Name: "network_service:app:db", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 5432, Protocol: NetworkProtocolTCP}}},
			wantErr:        "ambiguous tcp routers on entrypoint \"l4\"",
		},
		{
			name:           "multiple udp routers on same entrypoint rejected",
			entryProtocol:  EntryPointProtocolUDP,
			routerProtocol: RouterProtocolUDP,
			service:        TrafficService{Name: "network_service:app:game", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 25565, Protocol: NetworkProtocolUDP}}},
			wantErr:        "ambiguous udp routers on entrypoint \"l4\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := TrafficGraph{
				EntryPoints: []EntryPoint{{Name: "l4", Address: ":12345", Protocol: tt.entryProtocol}},
				Routers: []TrafficRouter{
					{Name: "one", EntryPoint: "l4", Protocol: tt.routerProtocol, Service: tt.service.Name},
					{Name: "two", EntryPoint: "l4", Protocol: tt.routerProtocol, Service: tt.service.Name},
				},
				Services: []TrafficService{tt.service},
			}
			require.ErrorContains(t, graph.Validate(), tt.wantErr)
		})
	}
}

func TestTrafficGraphValidateTLSRules(t *testing.T) {
	tests := []struct {
		name    string
		routers []TrafficRouter
		wantErr string
	}{
		{
			name: "duplicate exact tls sni on same entrypoint rejected",
			routers: []TrafficRouter{
				{Name: "one", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "db.example.com"}, Service: "network_service:app:https"},
				{Name: "two", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "db.example.com"}, Service: "network_service:app:https"},
			},
			wantErr: "duplicate exact tls sni",
		},
		{
			name: "exact sni plus wildcard deterministic allowed",
			routers: []TrafficRouter{
				{Name: "exact", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "db.example.com"}, Service: "network_service:app:https"},
				{Name: "wild", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.example.com"}, Service: "network_service:app:https"},
			},
		},
		{
			name: "duplicate wildcard sni rejected",
			routers: []TrafficRouter{
				{Name: "wild1", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.example.com"}, Service: "network_service:app:https"},
				{Name: "wild2", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.example.com"}, Service: "network_service:app:https"},
			},
			wantErr: "ambiguous wildcard tls sni",
		},
		{
			name: "nested wildcard sni overlap rejected",
			routers: []TrafficRouter{
				{Name: "wild1", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.example.com"}, Service: "network_service:app:https"},
				{Name: "wild2", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.sub.example.com"}, Service: "network_service:app:https"},
			},
			wantErr: "ambiguous wildcard tls sni",
		},
		{
			name: "tls passthrough requires sni",
			routers: []TrafficRouter{
				{Name: "pass", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Service: "network_service:app:https"},
			},
			wantErr: "requires sni",
		},
		{
			name: "http host and tls passthrough sni conflict rejected",
			routers: []TrafficRouter{
				{Name: "http", EntryPoint: "tls", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"},
				{Name: "pass", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "app.example.com"}, Service: "network_service:app:https"},
			},
			wantErr: "http host conflicts with tls passthrough sni",
		},
		{
			name: "http host conflicts with later wildcard tls passthrough sni",
			routers: []TrafficRouter{
				{Name: "http", EntryPoint: "tls", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"},
				{Name: "pass", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.example.com"}, Service: "network_service:app:https"},
			},
			wantErr: "http host conflicts with wildcard tls passthrough sni",
		},
		{
			name: "http host conflicts with earlier wildcard tls passthrough sni",
			routers: []TrafficRouter{
				{Name: "pass", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "*.example.com"}, Service: "network_service:app:https"},
				{Name: "http", EntryPoint: "tls", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: "route:app.example.com"},
			},
			wantErr: "http host conflicts with wildcard tls passthrough sni",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := TrafficGraph{
				EntryPoints: []EntryPoint{{Name: "tls", Address: ":443", Protocol: EntryPointProtocolTLSMux}},
				Routers:     tt.routers,
				Services: []TrafficService{
					{Name: "network_service:app:https", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 443, Protocol: NetworkProtocolTCP}}},
					{Name: "route:app.example.com", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.3", Port: 8080, Protocol: NetworkProtocolTCP}}},
				},
			}
			err := graph.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestTrafficGraphValidateBackendsAndL4Routers(t *testing.T) {
	tests := []struct {
		name     string
		router   TrafficRouter
		services []TrafficService
		wantErr  string
	}{
		{
			name:     "backend host required",
			router:   TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:db"},
			services: []TrafficService{{Name: "network_service:app:db", Backends: []TrafficBackend{{Name: "app", Port: 5432, Protocol: NetworkProtocolTCP}}}},
			wantErr:  "backend host is required",
		},
		{
			name:     "backend ports outside range rejected low",
			router:   TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:db"},
			services: []TrafficService{{Name: "network_service:app:db", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 0, Protocol: NetworkProtocolTCP}}}},
			wantErr:  "invalid backend port",
		},
		{
			name:     "backend ports outside range rejected high",
			router:   TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:db"},
			services: []TrafficService{{Name: "network_service:app:db", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 65536, Protocol: NetworkProtocolTCP}}}},
			wantErr:  "invalid backend port",
		},
		{
			name:   "l4 routers reject services with more than one backend",
			router: TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:db"},
			services: []TrafficService{{Name: "network_service:app:db", Backends: []TrafficBackend{
				{Name: "app", Host: "10.0.0.2", Port: 5432, Protocol: NetworkProtocolTCP},
				{Name: "app2", Host: "10.0.0.3", Port: 5432, Protocol: NetworkProtocolTCP},
			}}},
			wantErr: "l4 router service must have exactly one backend",
		},
		{
			name:     "tcp router requires tcp network backend",
			router:   TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:db"},
			services: []TrafficService{{Name: "network_service:app:db", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 53, Protocol: NetworkProtocolUDP}}}},
			wantErr:  "backend protocol udp does not match router protocol tcp",
		},
		{
			name:     "udp router requires udp network backend",
			router:   TrafficRouter{Name: "udp", EntryPoint: "udp", Protocol: RouterProtocolUDP, Service: "network_service:app:game"},
			services: []TrafficService{{Name: "network_service:app:game", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 53, Protocol: NetworkProtocolTCP}}}},
			wantErr:  "backend protocol tcp does not match router protocol udp",
		},
		{
			name:     "tls passthrough requires tcp network backend",
			router:   TrafficRouter{Name: "tls", EntryPoint: "tls", Protocol: RouterProtocolTLSPassthrough, Rule: TrafficRule{SNI: "db.example.com"}, Service: "network_service:app:https"},
			services: []TrafficService{{Name: "network_service:app:https", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 443, Protocol: NetworkProtocolUDP}}}},
			wantErr:  "backend protocol udp does not match router protocol tls_passthrough",
		},
		{
			name:     "l4 router rejects route ref",
			router:   TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "route:app.example.com"},
			services: []TrafficService{{Name: "route:app.example.com", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 443, Protocol: NetworkProtocolTCP}}}},
			wantErr:  "requires network_service service ref",
		},
		{
			name:     "static ref parses but graph validation rejects unsupported",
			router:   TrafficRouter{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "static:maintenance"},
			services: []TrafficService{{Name: "static:maintenance", Backends: []TrafficBackend{{Name: "maintenance", Host: "10.0.0.2", Port: 443, Protocol: NetworkProtocolTCP}}}},
			wantErr:  "static traffic service ref \"static:maintenance\" is unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := TrafficGraph{
				EntryPoints: []EntryPoint{
					{Name: "tcp", Address: ":443", Protocol: EntryPointProtocolTCP},
					{Name: "udp", Address: ":443", Protocol: EntryPointProtocolUDP},
					{Name: "tls", Address: ":8443", Protocol: EntryPointProtocolTLSMux},
				},
				Routers:  []TrafficRouter{tt.router},
				Services: tt.services,
			}
			require.ErrorContains(t, graph.Validate(), tt.wantErr)
		})
	}
}

func TestTrafficGraphValidateHTTPServiceRefs(t *testing.T) {
	tests := []struct {
		name    string
		service string
		wantErr string
	}{
		{name: "http allows route", service: "route:app.example.com"},
		{name: "http allows external_route", service: "external_route:api.example.com"},
		{name: "http rejects network_service", service: "network_service:app:http", wantErr: "does not support service ref kind network_service"},
		{name: "http rejects static", service: "static:maintenance", wantErr: "static traffic service ref \"static:maintenance\" is unsupported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := TrafficGraph{
				EntryPoints: []EntryPoint{{Name: "web", Address: ":80", Protocol: EntryPointProtocolHTTP}},
				Routers:     []TrafficRouter{{Name: "web", EntryPoint: "web", Protocol: RouterProtocolHTTP, Rule: TrafficRule{Host: "app.example.com"}, Service: tt.service}},
				Services:    []TrafficService{{Name: tt.service, Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 8080, Protocol: NetworkProtocolTCP}}}},
			}
			err := graph.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestTrafficGraphValidateRouterServiceRefs(t *testing.T) {
	t.Run("router service ref is parsed", func(t *testing.T) {
		graph := TrafficGraph{
			EntryPoints: []EntryPoint{{Name: "tcp", Address: ":5432", Protocol: EntryPointProtocolTCP}},
			Routers:     []TrafficRouter{{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:postgres:"}},
			Services:    []TrafficService{{Name: "network_service:postgres:", Backends: []TrafficBackend{{Name: "postgres", Host: "10.0.0.4", Port: 5432, Protocol: NetworkProtocolTCP}}}},
		}
		require.ErrorContains(t, graph.Validate(), "invalid traffic service ref")
	})

	t.Run("router service ref must exist as service name", func(t *testing.T) {
		graph := TrafficGraph{
			EntryPoints: []EntryPoint{{Name: "tcp", Address: ":5432", Protocol: EntryPointProtocolTCP}},
			Routers:     []TrafficRouter{{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:postgres:db"}},
		}
		require.ErrorContains(t, graph.Validate(), "references unknown service")
	})
}

func TestTrafficServiceRef(t *testing.T) {
	ref, err := ParseTrafficServiceRef("static:maintenance")
	require.NoError(t, err)
	require.Equal(t, TrafficServiceRefStatic, ref.Kind)
	require.True(t, ref.Reserved)
	require.Equal(t, "maintenance", ref.Name)
}

func TestParseTrafficServiceRef(t *testing.T) {
	tests := []struct {
		input string
		want  TrafficServiceRef
	}{
		{input: "route:app.example.com", want: TrafficServiceRef{Kind: TrafficServiceRefRoute, Domain: "app.example.com"}},
		{input: "external_route:api.example.com", want: TrafficServiceRef{Kind: TrafficServiceRefExternalRoute, Domain: "api.example.com"}},
		{input: "network_service:postgres:db", want: TrafficServiceRef{Kind: TrafficServiceRefNetworkService, Name: "postgres", PortName: "db"}},
		{input: "network_service:web:http", want: TrafficServiceRef{Kind: TrafficServiceRefNetworkService, Name: "web", PortName: "http"}},
		{input: "network_service:game:game", want: TrafficServiceRef{Kind: TrafficServiceRefNetworkService, Name: "game", PortName: "game"}},
		{input: "static:maintenance", want: TrafficServiceRef{Kind: TrafficServiceRefStatic, Name: "maintenance", Reserved: true}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseTrafficServiceRef(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	invalid := []string{"", "route:", "external_route:", "network_service:", "network_service:postgres", "network_service:postgres:", "network_service::db", "network_service:postgres:db:extra", "static:", "unknown:value"}
	for _, input := range invalid {
		t.Run("invalid "+input, func(t *testing.T) {
			_, err := ParseTrafficServiceRef(input)
			require.Error(t, err)
		})
	}
}

func TestTrafficDomainTypesMatchSpecFields(t *testing.T) {
	graph := TrafficGraph{
		Options: TrafficOptions{
			TCP: TCPOptions{DialTimeout: time.Second, IdleTimeout: time.Minute, DrainTimeout: 2 * time.Second, MaxConnections: 100},
			UDP: UDPOptions{IdleTimeout: time.Minute, DrainTimeout: 2 * time.Second, MaxSessions: 50},
		},
		EntryPoints: []EntryPoint{{Name: "tcp", Address: ":1234", Protocol: EntryPointProtocolTCP, TrustedCIDRs: []string{"10.0.0.0/8"}}},
		Routers:     []TrafficRouter{{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:game"}},
		Services:    []TrafficService{{Name: "network_service:app:game", Backends: []TrafficBackend{{Name: "app", Host: "10.0.0.2", Port: 1234, Protocol: NetworkProtocolTCP}}}},
	}
	require.NoError(t, graph.Validate())

	status := TrafficStatus{
		LastReloadStatus: "ok",
		LastReloadError:  "",
		EntryPoints:      []EntryPointStatus{{Name: "tcp", ActiveTCPConnections: 1, ActiveUDPSessions: 2, TotalAccepted: 3, TotalRefused: 4, TotalErrors: 5, BytesIn: 6, BytesOut: 7}},
		Routers:          []TrafficRouterStatus{{Name: "tcp", EntryPoint: "tcp", Protocol: RouterProtocolTCP, Service: "network_service:app:game", Active: true}},
		Services:         []TrafficServiceStatus{{Name: "network_service:app:game", Backends: []TrafficBackendStatus{{Name: "app", Host: "10.0.0.2", Port: 1234, Protocol: NetworkProtocolTCP, Active: true}}}},
		Counters:         TrafficCounters{ActiveTCPConnections: 1, ActiveUDPSessions: 2, TotalAccepted: 3, TotalRefused: 4, TotalErrors: 5, BytesIn: 6, BytesOut: 7},
	}
	require.Equal(t, "ok", status.LastReloadStatus)
	require.Empty(t, status.LastReloadError)
	require.Equal(t, int64(7), status.EntryPoints[0].BytesOut)
	require.True(t, status.Routers[0].Active)
	require.True(t, status.Services[0].Backends[0].Active)
	require.Equal(t, int64(7), status.Counters.BytesOut)
}
