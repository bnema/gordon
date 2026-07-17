package domain

import (
	"fmt"
	"net"
	"strings"
	"unicode"
)

// RouteTargetStatus describes whether an edge route target may receive traffic.
type RouteTargetStatus string

const (
	RouteTargetStatusReady       RouteTargetStatus = "ready"
	RouteTargetStatusUnavailable RouteTargetStatus = "unavailable"
	RouteTargetStatusDraining    RouteTargetStatus = "draining"
)

// RouteTargetProtocol is the routing protocol understood by the edge proxy.
type RouteTargetProtocol string

const (
	RouteTargetProtocolHTTP1 RouteTargetProtocol = "http1"
	RouteTargetProtocolH2C   RouteTargetProtocol = "h2c"
)

// RouteTargetUnavailableReason explains why a target is not ready for traffic.
type RouteTargetUnavailableReason string

const (
	RouteTargetUnavailableReasonNone              RouteTargetUnavailableReason = ""
	RouteTargetUnavailableReasonNoTarget          RouteTargetUnavailableReason = "no_target"
	RouteTargetUnavailableReasonStarting          RouteTargetUnavailableReason = "starting"
	RouteTargetUnavailableReasonHealthCheckFailed RouteTargetUnavailableReason = "health_check_failed"
	RouteTargetUnavailableReasonDeployment        RouteTargetUnavailableReason = "deployment"
	RouteTargetUnavailableReasonDraining          RouteTargetUnavailableReason = "draining"
)

// RouteTargetGeneration is a monotonically increasing version for route target snapshots.
type RouteTargetGeneration uint64

// After reports whether g is newer than previous.
func (g RouteTargetGeneration) After(previous RouteTargetGeneration) bool {
	return g > previous
}

// RouteTargetSnapshot is the routing-only view of edge targets.
type RouteTargetSnapshot struct {
	Generation RouteTargetGeneration
	Entries    []RouteTargetEntry
}

// Validate verifies the snapshot is a coherent, self-contained routing view.
func (s RouteTargetSnapshot) Validate() error {
	if s.Generation == 0 {
		return fmt.Errorf("%w: generation must be non-zero", ErrInvalidRouteSnapshot)
	}

	seenDomains := make(map[string]struct{}, len(s.Entries))
	for index, entry := range s.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("%w: entry %d: %w", ErrInvalidRouteSnapshot, index, err)
		}
		if entry.Generation > s.Generation {
			return fmt.Errorf("%w: entry %d generation cannot be newer than snapshot generation", ErrInvalidRouteSnapshot, index)
		}
		if _, exists := seenDomains[entry.CanonicalDomain]; exists {
			return fmt.Errorf("%w: duplicate canonical domain at entry %d", ErrInvalidRouteSnapshot, index)
		}
		seenDomains[entry.CanonicalDomain] = struct{}{}
	}
	return nil
}

// ValidateSplitReachability verifies that routable targets can be reached by a split edge process.
// Local snapshots may use loopback targets during the monolith transition; split edges may not.
func (s RouteTargetSnapshot) ValidateSplitReachability() error {
	if err := s.Validate(); err != nil {
		return err
	}
	for index, entry := range s.Entries {
		if err := entry.ValidateSplitReachability(); err != nil {
			return fmt.Errorf("%w: entry %d: %w", ErrInvalidRouteSnapshot, index, err)
		}
	}
	return nil
}

// RouteTargetEntry contains only fields required to route traffic to a target.
type RouteTargetEntry struct {
	CanonicalDomain   string
	TargetHost        string
	TargetPort        int
	Scheme            string
	Protocol          RouteTargetProtocol
	Status            RouteTargetStatus
	UnavailableReason RouteTargetUnavailableReason
	Generation        RouteTargetGeneration
}

// NewReadyRouteTargetEntry constructs a validated ready route target.
func NewReadyRouteTargetEntry(domainName, targetHost string, targetPort int, scheme string, protocol RouteTargetProtocol, generation RouteTargetGeneration) (RouteTargetEntry, error) {
	entry := RouteTargetEntry{
		TargetHost: targetHost,
		TargetPort: targetPort,
		Scheme:     scheme,
		Protocol:   protocol,
		Status:     RouteTargetStatusReady,
		Generation: generation,
	}
	if err := entry.setCanonicalDomain(domainName); err != nil {
		return RouteTargetEntry{}, err
	}
	if err := entry.Validate(); err != nil {
		return RouteTargetEntry{}, err
	}
	return entry, nil
}

