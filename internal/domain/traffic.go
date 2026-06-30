package domain

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type EntryPointProtocol string

const (
	EntryPointProtocolHTTP   EntryPointProtocol = "http"
	EntryPointProtocolTLSMux EntryPointProtocol = "tls_mux"
	EntryPointProtocolTCP    EntryPointProtocol = "tcp"
	EntryPointProtocolUDP    EntryPointProtocol = "udp"
)

type RouterProtocol string

const (
	RouterProtocolHTTP           RouterProtocol = "http"
	RouterProtocolTCP            RouterProtocol = "tcp"
	RouterProtocolUDP            RouterProtocol = "udp"
	RouterProtocolTLSPassthrough RouterProtocol = "tls_passthrough"
)

type NetworkProtocol string

const (
	NetworkProtocolTCP NetworkProtocol = "tcp"
	NetworkProtocolUDP NetworkProtocol = "udp"
)

type TrafficGraph struct {
	Options     TrafficOptions
	EntryPoints []EntryPoint
	Routers     []TrafficRouter
	Services    []TrafficService
}

type TrafficOptions struct {
	TCP TCPOptions
	UDP UDPOptions
}

type TCPOptions struct {
	DialTimeout    time.Duration
	IdleTimeout    time.Duration
	DrainTimeout   time.Duration
	MaxConnections int
}

type UDPOptions struct {
	IdleTimeout  time.Duration
	DrainTimeout time.Duration
	MaxSessions  int
}

type EntryPoint struct {
	Name         string
	Address      string
	Protocol     EntryPointProtocol
	TrustedCIDRs []string
}

type TrafficRouter struct {
	Name       string
	EntryPoint string
	Protocol   RouterProtocol
	Rule       TrafficRule
	Service    string
}

type TrafficRule struct {
	Host string
	SNI  string
}

type TrafficService struct {
	Name     string
	Backends []TrafficBackend
}

type TrafficBackend struct {
	Name     string
	Host     string
	Port     int
	Protocol NetworkProtocol
}

type TrafficStatus struct {
	LastReloadStatus string
	LastReloadError  string
	EntryPoints      []EntryPointStatus
	Routers          []TrafficRouterStatus
	Services         []TrafficServiceStatus
	Counters         TrafficCounters
}

type EntryPointStatus struct {
	Name                 string
	Address              string
	Protocol             EntryPointProtocol
	Active               bool
	ActiveTCPConnections int64
	ActiveUDPSessions    int64
	TotalAccepted        int64
	TotalRefused         int64
	TotalErrors          int64
	BytesIn              int64
	BytesOut             int64
}

type TrafficRouterStatus struct {
	Name       string
	EntryPoint string
	Protocol   RouterProtocol
	Rule       TrafficRule
	Service    string
	Active     bool
}

type TrafficServiceStatus struct {
	Name     string
	Active   bool
	Backends []TrafficBackendStatus
}

type TrafficBackendStatus struct {
	Name     string
	Host     string
	Port     int
	Protocol NetworkProtocol
	Active   bool
}

type TrafficCounters struct {
	ActiveTCPConnections int64
	ActiveUDPSessions    int64
	TotalAccepted        int64
	TotalRefused         int64
	TotalErrors          int64
	BytesIn              int64
	BytesOut             int64
}

type TrafficServiceRefKind string

const (
	TrafficServiceRefRoute          TrafficServiceRefKind = "route"
	TrafficServiceRefExternalRoute  TrafficServiceRefKind = "external_route"
	TrafficServiceRefNetworkService TrafficServiceRefKind = "network_service"
	TrafficServiceRefStatic         TrafficServiceRefKind = "static"
)

type TrafficServiceRef struct {
	Kind     TrafficServiceRefKind
	Domain   string
	Name     string
	PortName string
	Reserved bool
}

