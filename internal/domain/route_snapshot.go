package domain

import (
	"crypto/sha256"
	"encoding/base32"
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

// RouteTargetAttachment describes whether the split edge can reach a target
// through its own attachment. It intentionally does not identify a network.
type RouteTargetAttachment string

const (
	RouteTargetAttachmentAttached    RouteTargetAttachment = "attached"
	RouteTargetAttachmentNotRequired RouteTargetAttachment = "not_required"
	RouteTargetAttachmentUnavailable RouteTargetAttachment = "unavailable"
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
	// PolicyBlocked is used when external target validation rejects an SSRF-risky endpoint.
	RouteTargetUnavailableReasonPolicyBlocked RouteTargetUnavailableReason = "policy_blocked"
)

// RouteTargetGeneration is a monotonically increasing version for route target snapshots.
type RouteTargetGeneration uint64

// After reports whether g is newer than previous.
func (g RouteTargetGeneration) After(previous RouteTargetGeneration) bool {
	return g > previous
}

// RouteTargetKey is an opaque identity for an exact routing target. It is never
// a runtime or container identifier.
type RouteTargetKey string

const routeTargetKeyPrefix = "rtk_"

// NewRouteTargetKey validates a safe opaque target identity.
func NewRouteTargetKey(value string) (RouteTargetKey, error) {
	key := RouteTargetKey(value)
	if !key.Valid() {
		return "", fmt.Errorf("%w: route target key is invalid", ErrInvalidRoute)
	}
	return key, nil
}

// Valid reports whether the key has the opaque snapshot-key format.
func (k RouteTargetKey) Valid() bool {
	value := string(k)
	if !strings.HasPrefix(value, routeTargetKeyPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, routeTargetKeyPrefix)
	if len(encoded) < 16 || len(encoded) > 52 {
		return false
	}
	for _, char := range encoded {
		if (char < 'a' || char > 'z') && (char < '2' || char > '7') {
			return false
		}
	}
	return true
}

// RouteTargetSnapshot is the routing-only view of edge targets. Snapshot values are
// immutable after publication; callers that need to retain or modify one must use Clone.
type RouteTargetSnapshot struct {
	Generation               RouteTargetGeneration
	Entries                  []RouteTargetEntry
	RegistryForwardingTarget *RouteTargetEntry
}

// Clone returns an independent copy safe for the caller to retain or modify.
func (s RouteTargetSnapshot) Clone() RouteTargetSnapshot {
	clone := s
	if s.Entries != nil {
		clone.Entries = make([]RouteTargetEntry, len(s.Entries))
		copy(clone.Entries, s.Entries)
	}
	if s.RegistryForwardingTarget != nil {
		registryTarget := *s.RegistryForwardingTarget
		clone.RegistryForwardingTarget = &registryTarget
	}
	return clone
}

// Validate verifies the snapshot is a coherent, self-contained routing view.
func (s RouteTargetSnapshot) Validate() error {
	if s.Generation == 0 {
		return fmt.Errorf("%w: generation must be non-zero", ErrInvalidRouteSnapshot)
	}

	seenDomains := make(map[string]struct{}, len(s.Entries)+1)
	seenTargetKeys := make(map[RouteTargetKey]struct{}, len(s.Entries)+1)
	for index, entry := range s.Entries {
		if err := s.validateAggregateEntry(entry, seenDomains, seenTargetKeys); err != nil {
			return fmt.Errorf("%w: entry %d: %w", ErrInvalidRouteSnapshot, index, err)
		}
	}
	if s.RegistryForwardingTarget != nil {
		if s.RegistryForwardingTarget.Unavailable() {
			return fmt.Errorf("%w: registry forwarding target must be routable", ErrInvalidRouteSnapshot)
		}
		if err := s.validateAggregateEntry(*s.RegistryForwardingTarget, seenDomains, seenTargetKeys); err != nil {
			return fmt.Errorf("%w: registry forwarding target: %w", ErrInvalidRouteSnapshot, err)
		}
	}
	return nil
}

func (s RouteTargetSnapshot) validateAggregateEntry(entry RouteTargetEntry, seenDomains map[string]struct{}, seenTargetKeys map[RouteTargetKey]struct{}) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	if entry.Generation > s.Generation {
		return fmt.Errorf("entry generation cannot be newer than snapshot generation")
	}
	if _, exists := seenDomains[entry.CanonicalDomain]; exists {
		return fmt.Errorf("duplicate canonical domain")
	}
	seenDomains[entry.CanonicalDomain] = struct{}{}
	if entry.Unavailable() {
		return nil
	}
	if _, exists := seenTargetKeys[entry.TargetKey]; exists {
		return fmt.Errorf("duplicate route target key")
	}
	seenTargetKeys[entry.TargetKey] = struct{}{}
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
	if s.RegistryForwardingTarget != nil {
		if err := s.RegistryForwardingTarget.ValidateSplitReachability(); err != nil {
			return fmt.Errorf("%w: registry forwarding target: %w", ErrInvalidRouteSnapshot, err)
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
	UpstreamHost      string
	Attachment        RouteTargetAttachment
	TargetKey         RouteTargetKey
}

// NewReadyRouteTargetEntry constructs a validated attached (managed or registry) route target.
func NewReadyRouteTargetEntry(domainName, targetHost string, targetPort int, scheme string, protocol RouteTargetProtocol, generation RouteTargetGeneration) (RouteTargetEntry, error) {
	entry := RouteTargetEntry{
		TargetHost: targetHost,
		TargetPort: targetPort,
		Scheme:     scheme,
		Protocol:   protocol,
		Status:     RouteTargetStatusReady,
		Generation: generation,
		Attachment: RouteTargetAttachmentAttached,
	}
	if err := entry.setCanonicalDomain(domainName); err != nil {
		return RouteTargetEntry{}, err
	}
	entry.setDerivedTargetKey()
	if err := entry.Validate(); err != nil {
		return RouteTargetEntry{}, err
	}
	return entry, nil
}

// NewExternalReadyRouteTargetEntry constructs a validated external route target.
// targetHost may be a DNS-resolved address; upstreamHost is retained exactly for
// the outbound Host header and therefore must be a safe endpoint host.
func NewExternalReadyRouteTargetEntry(domainName, targetHost, upstreamHost string, targetPort int, scheme string, protocol RouteTargetProtocol, generation RouteTargetGeneration) (RouteTargetEntry, error) {
	entry := RouteTargetEntry{
		TargetHost:   targetHost,
		TargetPort:   targetPort,
		Scheme:       scheme,
		Protocol:     protocol,
		Status:       RouteTargetStatusReady,
		Generation:   generation,
		UpstreamHost: upstreamHost,
		Attachment:   RouteTargetAttachmentNotRequired,
	}
	if err := entry.setCanonicalDomain(domainName); err != nil {
		return RouteTargetEntry{}, err
	}
	entry.setDerivedTargetKey()
	if err := entry.Validate(); err != nil {
		return RouteTargetEntry{}, err
	}
	return entry, nil
}

// NewDrainingRouteTargetEntry constructs a validated draining attached route target.
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
		Attachment:        RouteTargetAttachmentUnavailable,
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
	target := ProxyTarget{
		Host:         e.TargetHost,
		Port:         e.TargetPort,
		Scheme:       e.Scheme,
		Protocol:     protocol,
		OriginalHost: e.UpstreamHost,
		TargetKey:    e.TargetKey,
	}
	// External upstreams must retain their configured host header. Managed
	// targets instead use the canonical route host.
	if e.UpstreamHost == "" {
		target.RouteHost = e.CanonicalDomain
	}
	return target, nil
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
		if e.UnavailableReason != RouteTargetUnavailableReasonNone {
			return fmt.Errorf("%w: ready route target cannot include unavailable reason", ErrInvalidRoute)
		}
		return e.validateRoutableTarget()
	case RouteTargetStatusDraining:
		if e.UnavailableReason != RouteTargetUnavailableReasonDraining {
			return fmt.Errorf("%w: draining route target requires draining reason", ErrInvalidRoute)
		}
		return e.validateRoutableTarget()
	case RouteTargetStatusUnavailable:
		return e.validateUnavailableTarget()
	default:
		return fmt.Errorf("%w: invalid route target status %q", ErrInvalidRoute, e.Status)
	}
}

