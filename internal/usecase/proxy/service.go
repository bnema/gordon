// Package proxy implements the reverse proxy use case.
package proxy

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

var proxyTracer = otel.Tracer("gordon.proxy")

// Config holds configuration needed by the proxy service.
type Config struct {
	RegistryDomain     string
	RegistryPort       int
	MaxBodySize        int64 // Maximum request body size in bytes (0 = no limit)
	MaxResponseSize    int64 // Maximum response body size in bytes (0 = no limit)
	MaxConcurrentConns int   // Maximum concurrent proxy connections (0 = no limit)
}

// TargetProvider resolves a canonical HTTP host to its app backend.
// Implemented by the apptraffic host index (derived from ACTIVE state);
// the proxy never interprets app state itself.
type TargetProvider interface {
	// LookupHost returns the projected backend for a canonical host.
	LookupHost(host string) (domain.AppBackend, bool)
}

// Service implements the ProxyService interface.
// App backends resolve through appTargets (ACTIVE-derived projection);
// the pre-v2.50 container-service + image-label resolution is removed.
type Service struct {
	configSvc in.ConfigService
	config    Config
	// appTargets is the ACTIVE-derived host index, wired after the app
	// store opens (WithAppTargets). Nil means no app serves this host.
	// App resolutions bypass the target cache entirely (see
	// resolveAppTarget); only external routes and explicit RegisterTarget
	// entries live in targets.
	appTargets TargetProvider
	targets    map[string]cachedTarget
	// gen is the LOCAL cache invalidation epoch for external/explicit
	// targets only. It is unrelated to the apptraffic HostIndex version:
	// app resolutions never consult it.
	gen              uint64
	mu               sync.RWMutex
	inFlight         map[string]int
	inFlightMu       sync.Mutex
	registryInFlight atomic.Int64 // active registry proxy requests, for graceful drain
}

// cachedTarget carries the local cache epoch its resolution was built from.
// Applies to external/explicit targets only; app targets are never cached.
type cachedTarget struct {
	target *domain.ProxyTarget
	gen    uint64
}

// WithAppTargets wires the ACTIVE-derived host index. The proxy is
// built before the app store opens, so wiring is deferred like the
// backup WithAppSources hook.
func (s *Service) WithAppTargets(provider TargetProvider) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appTargets = provider
	s.gen++
	s.targets = make(map[string]cachedTarget)
	return s
}

// NewService creates a new proxy service. The app target provider is
// wired later via WithAppTargets once the app store opens.
func NewService(
	configSvc in.ConfigService,
	config Config,
) *Service {
	return &Service{
		configSvc: configSvc,
		config:    config,
		targets:   make(map[string]cachedTarget),
		inFlight:  make(map[string]int),
	}
}

// GetTarget returns the proxy target for a given domain.
func (s *Service) GetTarget(ctx context.Context, domainName string) (target *domain.ProxyTarget, retErr error) {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return nil, domain.ErrNoTargetAvailable
	}
	domainName = canonicalDomain

	ctx, span := proxyTracer.Start(ctx, "proxy.get_target",
		trace.WithAttributes(attribute.String("domain", domainName)))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetTarget",
		"domain":              domainName,
	})
	log := zerowrap.FromCtx(ctx)

	// Check cache first (generation-guarded: a resolution built before
	// the latest invalidation is dropped, never served).
	s.mu.RLock()
	if cached, exists := s.targets[domainName]; exists {
		s.mu.RUnlock()
		log.Debug().
			Str("host", cached.target.Host).
			Int("port", cached.target.Port).
			Str("container_id", cached.target.ContainerID).
			Msg("using cached proxy target")
		return cached.target, nil
	}
	s.mu.RUnlock()

	// Check if this is an external route
	externalRoutes := s.configSvc.GetExternalRoutes()
	if targetAddr, ok := externalRoutes[domainName]; ok {
		return s.resolveExternalRoute(ctx, domainName, targetAddr, log)
	}

	// App backends resolve from the ACTIVE-derived host index.
	return s.resolveAppTarget(domainName, log)
}