func ParseTrafficServiceRef(value string) (TrafficServiceRef, error) {
	kind, rest, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || rest == "" {
		return TrafficServiceRef{}, fmt.Errorf("invalid traffic service ref %q", value)
	}
	switch TrafficServiceRefKind(kind) {
	case TrafficServiceRefRoute:
		return TrafficServiceRef{Kind: TrafficServiceRefRoute, Domain: rest}, nil
	case TrafficServiceRefExternalRoute:
		return TrafficServiceRef{Kind: TrafficServiceRefExternalRoute, Domain: rest}, nil
	case TrafficServiceRefNetworkService:
		name, portName, ok := strings.Cut(rest, ":")
		if !ok || name == "" || portName == "" || strings.Contains(portName, ":") {
			return TrafficServiceRef{}, fmt.Errorf("invalid network service ref %q", value)
		}
		return TrafficServiceRef{Kind: TrafficServiceRefNetworkService, Name: name, PortName: portName}, nil
	case TrafficServiceRefStatic:
		return TrafficServiceRef{Kind: TrafficServiceRefStatic, Name: rest, Reserved: true}, nil
	default:
		return TrafficServiceRef{}, fmt.Errorf("invalid traffic service ref kind %q", kind)
	}
}

func (g TrafficGraph) Validate() error {
	entryPoints, err := validateTrafficEntryPoints(g.EntryPoints)
	if err != nil {
		return err
	}
	services, err := validateTrafficServices(g.Services)
	if err != nil {
		return err
	}
	return g.validateRouters(entryPoints, services)
}

func validateTrafficEntryPoints(entries []EntryPoint) (map[string]EntryPoint, error) {
	entryPoints := make(map[string]EntryPoint, len(entries))
	listenAddresses := newProtocolListenAddressSet()
	for _, entryPoint := range entries {
		if err := validateTrafficEntryPoint(entryPoint, entryPoints, listenAddresses); err != nil {
			return nil, err
		}
		entryPoints[entryPoint.Name] = entryPoint
	}
	return entryPoints, nil
}

type protocolListenAddressSet struct {
	tcp listenAddressSet
	udp listenAddressSet
}

func newProtocolListenAddressSet() *protocolListenAddressSet {
	return &protocolListenAddressSet{tcp: newListenAddressSet(), udp: newListenAddressSet()}
}

func (s *protocolListenAddressSet) add(entryPoint EntryPoint, addr listenAddress) error {
	if entryPoint.Protocol == EntryPointProtocolUDP {
		return s.udp.add(addr, entryPoint.Name, "udp")
	}
	return s.tcp.add(addr, entryPoint.Name, "tcp")
}

func validateTrafficEntryPoint(entryPoint EntryPoint, existing map[string]EntryPoint, listenAddresses *protocolListenAddressSet) error {
	if entryPoint.Name == "" {
		return fmt.Errorf("entrypoint name is required")
	}
	if _, exists := existing[entryPoint.Name]; exists {
		return fmt.Errorf("duplicate entrypoint name %q", entryPoint.Name)
	}
	if err := validateEntryPointProtocol(entryPoint.Protocol); err != nil {
		return fmt.Errorf("invalid entrypoint protocol for %q: %w", entryPoint.Name, err)
	}
	if err := validateTrustedCIDRs(entryPoint); err != nil {
		return err
	}
	addr, err := parseListenAddress(entryPoint.Address)
	if err != nil {
		return fmt.Errorf("invalid entrypoint address for %q: %w", entryPoint.Name, err)
	}
	return listenAddresses.add(entryPoint, addr)
}

func validateTrafficServices(values []TrafficService) (map[string]TrafficService, error) {
	services := make(map[string]TrafficService, len(values))
	for _, service := range values {
		if err := validateTrafficService(service, services); err != nil {
			return nil, err
		}
		services[service.Name] = service
	}
	return services, nil
}

