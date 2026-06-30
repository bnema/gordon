package traffic

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const DefaultEdgeEntryPointName = "edge"

// Input is the complete, usecase-level input used to build a validated traffic graph.
type Input struct {
	EntryPoints     map[string]EntryPointConfig
	Traffic         Config
	Routes          []domain.Route
	ExternalRoutes  map[string]string
	NetworkServices []NetworkServiceConfig
}

type EntryPointConfig struct {
	Address                 string                    `mapstructure:"address"`
	Protocol                domain.EntryPointProtocol `mapstructure:"protocol"`
	TrustedCIDRs            []string                  `mapstructure:"trusted_cidrs"`
	RawFallback             string                    `mapstructure:"raw_fallback"`
	RawFallbackTrustedCIDRs []string                  `mapstructure:"raw_fallback_trusted_cidrs"`
	AllowPublicRawFallback  bool                      `mapstructure:"allow_public_raw_fallback"`
}

// Config holds traffic router and option configuration.
type Config struct {
	TCP TCPConfig `mapstructure:"tcp"`
	UDP UDPConfig `mapstructure:"udp"`
	TLS TLSConfig `mapstructure:"tls"`
}

type TCPConfig struct {
	Routers        []RouterConfig `mapstructure:"routers"`
	DialTimeout    string         `mapstructure:"dial_timeout"`
	IdleTimeout    string         `mapstructure:"idle_timeout"`
	DrainTimeout   string         `mapstructure:"drain_timeout"`
	MaxConnections int            `mapstructure:"max_connections"`
}

type UDPConfig struct {
	Routers      []RouterConfig `mapstructure:"routers"`
	IdleTimeout  string         `mapstructure:"idle_timeout"`
	DrainTimeout string         `mapstructure:"drain_timeout"`
	MaxSessions  int            `mapstructure:"max_sessions"`
}

type TLSConfig struct {
	Routers []RouterConfig `mapstructure:"routers"`
}

type RouterConfig struct {
	Name       string `mapstructure:"name"`
	EntryPoint string `mapstructure:"entrypoint"`
	Host       string `mapstructure:"host"`
	SNI        string `mapstructure:"sni"`
	Service    string `mapstructure:"service"`
}

type NetworkServiceConfig struct {
	Name  string       `mapstructure:"name"`
	Ports []PortConfig `mapstructure:"ports"`
}

type PortConfig struct {
	Name      string                 `mapstructure:"name"`
	Container int                    `mapstructure:"container"`
	Protocol  domain.NetworkProtocol `mapstructure:"protocol"`
}

// Build builds and validates a TrafficGraph snapshot. Managed route services are
// intentionally backend-less because their concrete container endpoints are resolved at runtime.
func Build(input Input) (domain.TrafficGraph, error) {
	b := builder{input: input, services: map[string]domain.TrafficService{}}
	graph := domain.TrafficGraph{}

	options, err := buildOptions(input.Traffic)
	if err != nil {
		return domain.TrafficGraph{}, fmt.Errorf("build traffic options: %w", err)
	}
	if err := validateNetworkServices(input.NetworkServices); err != nil {
		return domain.TrafficGraph{}, fmt.Errorf("validate network services: %w", err)
	}
	graph.Options = options
	graph.EntryPoints = b.buildEntryPoints()

	if err := b.addHTTPRoutes(&graph); err != nil {
		return domain.TrafficGraph{}, fmt.Errorf("add HTTP routes: %w", err)
	}
	if err := b.addExternalRoutes(&graph); err != nil {
		return domain.TrafficGraph{}, fmt.Errorf("add external routes: %w", err)
	}
	if err := b.addExplicitRouters(&graph); err != nil {
		return domain.TrafficGraph{}, fmt.Errorf("add explicit traffic routers: %w", err)
	}

	graph.Services = make([]domain.TrafficService, 0, len(b.services))
	for _, svc := range b.services {
		graph.Services = append(graph.Services, svc)
	}
	sort.Slice(graph.Services, func(i, j int) bool { return graph.Services[i].Name < graph.Services[j].Name })
	if err := graph.Validate(); err != nil {
		return domain.TrafficGraph{}, fmt.Errorf("validate traffic graph: %w", err)
	}
	return graph, nil
}

type builder struct {
	input    Input
	services map[string]domain.TrafficService
}

func (b *builder) buildEntryPoints() []domain.EntryPoint {
	entries := make([]domain.EntryPoint, 0, len(b.input.EntryPoints))
	for _, name := range sortedKeys(b.input.EntryPoints) {
		cfg := b.input.EntryPoints[name]
		entries = append(entries, domain.EntryPoint{
			Name:                    name,
			Address:                 cfg.Address,
			Protocol:                cfg.Protocol,
			TrustedCIDRs:            append([]string(nil), cfg.TrustedCIDRs...),
			RawFallback:             cfg.RawFallback,
			RawFallbackTrustedCIDRs: append([]string(nil), cfg.RawFallbackTrustedCIDRs...),
			AllowPublicRawFallback:  cfg.AllowPublicRawFallback,
		})
	}
	return entries
}