// NewDrainingRouteTargetEntry constructs a validated draining route target.
func NewDrainingRouteTargetEntry(domainName, targetHost string, targetPort int, scheme string, protocol RouteTargetProtocol, generation RouteTargetGeneration) (RouteTargetEntry, error) {
	entry, err := NewReadyRouteTargetEntry(domainName, targetHost, targetPort, scheme, protocol, generation)
	if err != nil {
		return RouteTargetEntry{}, err
	}
	entry.Status = RouteTargetStatusDraining
	entry.UnavailableReason = RouteTargetUnavailableReasonDraining
	return entry, nil
}

// NewUnavailableRouteTargetEntry constructs a validated unavailable route target.
func NewUnavailableRouteTargetEntry(domainName string, reason RouteTargetUnavailableReason, generation RouteTargetGeneration) (RouteTargetEntry, error) {
	entry := RouteTargetEntry{
		Status:            RouteTargetStatusUnavailable,
		UnavailableReason: reason,
		Generation:        generation,
	}
	if err := entry.setCanonicalDomain(domainName); err != nil {
		return RouteTargetEntry{}, err
	}
	if err := entry.Validate(); err != nil {
		return RouteTargetEntry{}, err
	}
	return entry, nil
}

// ProtocolFromProxyTarget maps the current ProxyTarget protocol field to a route snapshot protocol.
func ProtocolFromProxyTarget(target ProxyTarget) RouteTargetProtocol {
	if target.Protocol == string(RouteTargetProtocolH2C) {
		return RouteTargetProtocolH2C
	}
	return RouteTargetProtocolHTTP1
}

// ToProxyTarget converts a routable snapshot entry back to a routing-only ProxyTarget.
func (e RouteTargetEntry) ToProxyTarget() (ProxyTarget, error) {
	if !e.Ready() && !e.Draining() {
		return ProxyTarget{}, fmt.Errorf("%w: unavailable route target cannot be converted to proxy target", ErrInvalidRoute)
	}
	if err := e.Validate(); err != nil {
		return ProxyTarget{}, err
	}
	protocol := ""
	if e.Protocol == RouteTargetProtocolH2C {
		protocol = string(RouteTargetProtocolH2C)
	}
	return ProxyTarget{
		Host:      e.TargetHost,
		Port:      e.TargetPort,
		Scheme:    e.Scheme,
		Protocol:  protocol,
		RouteHost: e.CanonicalDomain,
	}, nil
}

// Ready reports whether this entry may receive traffic.
func (e RouteTargetEntry) Ready() bool {
	return e.Status == RouteTargetStatusReady
}

// Draining reports whether this entry is being drained.
func (e RouteTargetEntry) Draining() bool {
	return e.Status == RouteTargetStatusDraining
}

// Unavailable reports whether this entry cannot receive traffic.
func (e RouteTargetEntry) Unavailable() bool {
	return e.Status == RouteTargetStatusUnavailable
}

// Validate verifies the entry contains only a coherent routing target state.
func (e RouteTargetEntry) Validate() error {
	canonicalDomain, validDomain := CanonicalRouteDomain(e.CanonicalDomain)
	if !validDomain || e.CanonicalDomain != canonicalDomain {
		return fmt.Errorf("%w: %s", ErrRouteDomainInvalid, e.CanonicalDomain)
	}
	if e.Generation == 0 {
		return fmt.Errorf("%w: route target generation must be non-zero", ErrInvalidRoute)
	}
	if !e.validStatus() {
		return fmt.Errorf("%w: invalid route target status %q", ErrInvalidRoute, e.Status)
	}
	if !e.validScheme() {
		return fmt.Errorf("%w: invalid route target scheme %q", ErrInvalidRoute, e.Scheme)
	}
	if !e.validProtocol() {
		return fmt.Errorf("%w: invalid route target protocol %q", ErrInvalidRoute, e.Protocol)
	}
	if !e.validUnavailableReason() {
		return fmt.Errorf("%w: invalid route target unavailable reason %q", ErrInvalidRoute, e.UnavailableReason)
	}
	return e.validateStateCoherence()
}