func validateTrafficService(service TrafficService, existing map[string]TrafficService) error {
	if service.Name == "" {
		return fmt.Errorf("traffic service name is required")
	}
	if _, exists := existing[service.Name]; exists {
		return fmt.Errorf("duplicate traffic service name %q", service.Name)
	}
	if _, err := ParseTrafficServiceRef(service.Name); err != nil {
		return fmt.Errorf("invalid traffic service ref %q: %w", service.Name, err)
	}
	for _, backend := range service.Backends {
		if err := validateTrafficBackend(service.Name, backend); err != nil {
			return err
		}
	}
	return nil
}

func validateTrafficBackend(serviceName string, backend TrafficBackend) error {
	if strings.TrimSpace(backend.Host) == "" {
		return fmt.Errorf("backend host is required for service %q", serviceName)
	}
	if backend.Port < 1 || backend.Port > 65535 {
		return fmt.Errorf("invalid backend port %d for service %q", backend.Port, serviceName)
	}
	if err := validateNetworkProtocol(backend.Protocol); err != nil {
		return fmt.Errorf("invalid backend protocol for service %q: %w", serviceName, err)
	}
	return nil
}

func (g TrafficGraph) validateRouters(entryPoints map[string]EntryPoint, services map[string]TrafficService) error {
	state := newRouterValidationState()
	for _, router := range g.Routers {
		if err := state.validateRouter(router, entryPoints, services); err != nil {
			return err
		}
	}
	return nil
}

type routerValidationState struct {
	routerNames map[string]struct{}
	exactSNI    map[string]map[string]string
	wildSNI     map[string]map[string]string
	httpHosts   map[string]map[string]string
	tcpRouters  map[string]string
	udpRouters  map[string]string
}

func newRouterValidationState() *routerValidationState {
	return &routerValidationState{
		routerNames: map[string]struct{}{},
		exactSNI:    map[string]map[string]string{},
		wildSNI:     map[string]map[string]string{},
		httpHosts:   map[string]map[string]string{},
		tcpRouters:  map[string]string{},
		udpRouters:  map[string]string{},
	}
}

func (s *routerValidationState) validateRouter(router TrafficRouter, entryPoints map[string]EntryPoint, services map[string]TrafficService) error {
	if err := s.validateRouterName(router); err != nil {
		return err
	}
	entryPoint, serviceRef, service, err := resolveRouterTargets(router, entryPoints, services)
	if err != nil {
		return err
	}
	if err := validateRouterEntryPointProtocol(router, entryPoint); err != nil {
		return err
	}
	if err := validateRouterServiceRef(router, serviceRef, service); err != nil {
		return err
	}
	if err := s.validateRouterRule(router, entryPoint); err != nil {
		return err
	}
	return s.validateTLSPassthroughRule(router, entryPoint)
}

func (s *routerValidationState) validateRouterName(router TrafficRouter) error {
	if router.Name == "" {
		return fmt.Errorf("traffic router name is required")
	}
	if _, exists := s.routerNames[router.Name]; exists {
		return fmt.Errorf("duplicate traffic router name %q", router.Name)
	}
	s.routerNames[router.Name] = struct{}{}
	return nil
}

func resolveRouterTargets(router TrafficRouter, entryPoints map[string]EntryPoint, services map[string]TrafficService) (EntryPoint, TrafficServiceRef, TrafficService, error) {
	entryPoint, ok := entryPoints[router.EntryPoint]
	if !ok {
		return EntryPoint{}, TrafficServiceRef{}, TrafficService{}, fmt.Errorf("router %q references unknown entrypoint %q", router.Name, router.EntryPoint)
	}
	serviceRef, err := ParseTrafficServiceRef(router.Service)
	if err != nil {
		return EntryPoint{}, TrafficServiceRef{}, TrafficService{}, fmt.Errorf("router %q has invalid service ref: %w", router.Name, err)
	}
	service, ok := services[router.Service]
	if !ok {
		return EntryPoint{}, TrafficServiceRef{}, TrafficService{}, fmt.Errorf("router %q references unknown service %q", router.Name, router.Service)
	}
	if err := validateRouterProtocol(router.Protocol); err != nil {
		return EntryPoint{}, TrafficServiceRef{}, TrafficService{}, fmt.Errorf("invalid router protocol for %q: %w", router.Name, err)
	}
	return entryPoint, serviceRef, service, nil
}