func (e RouteTargetEntry) validateUnavailableTarget() error {
	if e.UnavailableReason == RouteTargetUnavailableReasonNone {
		return fmt.Errorf("%w: unavailable route target requires reason", ErrInvalidRoute)
	}
	if e.UnavailableReason == RouteTargetUnavailableReasonDraining {
		return fmt.Errorf("%w: unavailable route target cannot use draining reason", ErrInvalidRoute)
	}
	if e.TargetHost != "" || e.TargetPort != 0 || e.Scheme != "" || e.Protocol != "" || e.UpstreamHost != "" || e.TargetKey != "" {
		return fmt.Errorf("%w: unavailable route target cannot include routing endpoint fields", ErrInvalidRoute)
	}
	if e.Attachment != RouteTargetAttachmentUnavailable {
		return fmt.Errorf("%w: unavailable route target requires unavailable attachment", ErrInvalidRoute)
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
	if !e.TargetKey.Valid() {
		return fmt.Errorf("%w: route target key is invalid", ErrInvalidRoute)
	}
	if e.TargetKey != e.derivedTargetKey() {
		return fmt.Errorf("%w: route target key does not match routing target", ErrInvalidRoute)
	}
	if e.UpstreamHost == "" {
		if e.Attachment != RouteTargetAttachmentAttached {
			return fmt.Errorf("%w: attached route target requires edge attachment", ErrInvalidRoute)
		}
		return nil
	}
	if !validRouteTargetHost(e.UpstreamHost) {
		return fmt.Errorf("%w: upstream host is invalid", ErrInvalidRoute)
	}
	if e.Attachment != RouteTargetAttachmentNotRequired {
		return fmt.Errorf("%w: external route target must not require edge attachment", ErrInvalidRoute)
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

func (e *RouteTargetEntry) setDerivedTargetKey() {
	e.TargetKey = e.derivedTargetKey()
}

func (e RouteTargetEntry) derivedTargetKey() RouteTargetKey {
	payload := strings.Join([]string{
		e.CanonicalDomain,
		canonicalRouteTargetIdentityHost(e.TargetHost),
		fmt.Sprintf("%d", e.TargetPort),
		e.Scheme,
		string(e.Protocol),
		canonicalRouteTargetIdentityHost(e.UpstreamHost),
		string(e.Attachment),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return RouteTargetKey(routeTargetKeyPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])))
}

// canonicalRouteTargetIdentityHost keeps endpoint spelling transport-friendly while
// making DNS aliases and equivalent IP literal spellings share one target identity.
func canonicalRouteTargetIdentityHost(host string) string {
	host = strings.TrimSuffix(host, ".")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(host)
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
		RouteTargetUnavailableReasonDraining,
		RouteTargetUnavailableReasonPolicyBlocked:
		return true
	default:
		return false
	}
}

// ValidateSplitReachability verifies that a routable entry does not rely on local loopback
// and declares whether an edge attachment is required. It is intentionally separate from
// Validate because monolith deployments may use loopback.
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
	// An absolute FQDN may include one root-label dot, but IP literals may not.
	// Reject a dotted IP spelling before removing the DNS root label.
	if strings.HasSuffix(host, ".") && net.ParseIP(strings.TrimSuffix(host, ".")) != nil {
		return false
	}
	// The root-label dot is not part of the 253-byte hostname limit. Do not trim
	// more than one: repeated dots are not valid endpoint hosts.
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	// A colon that was not parsed as IPv6 is an authority/host-port value.
	if strings.Contains(host, ":") {
		return false
	}
	if strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !isValidEndpointHostLabel(label) {
			return false
		}
	}
	return true
}

func isValidEndpointHostLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, char := range label {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func containsWhitespaceOrControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0
}

func isLoopbackRouteTargetHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validRouteTargetPort(port int) bool {
	return port > 0 && port <= 65535
}