func (e RouteTargetEntry) validateStateCoherence() error {
	switch e.Status {
	case RouteTargetStatusReady:
		return e.validateReadyTarget()
	case RouteTargetStatusDraining:
		return e.validateDrainingTarget()
	case RouteTargetStatusUnavailable:
		return e.validateUnavailableTarget()
	default:
		return fmt.Errorf("%w: invalid route target status %q", ErrInvalidRoute, e.Status)
	}
}

func (e RouteTargetEntry) validateReadyTarget() error {
	if e.UnavailableReason != RouteTargetUnavailableReasonNone {
		return fmt.Errorf("%w: ready route target cannot include unavailable reason", ErrInvalidRoute)
	}
	return e.validateRoutableTarget()
}

func (e RouteTargetEntry) validateDrainingTarget() error {
	if e.UnavailableReason != RouteTargetUnavailableReasonDraining {
		return fmt.Errorf("%w: draining route target requires draining reason", ErrInvalidRoute)
	}
	return e.validateRoutableTarget()
}

func (e RouteTargetEntry) validateUnavailableTarget() error {
	if e.UnavailableReason == RouteTargetUnavailableReasonNone {
		return fmt.Errorf("%w: unavailable route target requires reason", ErrInvalidRoute)
	}
	if e.UnavailableReason == RouteTargetUnavailableReasonDraining {
		return fmt.Errorf("%w: unavailable route target cannot use draining reason", ErrInvalidRoute)
	}
	if e.TargetHost != "" || e.TargetPort != 0 || e.Scheme != "" || e.Protocol != "" {
		return fmt.Errorf("%w: unavailable route target cannot include routing endpoint fields", ErrInvalidRoute)
	}
	return nil
}

func (e RouteTargetEntry) validateRoutableTarget() error {
	if !validRouteTargetHost(e.TargetHost) {
		return fmt.Errorf("%w: route target host is invalid", ErrInvalidRoute)
	}
	if !validRouteTargetPort(e.TargetPort) {
		return fmt.Errorf("%w: route target port must be between 1 and 65535", ErrInvalidRoute)
	}
	return nil
}

func (e *RouteTargetEntry) setCanonicalDomain(domainName string) error {
	canonical, ok := CanonicalRouteDomain(domainName)
	if !ok {
		return fmt.Errorf("%w: %s", ErrRouteDomainInvalid, domainName)
	}
	e.CanonicalDomain = canonical
	return nil
}

func (e RouteTargetEntry) validStatus() bool {
	switch e.Status {
	case RouteTargetStatusReady, RouteTargetStatusUnavailable, RouteTargetStatusDraining:
		return true
	default:
		return false
	}
}

func (e RouteTargetEntry) validScheme() bool {
	if e.Unavailable() && e.Scheme == "" {
		return true
	}
	return e.Scheme == "http" || e.Scheme == "https"
}

func (e RouteTargetEntry) validProtocol() bool {
	if e.Protocol == "" && e.Unavailable() {
		return true
	}
	return e.Protocol == RouteTargetProtocolHTTP1 || e.Protocol == RouteTargetProtocolH2C
}

func (e RouteTargetEntry) validUnavailableReason() bool {
	switch e.UnavailableReason {
	case RouteTargetUnavailableReasonNone,
		RouteTargetUnavailableReasonNoTarget,
		RouteTargetUnavailableReasonStarting,
		RouteTargetUnavailableReasonHealthCheckFailed,
		RouteTargetUnavailableReasonDeployment,
		RouteTargetUnavailableReasonDraining:
		return true
	default:
		return false
	}
}

// ValidateSplitReachability verifies that a routable entry does not rely on local loopback.
// It is intentionally separate from Validate because monolith deployments may use loopback.
func (e RouteTargetEntry) ValidateSplitReachability() error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Unavailable() {
		return nil
	}
	if isLoopbackRouteTargetHost(e.TargetHost) {
		return fmt.Errorf("%w: loopback target host", ErrRouteTargetNotReachable)
	}
	return nil
}

func validRouteTargetHost(host string) bool {
	if host == "" || containsWhitespaceOrControl(host) {
		return false
	}
	if strings.ContainsAny(host, "/\\?#@") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") || len(host) > 253 || !hostnamePattern.MatchString(host) {
		return false
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !isValidLabel(label) {
			return false
		}
	}
	return true
}

func containsWhitespaceOrControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0
}

func isLoopbackRouteTargetHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validRouteTargetPort(port int) bool {
	return port > 0 && port <= 65535
}