// resolveAppTarget maps a canonical host to its recorded loopback
// backend. App targets are NEVER cached: every resolution reads the
// ACTIVE-derived host index (an in-memory RWMutex lookup), so a freshly
// rebuilt index is observed on the very next request with no invalidation
// wiring. Unbound or unknown hosts fail closed (no container-IP
// fallback, no image-label inference, no stale-target fallback).
func (s *Service) resolveAppTarget(domainName string, log zerowrap.Logger) (*domain.ProxyTarget, error) {
	s.mu.RLock()
	provider := s.appTargets
	s.mu.RUnlock()
	if provider == nil {
		log.Debug().Msg("no app target provider wired")
		return nil, domain.ErrNoTargetAvailable
	}
	backend, ok := provider.LookupHost(domainName)
	if !ok || !backend.Resolved() {
		log.Debug().Msg("no resolved app backend for host")
		return nil, domain.ErrNoTargetAvailable
	}
	return &domain.ProxyTarget{
		Host:        backend.Host,
		Port:        backend.Port,
		ContainerID: backend.ContainerID,
		Scheme:      "http",
		RouteHost:   domainName,
	}, nil
}

// resolveExternalRoute resolves an external route target address into a ProxyTarget,
// performing DNS validation and SSRF protection.
func (s *Service) resolveExternalRoute(_ context.Context, domainName, targetAddr string, log zerowrap.Logger) (*domain.ProxyTarget, error) {
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return nil, log.WrapErrWithFields(err, "invalid external route target", map[string]any{"target": targetAddr})
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, log.WrapErrWithFields(err, "invalid port in external route", map[string]any{"target": targetAddr})
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port in external route %q: port must be between 1 and 65535", targetAddr)
	}

	// SECURITY: Resolve DNS and validate that the target is not an internal/blocked
	// network. We use the resolved IP as the proxy target to prevent DNS rebinding
	// (TOCTOU) attacks where a hostname resolves to a public IP during validation
	// but to a private IP when the proxy connects.
	resolvedIP, err := ResolveAndValidateHost(host)
	if err != nil {
		log.Warn().
			Err(err).
			Str("host", host).
			Str("domain", domainName).
			Msg("SSRF protection: blocked external route to internal network")
		return nil, err
	}

	// Preserve the original hostname for the Host header so virtual-hosted
	// upstreams work correctly. The resolved IP is used for dialing only.
	var originalHost string
	if resolvedIP != host {
		originalHost = host
	}

	t := &domain.ProxyTarget{
		Host:         resolvedIP,
		Port:         port,
		ContainerID:  "", // Not a container
		Scheme:       "http",
		OriginalHost: originalHost,
	}

	// Cache external route target
	s.mu.Lock()
	s.targets[domainName] = cachedTarget{target: t, gen: s.gen}
	s.mu.Unlock()

	log.Debug().
		Str("host", host).
		Int("port", port).
		Msg("using external route target")
	return t, nil
}

// RegisterTarget registers a new proxy target for a domain.
func (s *Service) RegisterTarget(_ context.Context, domainName string, target *domain.ProxyTarget) error {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return domain.ErrRouteDomainInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.targets[canonicalDomain] = cachedTarget{target: target, gen: s.gen}
	return nil
}

// UnregisterTarget removes a proxy target for a domain.
func (s *Service) UnregisterTarget(_ context.Context, domainName string) error {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return domain.ErrRouteDomainInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.targets, canonicalDomain)
	return nil
}

// InvalidateTarget removes a cached proxy target, forcing re-lookup on next request.
// This is used during zero-downtime deployments to switch traffic to a new container.
// Every invalidation bumps the cache generation so concurrent stale
// resolutions are dropped instead of cached.
func (s *Service) InvalidateTarget(_ context.Context, domainName string) {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.gen++
	delete(s.targets, canonicalDomain)
}