func (s *routerValidationState) validateRouterRule(router TrafficRouter, entryPoint EntryPoint) error {
	switch router.Protocol {
	case RouterProtocolTCP:
		return validateSingleL4Router(s.tcpRouters, "tcp", entryPoint.Name, router.Name)
	case RouterProtocolUDP:
		return validateSingleL4Router(s.udpRouters, "udp", entryPoint.Name, router.Name)
	case RouterProtocolHTTP:
		return s.validateHTTPRule(router, entryPoint)
	default:
		return nil
	}
}

func validateSingleL4Router(routers map[string]string, protocol string, entryPoint string, routerName string) error {
	if existing := routers[entryPoint]; existing != "" {
		return fmt.Errorf("ambiguous %s routers on entrypoint %q: %q and %q", protocol, entryPoint, existing, routerName)
	}
	routers[entryPoint] = routerName
	return nil
}

func (s *routerValidationState) validateHTTPRule(router TrafficRouter, entryPoint EntryPoint) error {
	if router.Rule.Host == "" {
		return nil
	}
	host := normalizeHost(router.Rule.Host)
	if existing := s.httpHosts[entryPoint.Name][host]; existing != "" {
		return fmt.Errorf("duplicate http host %q on entrypoint %q used by routers %q and %q", host, entryPoint.Name, existing, router.Name)
	}
	if err := s.validateHTTPRuleTLSConflicts(entryPoint.Name, host); err != nil {
		return err
	}
	putNested(s.httpHosts, entryPoint.Name, host, router.Name)
	return nil
}

func (s *routerValidationState) validateHTTPRuleTLSConflicts(entryPoint string, host string) error {
	if s.exactSNI[entryPoint][host] != "" {
		return fmt.Errorf("http host conflicts with tls passthrough sni %q on entrypoint %q", host, entryPoint)
	}
	if wildcard := matchingWildcardSNI(host, s.wildSNI[entryPoint]); wildcard != "" {
		return fmt.Errorf("http host conflicts with wildcard tls passthrough sni %q on entrypoint %q", wildcard, entryPoint)
	}
	return nil
}

func (s *routerValidationState) validateTLSPassthroughRule(router TrafficRouter, entryPoint EntryPoint) error {
	if router.Protocol != RouterProtocolTLSPassthrough {
		return nil
	}
	if strings.TrimSpace(router.Rule.SNI) == "" {
		return fmt.Errorf("tls passthrough router %q requires sni", router.Name)
	}
	sni := normalizeHost(router.Rule.SNI)
	if strings.HasPrefix(sni, "*.") {
		return s.validateWildcardSNI(entryPoint.Name, sni, router.Name)
	}
	return s.validateExactSNI(entryPoint.Name, sni, router.Name)
}

func (s *routerValidationState) validateWildcardSNI(entryPoint string, sni string, routerName string) error {
	if s.wildSNI[entryPoint][sni] != "" {
		return fmt.Errorf("ambiguous wildcard tls sni %q on entrypoint %q", sni, entryPoint)
	}
	if wildcard := overlappingWildcardSNI(sni, s.wildSNI[entryPoint]); wildcard != "" {
		return fmt.Errorf("ambiguous wildcard tls sni %q overlaps %q on entrypoint %q", sni, wildcard, entryPoint)
	}
	if host := matchingHTTPHost(sni, s.httpHosts[entryPoint]); host != "" {
		return fmt.Errorf("http host conflicts with wildcard tls passthrough sni %q on entrypoint %q for host %q", sni, entryPoint, host)
	}
	putNested(s.wildSNI, entryPoint, sni, routerName)
	return nil
}