func (b *builder) addHTTPRoutes(graph *domain.TrafficGraph) error {
	routes := append([]domain.Route{}, b.input.Routes...)
	if len(routes) == 0 {
		return nil
	}
	entryPoint, err := b.routeEntryPoint()
	if err != nil {
		return err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Domain < routes[j].Domain })
	for _, route := range routes {
		serviceName := string(domain.TrafficServiceRefRoute) + ":" + route.Domain
		b.addService(domain.TrafficService{Name: serviceName})
		graph.Routers = append(graph.Routers, domain.TrafficRouter{Name: "route:" + route.Domain, EntryPoint: entryPoint, Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: route.Domain}, Service: serviceName})
	}
	return nil
}

func (b *builder) addExternalRoutes(graph *domain.TrafficGraph) error {
	hostNames := sortedKeys(b.input.ExternalRoutes)
	if len(hostNames) == 0 {
		return nil
	}
	entryPoint, err := b.routeEntryPoint()
	if err != nil {
		return err
	}
	for _, hostName := range hostNames {
		target := b.input.ExternalRoutes[hostName]
		host, port, err := parseBackendAddress(target)
		if err != nil {
			return fmt.Errorf("external route %q target %q: %w", hostName, target, err)
		}
		serviceName := string(domain.TrafficServiceRefExternalRoute) + ":" + hostName
		b.addService(domain.TrafficService{Name: serviceName, Backends: []domain.TrafficBackend{{Name: hostName, Host: host, Port: port, Protocol: domain.NetworkProtocolTCP}}})
		graph.Routers = append(graph.Routers, domain.TrafficRouter{Name: "external_route:" + hostName, EntryPoint: entryPoint, Protocol: domain.RouterProtocolHTTP, Rule: domain.TrafficRule{Host: hostName}, Service: serviceName})
	}
	return nil
}

func (b *builder) routeEntryPoint() (string, error) {
	if cfg, ok := b.input.EntryPoints[DefaultEdgeEntryPointName]; ok && routeCapableEntryPoint(cfg.Protocol) {
		return DefaultEdgeEntryPointName, nil
	}

	candidates := make([]string, 0)
	for _, name := range sortedKeys(b.input.EntryPoints) {
		if routeCapableEntryPoint(b.input.EntryPoints[name].Protocol) {
			candidates = append(candidates, name)
		}
	}
	switch len(candidates) {
	case 0:
		return DefaultEdgeEntryPointName, nil
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple route-capable entrypoints configured without %q: %s", DefaultEdgeEntryPointName, strings.Join(candidates, ", "))
	}
}

func routeCapableEntryPoint(protocol domain.EntryPointProtocol) bool {
	switch protocol {
	case domain.EntryPointProtocolSmartTCP, domain.EntryPointProtocolTLSMux:
		return true
	default:
		return false
	}
}

func (b *builder) addExplicitRouters(graph *domain.TrafficGraph) error {
	if err := b.addL4Routers(graph, b.input.Traffic.TCP.Routers, domain.RouterProtocolTCP, domain.NetworkProtocolTCP); err != nil {
		return err
	}
	if err := b.addL4Routers(graph, b.input.Traffic.UDP.Routers, domain.RouterProtocolUDP, domain.NetworkProtocolUDP); err != nil {
		return err
	}
	return b.addL4Routers(graph, b.input.Traffic.TLS.Routers, domain.RouterProtocolTLSPassthrough, domain.NetworkProtocolTCP)
}

