// Package proxy implements the reverse proxy use case.
package proxy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"

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

// Service implements the ProxyService interface. Target discovery is exclusively
// delegated to snapshotProvider; the service never retains runtime or config-service
// dependencies.
type Service struct {
	snapshotProvider out.RouteSnapshotProvider
	config           Config

	mu                  sync.RWMutex
	targets             map[string]*domain.ProxyTarget
	targetGenerations   map[string]domain.RouteTargetGeneration
	latestGeneration    domain.RouteTargetGeneration
	invalidationVersion uint64
	targetLookup        singleflight.Group

	inFlight         map[string]int
	inFlightMu       sync.Mutex
	registryInFlight atomic.Int64 // active registry proxy requests, for graceful drain

	// Wait-path callbacks are package-private synchronization hooks for drain
	// contract tests. Production leaves them nil.
	waitForNoInFlightWait func()
	drainRegistryWait     func()
}

// NewService retains the pre-snapshot constructor for compatibility. All discovery
// still goes through the local RouteSnapshotProvider and NewSnapshotService.
func NewService(
	runtime out.ContainerRuntime,
	containerSvc in.ContainerService,
	configSvc in.ConfigService,
	config Config,
) *Service {
	return NewSnapshotService(NewLocalSnapshotProvider(runtime, containerSvc, configSvc, config), config)
}

// NewSnapshotService creates a proxy service backed by route snapshots.
func NewSnapshotService(provider out.RouteSnapshotProvider, config Config) *Service {
	return &Service{
		snapshotProvider:  provider,
		config:            config,
		targets:           make(map[string]*domain.ProxyTarget),
		targetGenerations: make(map[string]domain.RouteTargetGeneration),
		inFlight:          make(map[string]int),
	}
}

// GetTarget returns the proxy target for a given domain.
func (s *Service) GetTarget(ctx context.Context, domainName string) (target *domain.ProxyTarget, retErr error) {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return nil, domain.ErrNoTargetAvailable
	}
	domainName = canonicalDomain

	ctx, span := proxyTracer.Start(ctx, "proxy.get_target", trace.WithAttributes(attribute.String("domain", domainName)))
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

	if target := s.cachedTarget(domainName); target != nil {
		return target, nil
	}
	if s.snapshotProvider == nil {
		return nil, domain.ErrNoTargetAvailable
	}

	result, err, _ := s.targetLookup.Do(domainName, func() (any, error) {
		if target := s.cachedTarget(domainName); target != nil {
			return snapshotLookupResult{target: target}, nil
		}
		return s.resolveSnapshotTarget(ctx, domainName)
	})
	if err != nil {
		return nil, err
	}
	lookup := result.(snapshotLookupResult)
	if lookup.target == nil {
		return nil, domain.ErrNoTargetAvailable
	}
	return lookup.target, nil
}

type snapshotLookupResult struct {
	target *domain.ProxyTarget
}

func (s *Service) resolveSnapshotTarget(ctx context.Context, domainName string) (snapshotLookupResult, error) {
	lookupInvalidationVersion := s.invalidationVersionForLookup()
	snapshot, err := s.snapshotProvider.CurrentSnapshot(ctx)
	if err != nil {
		return snapshotLookupResult{}, fmt.Errorf("get route snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return snapshotLookupResult{}, fmt.Errorf("invalid route snapshot: %w", err)
	}
	if !s.observeSnapshot(snapshot.Generation) {
		// A delayed provider result must never resurrect an older routing view.
		return snapshotLookupResult{}, nil
	}

	entry, registry, found := findSnapshotEntry(snapshot, domainName)
	if !found || entry.Unavailable() {
		if found && entry.UnavailableReason == domain.RouteTargetUnavailableReasonPolicyBlocked {
			return snapshotLookupResult{}, domain.ErrSSRFBlocked
		}
		return snapshotLookupResult{}, nil
	}
	target, err := entry.ToProxyTarget()
	if err != nil {
		return snapshotLookupResult{}, fmt.Errorf("convert route snapshot target: %w", err)
	}
	target.Registry = registry
	result := &target
	// Draining targets remain routable for the current request but are deliberately
	// not retained: a following request must observe the drain transition.
	if entry.Ready() {
		s.cacheSnapshotTarget(domainName, result, snapshot.Generation, lookupInvalidationVersion)
	}
	return snapshotLookupResult{target: result}, nil
}

// findSnapshotEntry returns target data and its routing kind from one immutable
// snapshot. Registry classification must travel with the selected target rather
// than coming from edge-local configuration.
func findSnapshotEntry(snapshot domain.RouteTargetSnapshot, domainName string) (domain.RouteTargetEntry, bool, bool) {
	for _, entry := range snapshot.Entries {
		if entry.CanonicalDomain == domainName {
			return entry, false, true
		}
	}
	if snapshot.RegistryForwardingTarget != nil && snapshot.RegistryForwardingTarget.CanonicalDomain == domainName {
		return *snapshot.RegistryForwardingTarget, true, true
	}
	return domain.RouteTargetEntry{}, false, false
}

func (s *Service) cachedTarget(domainName string) *domain.ProxyTarget {
	s.mu.RLock()
	target := s.targets[domainName]
	s.mu.RUnlock()
	return target
}

func (s *Service) invalidationVersionForLookup() uint64 {
	s.mu.RLock()
	version := s.invalidationVersion
	s.mu.RUnlock()
	return version
}

// observeSnapshot accepts only the current or newer routing view. Observing a
// newer generation invalidates every older cached target, including targets for
// domains not present in the new view.
func (s *Service) observeSnapshot(generation domain.RouteTargetGeneration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation < s.latestGeneration {
		return false
	}
	if generation > s.latestGeneration {
		s.latestGeneration = generation
		s.targets = make(map[string]*domain.ProxyTarget)
		s.targetGenerations = make(map[string]domain.RouteTargetGeneration)
	}
	return true
}

func (s *Service) cacheSnapshotTarget(domainName string, target *domain.ProxyTarget, generation domain.RouteTargetGeneration, lookupInvalidationVersion uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latestGeneration != generation || s.invalidationVersion != lookupInvalidationVersion {
		return
	}
	s.targets[domainName] = target
	s.targetGenerations[domainName] = generation
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
	delete(s.targetGenerations, canonicalDomain)
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
	delete(s.targetGenerations, canonicalDomain)
	s.invalidationVersion++
	return nil
}

// InvalidateTarget removes a cached proxy target, forcing re-lookup on next request.
func (s *Service) InvalidateTarget(_ context.Context, domainName string) {
	canonicalDomain, ok := domain.CanonicalRouteDomain(domainName)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.targets, canonicalDomain)
	delete(s.targetGenerations, canonicalDomain)
	s.invalidationVersion++
}