func (s *routerValidationState) validateExactSNI(entryPoint string, sni string, routerName string) error {
	if s.exactSNI[entryPoint][sni] != "" {
		return fmt.Errorf("duplicate exact tls sni %q on entrypoint %q", sni, entryPoint)
	}
	if s.httpHosts[entryPoint][sni] != "" {
		return fmt.Errorf("http host conflicts with tls passthrough sni %q on entrypoint %q", sni, entryPoint)
	}
	putNested(s.exactSNI, entryPoint, sni, routerName)
	return nil
}

func validateRouterEntryPointProtocol(router TrafficRouter, entryPoint EntryPoint) error {
	compatible := false
	switch router.Protocol {
	case RouterProtocolHTTP:
		compatible = entryPoint.Protocol == EntryPointProtocolHTTP || entryPoint.Protocol == EntryPointProtocolTLSMux
	case RouterProtocolTLSPassthrough:
		compatible = entryPoint.Protocol == EntryPointProtocolTLSMux
	case RouterProtocolTCP:
		compatible = entryPoint.Protocol == EntryPointProtocolTCP
	case RouterProtocolUDP:
		compatible = entryPoint.Protocol == EntryPointProtocolUDP
	}
	if compatible {
		return nil
	}
	return fmt.Errorf("router %q protocol %s is incompatible with entrypoint %q protocol %s", router.Name, router.Protocol, entryPoint.Name, entryPoint.Protocol)
}

func validateRouterServiceRef(router TrafficRouter, ref TrafficServiceRef, service TrafficService) error {
	switch router.Protocol {
	case RouterProtocolHTTP:
		if ref.Kind == TrafficServiceRefRoute || ref.Kind == TrafficServiceRefExternalRoute {
			return nil
		}
	case RouterProtocolTCP, RouterProtocolUDP, RouterProtocolTLSPassthrough:
		return validateL4RouterService(router, ref, service)
	}
	if ref.Kind == TrafficServiceRefStatic {
		return fmt.Errorf("static traffic service ref %q is unsupported in traffic graph validation", router.Service)
	}
	return fmt.Errorf("router %q protocol %s does not support service ref kind %s", router.Name, router.Protocol, ref.Kind)
}

func validateL4RouterService(router TrafficRouter, ref TrafficServiceRef, service TrafficService) error {
	want, ok := l4BackendProtocol(router.Protocol)
	if !ok {
		return nil
	}
	if ref.Kind != TrafficServiceRefNetworkService {
		if ref.Kind == TrafficServiceRefStatic {
			return fmt.Errorf("static traffic service ref %q is unsupported in traffic graph validation", router.Service)
		}
		return fmt.Errorf("l4 router %q requires network_service service ref", router.Name)
	}
	if len(service.Backends) != 1 {
		return fmt.Errorf("l4 router service must have exactly one backend for router %q", router.Name)
	}
	backend := service.Backends[0]
	if backend.Protocol != want {
		return fmt.Errorf("backend protocol %s does not match router protocol %s for router %q (expected backend protocol %s)", backend.Protocol, router.Protocol, router.Name, want)
	}
	return nil
}

func l4BackendProtocol(protocol RouterProtocol) (NetworkProtocol, bool) {
	switch protocol {
	case RouterProtocolTCP:
		return NetworkProtocolTCP, true
	case RouterProtocolUDP:
		return NetworkProtocolUDP, true
	case RouterProtocolTLSPassthrough:
		return NetworkProtocolTCP, true
	default:
		return "", false
	}
}

func validateTrustedCIDRs(entryPoint EntryPoint) error {
	for _, cidr := range entryPoint.TrustedCIDRs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("invalid trusted_cidrs entry %q for entrypoint %q: %w", cidr, entryPoint.Name, err)
		}
	}
	return nil
}