func (b *builder) addL4Routers(graph *domain.TrafficGraph, routers []RouterConfig, protocol domain.RouterProtocol, backendProtocol domain.NetworkProtocol) error {
	ordered := append([]RouterConfig{}, routers...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	for _, cfg := range ordered {
		service, err := b.resolveService(cfg.Service, backendProtocol)
		if err != nil {
			return fmt.Errorf("router %q: %w", cfg.Name, err)
		}
		b.addService(service)
		graph.Routers = append(graph.Routers, domain.TrafficRouter{Name: cfg.Name, EntryPoint: cfg.EntryPoint, Protocol: protocol, Rule: domain.TrafficRule{Host: cfg.Host, SNI: cfg.SNI}, Service: cfg.Service})
	}
	return nil
}

func (b *builder) resolveService(refValue string, protocol domain.NetworkProtocol) (domain.TrafficService, error) {
	ref, err := domain.ParseTrafficServiceRef(refValue)
	if err != nil {
		return domain.TrafficService{}, err
	}
	if ref.Kind == domain.TrafficServiceRefStatic {
		return domain.TrafficService{}, fmt.Errorf("static service ref %q is unsupported", refValue)
	}
	if ref.Kind != domain.TrafficServiceRefNetworkService {
		return domain.TrafficService{}, fmt.Errorf("service ref %q must be network_service:<name>:<port-name>", refValue)
	}
	ns, ok := b.networkService(ref.Name)
	if !ok {
		return domain.TrafficService{}, fmt.Errorf("unknown network service %q", ref.Name)
	}
	port, ok := networkServicePort(ns, ref.PortName)
	if !ok {
		return domain.TrafficService{}, fmt.Errorf("unknown port %q on network service %q", ref.PortName, ref.Name)
	}
	if port.Container < 1 || port.Container > 65535 {
		return domain.TrafficService{}, fmt.Errorf("invalid port %d on network service %q port %q", port.Container, ref.Name, ref.PortName)
	}
	if port.Protocol != protocol {
		return domain.TrafficService{}, fmt.Errorf("network service %q port %q protocol %s does not match router protocol %s", ref.Name, ref.PortName, port.Protocol, protocol)
	}
	return domain.TrafficService{Name: refValue, Backends: []domain.TrafficBackend{{Name: ref.Name + ":" + ref.PortName, Host: ref.Name, Port: port.Container, Protocol: port.Protocol}}}, nil
}

func validateNetworkServices(services []NetworkServiceConfig) error {
	seen := map[string]struct{}{}
	for _, service := range services {
		if service.Name == "" {
			return fmt.Errorf("network service name is required")
		}
		if _, exists := seen[service.Name]; exists {
			return fmt.Errorf("duplicate network service %q", service.Name)
		}
		seen[service.Name] = struct{}{}
		if err := validateNetworkServicePorts(service); err != nil {
			return err
		}
	}
	return nil
}

func validateNetworkServicePorts(service NetworkServiceConfig) error {
	seen := map[string]struct{}{}
	for _, port := range service.Ports {
		if port.Name == "" {
			return fmt.Errorf("network service %q port name is required", service.Name)
		}
		if _, exists := seen[port.Name]; exists {
			return fmt.Errorf("duplicate port %q on network service %q", port.Name, service.Name)
		}
		if port.Container < 1 || port.Container > 65535 {
			return fmt.Errorf("invalid container port %d on network service %q port %q", port.Container, service.Name, port.Name)
		}
		if err := validateNetworkServicePortProtocol(port.Protocol); err != nil {
			return fmt.Errorf("invalid protocol for network service %q port %q: %w", service.Name, port.Name, err)
		}
		seen[port.Name] = struct{}{}
	}
	return nil
}

func validateNetworkServicePortProtocol(protocol domain.NetworkProtocol) error {
	switch protocol {
	case domain.NetworkProtocolTCP, domain.NetworkProtocolUDP:
		return nil
	default:
		return fmt.Errorf("%q", protocol)
	}
}

func (b *builder) networkService(name string) (NetworkServiceConfig, bool) {
	for _, service := range b.input.NetworkServices {
		if service.Name == name {
			return service, true
		}
	}
	return NetworkServiceConfig{}, false
}

func networkServicePort(service NetworkServiceConfig, name string) (PortConfig, bool) {
	for _, port := range service.Ports {
		if port.Name == name {
			return port, true
		}
	}
	return PortConfig{}, false
}

func (b *builder) addService(service domain.TrafficService) {
	if _, exists := b.services[service.Name]; !exists {
		b.services[service.Name] = service
	}
}

func buildOptions(cfg Config) (domain.TrafficOptions, error) {
	tcpDial, err := parsePositiveDurationDefault(cfg.TCP.DialTimeout, 10*time.Second, "traffic.tcp.dial_timeout")
	if err != nil {
		return domain.TrafficOptions{}, err
	}
	tcpIdle, err := parsePositiveDurationDefault(cfg.TCP.IdleTimeout, 5*time.Minute, "traffic.tcp.idle_timeout")
	if err != nil {
		return domain.TrafficOptions{}, err
	}
	tcpDrain, err := parsePositiveDurationDefault(cfg.TCP.DrainTimeout, 30*time.Second, "traffic.tcp.drain_timeout")
	if err != nil {
		return domain.TrafficOptions{}, err
	}
	udpIdle, err := parsePositiveDurationDefault(cfg.UDP.IdleTimeout, 30*time.Second, "traffic.udp.idle_timeout")
	if err != nil {
		return domain.TrafficOptions{}, err
	}
	udpDrain, err := parsePositiveDurationDefault(cfg.UDP.DrainTimeout, 30*time.Second, "traffic.udp.drain_timeout")
	if err != nil {
		return domain.TrafficOptions{}, err
	}
	return domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: tcpDial, IdleTimeout: tcpIdle, DrainTimeout: tcpDrain, MaxConnections: cfg.TCP.MaxConnections}, UDP: domain.UDPOptions{IdleTimeout: udpIdle, DrainTimeout: udpDrain, MaxSessions: cfg.UDP.MaxSessions}}, nil
}

func parsePositiveDurationDefault(value string, fallback time.Duration, field string) (time.Duration, error) {
	d, err := parseDurationDefault(value, fallback, field)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid %s: must be positive", field)
	}
	return d, nil
}

func parseDurationDefault(value string, fallback time.Duration, field string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", field, err)
	}
	return d, nil
}

func parseBackendAddress(value string) (string, int, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		lastColon := strings.LastIndex(value, ":")
		if lastColon <= 0 || lastColon == len(value)-1 || strings.Count(value, ":") > 1 {
			return "", 0, err
		}
		host, portText = value[:lastColon], value[lastColon+1:]
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}
	if host == "" || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("host and valid port are required")
	}
	return host, port, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