// WaitForNoInFlight waits until no requests are currently proxied to the
// given opaque target key, or until timeout/context cancellation.
func (s *Service) WaitForNoInFlight(ctx context.Context, targetKey string, timeout time.Duration) bool {
	if targetKey == "" {
		return true
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	deadline := time.Now().Add(timeout)
	for {
		s.inFlightMu.Lock()
		count := s.inFlight[targetKey]
		s.inFlightMu.Unlock()
		if count <= 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		s.notifyWaitForNoInFlightWait()
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
	s.targetGenerations = make(map[string]domain.RouteTargetGeneration)
	s.invalidationVersion++
	s.mu.Unlock()
	log := zerowrap.FromCtx(ctx)
	log.Debug().Msg("proxy targets cache cleared")
	return nil
}

// UpdateConfig updates the service configuration and invalidates old targets.
func (s *Service) UpdateConfig(config Config) {
	s.mu.Lock()
	s.config = config
	s.targets = make(map[string]*domain.ProxyTarget)
	s.targetGenerations = make(map[string]domain.RouteTargetGeneration)
	s.invalidationVersion++
	s.mu.Unlock()
	if provider, ok := s.snapshotProvider.(interface{ UpdateConfig(Config) }); ok {
		provider.UpdateConfig(config)
	}
}

// IsRegistryDomain returns true if the host matches the configured registry domain.
func (s *Service) IsRegistryDomain(host string) bool {
	canonicalHost, ok := domain.CanonicalRouteDomain(host)
	if !ok {
		return false
	}
	s.mu.RLock()
	registryDomain, configured := domain.CanonicalRouteDomain(s.config.RegistryDomain)
	s.mu.RUnlock()
	return configured && canonicalHost == registryDomain
}

// IsKnownHost returns true when the current snapshot contains the host.
func (s *Service) IsKnownHost(ctx context.Context, host string) bool {
	canonicalHost, ok := domain.CanonicalRouteDomain(host)
	if !ok {
		return false
	}
	if s.snapshotProvider == nil {
		return false
	}
	snapshot, err := s.snapshotProvider.CurrentSnapshot(ctx)
	if err != nil || snapshot.Validate() != nil || !s.observeSnapshot(snapshot.Generation) {
		return false
	}
	_, _, found := findSnapshotEntry(snapshot, canonicalHost)
	return found
}

// TrackInFlight records an in-flight request for an opaque target key.
// Returns a release function that must be called when the request completes.
func (s *Service) TrackInFlight(targetKey string) func() {
	if targetKey == "" {
		return func() {}
	}
	s.inFlightMu.Lock()
	s.inFlight[targetKey]++
	s.inFlightMu.Unlock()
	return func() {
		s.inFlightMu.Lock()
		if s.inFlight[targetKey] > 1 {
			s.inFlight[targetKey]--
		} else {
			delete(s.inFlight, targetKey)
		}
		s.inFlightMu.Unlock()
	}
}

// TrackRegistryRequest increments the registry in-flight counter.
func (s *Service) TrackRegistryRequest() { s.registryInFlight.Add(1) }

// ReleaseRegistryRequest decrements the registry in-flight counter.
func (s *Service) ReleaseRegistryRequest() { s.registryInFlight.Add(-1) }

// ProxyConfig returns the current proxy configuration for adapter use.
func (s *Service) ProxyConfig() in.ProxyServiceConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return in.ProxyServiceConfig{
		RegistryDomain: s.config.RegistryDomain, RegistryPort: s.config.RegistryPort,
		MaxBodySize: s.config.MaxBodySize, MaxResponseSize: s.config.MaxResponseSize,
		MaxConcurrentConns: s.config.MaxConcurrentConns,
	}
}

// RegistryInFlight returns the current count of active registry proxy requests.
func (s *Service) RegistryInFlight() int64 { return s.registryInFlight.Load() }

// DrainRegistryInFlight blocks until all in-flight registry proxy requests complete or timeout.
func (s *Service) DrainRegistryInFlight(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.registryInFlight.Load() == 0 {
			return true
		}
		s.notifyDrainRegistryWait()
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (s *Service) notifyWaitForNoInFlightWait() {
	if s.waitForNoInFlightWait != nil {
		s.waitForNoInFlightWait()
	}
}

func (s *Service) notifyDrainRegistryWait() {
	if s.drainRegistryWait != nil {
		s.drainRegistryWait()
	}
}