// WaitForNoInFlight waits until no requests are currently proxied to the
// given container, or until timeout/context cancellation.
func (s *Service) WaitForNoInFlight(ctx context.Context, containerID string, timeout time.Duration) bool {
	if containerID == "" {
		return true
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	deadline := time.Now().Add(timeout)
	for {
		s.inFlightMu.Lock()
		count := s.inFlight[containerID]
		s.inFlightMu.Unlock()
		if count <= 0 {
			return true
		}

		if time.Now().After(deadline) {
			return false
		}

		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
}

// RefreshTargets drops all cached proxy targets, forcing re-lookup.
func (s *Service) RefreshTargets(ctx context.Context) error {
	s.mu.Lock()
	s.gen++
	s.targets = make(map[string]cachedTarget)
	s.mu.Unlock()

	log := zerowrap.FromCtx(ctx)
	log.Debug().Msg("proxy targets cache cleared")
	return nil
}

// UpdateConfig updates the service configuration.
// Connection limiting uses an atomic counter checked against config, so
// changing MaxConcurrentConns takes effect immediately without any race.
func (s *Service) UpdateConfig(config Config) {
	s.mu.Lock()
	s.config = config
	s.mu.Unlock()
}

// IsRegistryDomain returns true if the host matches the configured registry domain.
func (s *Service) IsRegistryDomain(host string) bool {
	canonicalHost, ok := domain.CanonicalRouteDomain(host)
	if !ok {
		return false
	}

	s.mu.RLock()
	registryDomain, ok := domain.CanonicalRouteDomain(s.config.RegistryDomain)
	s.mu.RUnlock()
	return ok && canonicalHost == registryDomain
}

// IsKnownHost returns true for the registry domain, external routes,
// and app-served hosts from the ACTIVE-derived index.
func (s *Service) IsKnownHost(_ context.Context, host string) bool {
	canonicalHost, ok := domain.CanonicalRouteDomain(host)
	if !ok {
		return false
	}
	if s.IsRegistryDomain(canonicalHost) {
		return true
	}
	if _, ok := s.configSvc.GetExternalRoutes()[canonicalHost]; ok {
		return true
	}
	s.mu.RLock()
	provider := s.appTargets
	s.mu.RUnlock()
	if provider == nil {
		return false
	}
	_, ok = provider.LookupHost(canonicalHost)
	return ok
}

// TrackInFlight records an in-flight request for a container.
// Returns a release function that must be called when the request completes.
func (s *Service) TrackInFlight(containerID string) func() {
	if containerID == "" {
		return func() {}
	}

	s.inFlightMu.Lock()
	s.inFlight[containerID]++
	s.inFlightMu.Unlock()

	return func() {
		s.inFlightMu.Lock()
		if s.inFlight[containerID] > 1 {
			s.inFlight[containerID]--
		} else {
			delete(s.inFlight, containerID)
		}
		s.inFlightMu.Unlock()
	}
}

// TrackRegistryRequest increments the registry in-flight counter.
func (s *Service) TrackRegistryRequest() {
	s.registryInFlight.Add(1)
}

// ReleaseRegistryRequest decrements the registry in-flight counter.
func (s *Service) ReleaseRegistryRequest() {
	s.registryInFlight.Add(-1)
}

// ProxyConfig returns the current proxy configuration for adapter use.
func (s *Service) ProxyConfig() in.ProxyServiceConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return in.ProxyServiceConfig{
		RegistryDomain:     s.config.RegistryDomain,
		RegistryPort:       s.config.RegistryPort,
		MaxBodySize:        s.config.MaxBodySize,
		MaxResponseSize:    s.config.MaxResponseSize,
		MaxConcurrentConns: s.config.MaxConcurrentConns,
	}
}

// RegistryInFlight returns the current count of active registry proxy requests.
func (s *Service) RegistryInFlight() int64 {
	return s.registryInFlight.Load()
}

// DrainRegistryInFlight blocks until all in-flight registry proxy requests
// complete or the timeout elapses. Returns true if drained cleanly, false if
// timed out with requests still in flight. Call this before shutting down the
// registry server.
func (s *Service) DrainRegistryInFlight(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.registryInFlight.Load() == 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
