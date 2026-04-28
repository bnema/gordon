// Package proxy implements the reverse proxy use case.
package proxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
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

// Service implements the ProxyService interface.
type Service struct {
	runtime          out.ContainerRuntime
	containerSvc     in.ContainerService
	configSvc        in.ConfigService
	config           Config
	targets          map[string]*domain.ProxyTarget
	mu               sync.RWMutex
	inFlight         map[string]int
	inFlightMu       sync.Mutex
	registryInFlight atomic.Int64 // active registry proxy requests, for graceful drain
}

// NewService creates a new proxy service.
func NewService(
	runtime out.ContainerRuntime,
	containerSvc in.ContainerService,
	configSvc in.ConfigService,
	config Config,
) *Service {
	return &Service{
		runtime:      runtime,
		containerSvc: containerSvc,
		configSvc:    configSvc,
		config:       config,
		targets:      make(map[string]*domain.ProxyTarget),
		inFlight:     make(map[string]int),
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

	// Check cache first
	s.mu.RLock()
	if target, exists := s.targets[domainName]; exists {
		s.mu.RUnlock()
		log.Debug().
			Str("host", target.Host).
			Int("port", target.Port).
			Str("container_id", target.ContainerID).
			Msg("using cached proxy target")
		return target, nil
	}
	s.mu.RUnlock()

	// Check if this is an external route
	externalRoutes := s.configSvc.GetExternalRoutes()
	if targetAddr, ok := externalRoutes[domainName]; ok {
		return s.resolveExternalRoute(ctx, domainName, targetAddr, log)
	}

	// Get container for this domain
	container, exists := s.containerSvc.Get(ctx, domainName)
	if !exists {
		log.Debug().Msg("container not found for domain")
		return nil, domain.ErrNoTargetAvailable
	}
	log.Debug().Str("container_id", container.ID).Str("image", container.Image).Msg("found container for domain")

	// Build target based on runtime mode

	if s.isRunningInContainer() {
		// Gordon is in a container - use container network
		meta, err := s.resolveTargetMetadata(ctx, container.Image)
		if err != nil {
			return nil, log.WrapErr(err, "failed to resolve target metadata")
		}
		containerIP, _, err := s.runtime.GetContainerNetworkInfo(ctx, container.ID)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "failed to get container network info", map[string]any{zerowrap.FieldEntityID: container.ID})
		}
		target = &domain.ProxyTarget{
			Host:        containerIP,
			Port:        meta.Port,
			ContainerID: container.ID,
			Scheme:      "http",
			Protocol:    meta.Protocol,
			RouteHost:   domainName,
		}
	} else {
		// Gordon is on the host - use host port mapping
		routes := s.configSvc.GetRoutes(ctx)
		var route *domain.Route
		for _, r := range routes {
			if r.Domain == domainName {
				route = &r
				break
			}
		}

		if route == nil {
			return nil, domain.ErrRouteNotFound
		}

		meta, err := s.resolveTargetMetadata(ctx, container.Image)
		if err != nil {
			return nil, log.WrapErr(err, "failed to resolve target metadata")
		}

		hostPort, err := s.runtime.GetContainerPort(ctx, container.ID, meta.Port)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "failed to get host port mapping", map[string]any{"internal_port": meta.Port})
		}

		target = &domain.ProxyTarget{
			Host:        "localhost",
			Port:        hostPort,
			ContainerID: container.ID,
			Scheme:      "http",
			Protocol:    meta.Protocol,
			RouteHost:   domainName,
		}
	}

	// Cache the target
	s.mu.Lock()
	s.targets[domainName] = target
	s.mu.Unlock()

	return target, nil
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
	s.targets[domainName] = t
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

	s.targets[canonicalDomain] = target
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
func (s *Service) InvalidateTarget(_ context.Context, domainName string) {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

// RefreshTargets refreshes all proxy targets from container state.
func (s *Service) RefreshTargets(ctx context.Context) error {
	s.mu.Lock()
	s.targets = make(map[string]*domain.ProxyTarget)
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

// IsKnownHost returns true if host is configured as registry, route, or external route.
func (s *Service) IsKnownHost(ctx context.Context, host string) bool {
	canonicalHost, ok := domain.CanonicalRouteDomain(host)
	if !ok {
		return false
	}
	if s.IsRegistryDomain(canonicalHost) {
		return true
	}
	if _, err := s.configSvc.GetRoute(ctx, canonicalHost); err == nil {
		return true
	}
	_, ok = s.configSvc.GetExternalRoutes()[canonicalHost]
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

func (s *Service) isRunningInContainer() bool {
	// Check for /.dockerenv
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Check cgroup for container indicators
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") ||
			strings.Contains(content, "containerd") ||
			strings.Contains(content, "podman") {
			return true
		}
	}

	// NOTE: Hostname length check (12 or 64 chars) was removed because it produced
	// false positives on hosts with short hostnames (e.g., "web-server-1" = 12 chars),
	// which would cause the proxy to use container network IPs instead of host port mappings.

	// Check environment variables
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" ||
		os.Getenv("DOCKER_CONTAINER") != "" ||
		os.Getenv("container") != "" {
		return true
	}

	return false
}