func validateEntryPointProtocol(protocol EntryPointProtocol) error {
	switch protocol {
	case EntryPointProtocolHTTP, EntryPointProtocolTLSMux, EntryPointProtocolTCP, EntryPointProtocolUDP:
		return nil
	default:
		return fmt.Errorf("%q", protocol)
	}
}

func validateRouterProtocol(protocol RouterProtocol) error {
	switch protocol {
	case RouterProtocolHTTP, RouterProtocolTCP, RouterProtocolUDP, RouterProtocolTLSPassthrough:
		return nil
	default:
		return fmt.Errorf("%q", protocol)
	}
}

func validateNetworkProtocol(protocol NetworkProtocol) error {
	switch protocol {
	case NetworkProtocolTCP, NetworkProtocolUDP:
		return nil
	default:
		return fmt.Errorf("%q", protocol)
	}
}

type listenAddress struct {
	key      string
	port     int
	wildcard bool
}

type listenAddressSet struct {
	byKey map[string]string
	all   []registeredListenAddress
}

type registeredListenAddress struct {
	addr listenAddress
	name string
}

func newListenAddressSet() listenAddressSet {
	return listenAddressSet{byKey: map[string]string{}}
}

func (s *listenAddressSet) add(addr listenAddress, name string, protocol string) error {
	if other, exists := s.byKey[addr.key]; exists {
		return fmt.Errorf("duplicate %s entrypoint address %q used by %q and %q", protocol, addr.key, other, name)
	}
	for _, existing := range s.all {
		if existing.addr.port == addr.port && (existing.addr.wildcard || addr.wildcard) {
			return fmt.Errorf("conflicting %s entrypoint address %q used by %q and %q", protocol, addr.key, existing.name, name)
		}
	}
	s.byKey[addr.key] = name
	s.all = append(s.all, registeredListenAddress{addr: addr, name: name})
	return nil
}

func parseListenAddress(address string) (listenAddress, error) {
	host, portValue, err := net.SplitHostPort(address)
	if err != nil {
		return listenAddress{}, err
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return listenAddress{}, fmt.Errorf("invalid port %q", portValue)
	}
	return listenAddress{
		key:      net.JoinHostPort(host, strconv.Itoa(port)),
		port:     port,
		wildcard: isWildcardListenHost(host),
	}, nil
}

func isWildcardListenHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func matchingWildcardSNI(host string, wildcards map[string]string) string {
	for wildcard := range wildcards {
		if hostMatchesWildcard(host, wildcard) {
			return wildcard
		}
	}
	return ""
}

func matchingHTTPHost(wildcard string, hosts map[string]string) string {
	for host := range hosts {
		if hostMatchesWildcard(host, wildcard) {
			return host
		}
	}
	return ""
}

func overlappingWildcardSNI(wildcard string, wildcards map[string]string) string {
	for existing := range wildcards {
		if wildcardSNIOverlap(wildcard, existing) {
			return existing
		}
	}
	return ""
}

func wildcardSNIOverlap(left string, right string) bool {
	leftSuffix, leftOK := strings.CutPrefix(left, "*.")
	rightSuffix, rightOK := strings.CutPrefix(right, "*.")
	if !leftOK || !rightOK {
		return false
	}
	return leftSuffix == rightSuffix || strings.HasSuffix(leftSuffix, "."+rightSuffix) || strings.HasSuffix(rightSuffix, "."+leftSuffix)
}

func hostMatchesWildcard(host string, wildcard string) bool {
	suffix, ok := strings.CutPrefix(wildcard, "*.")
	if !ok || host == suffix {
		return false
	}
	return strings.HasSuffix(host, "."+suffix)
}

func putNested(values map[string]map[string]string, key string, nestedKey string, value string) {
	if values[key] == nil {
		values[key] = map[string]string{}
	}
	values[key][nestedKey] = value
}
