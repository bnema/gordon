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

// Terminal reporters are removed after control accepts them. The cap bounds
// outstanding transport failures without evicting an active drain silently.
const maxPendingEdgeDrains = 1024

// Config holds configuration needed by the proxy service.
type Config struct {
	RegistryDomain     string
	RegistryPort       int
	MaxBodySize        int64 // Maximum request body size in bytes (0 = no limit)
	MaxResponseSize    int64 // Maximum response body size in bytes (0 = no limit)
	MaxConcurrentConns int   // Maximum concurrent proxy connections (0 = no limit)

	// EdgeDrainTimeout optionally bounds an edge's wait for retired application
	// traffic. Zero disables edge-side timeout reporting.
	EdgeDrainTimeout time.Duration
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

	// requestAcquireMu serializes request admission against an accepted snapshot
	// transition. A request selected from the old view is counted before that
	// transition can decide the old target has drained.
	requestAcquireMu sync.Mutex
	// acquireTargetSelected is a package-private test synchronization hook. It
	// runs after selection and before application traffic is registered.
	acquireTargetSelected func()

	inFlight         map[string]int
	inFlightMu       sync.Mutex
	registryInFlight atomic.Int64 // active registry proxy requests, for graceful drain

	drainReporter       out.EdgeDrainReporter
	drainCtx            context.Context
	drainCancel         context.CancelFunc
	drainMu             sync.Mutex
	pendingDrains       map[edgeDrainIdentity]*edgeDrain
	completedDrains     map[edgeDrainIdentity]struct{}
	completedDrainOrder []edgeDrainIdentity

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
func NewSnapshotService(provider out.RouteSnapshotProvider, config Config, reporters ...out.EdgeDrainReporter) *Service {
	var reporter out.EdgeDrainReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	drainCtx, drainCancel := context.WithCancel(context.Background())
	return &Service{
		snapshotProvider:  provider,
		config:            config,
		targets:           make(map[string]*domain.ProxyTarget),
		targetGenerations: make(map[string]domain.RouteTargetGeneration),
		inFlight:          make(map[string]int),
		drainReporter:     reporter,
		drainCtx:          drainCtx,
		drainCancel:       drainCancel,
		pendingDrains:     make(map[edgeDrainIdentity]*edgeDrain),
		completedDrains:   make(map[edgeDrainIdentity]struct{}),
	}
}

type edgeDrainIdentity struct {
	domain     string
	generation domain.RouteTargetGeneration
	key        domain.RouteTargetKey
}

type edgeDrain struct {
	state    domain.RouteDrainState
	reported bool
}

// AcquireTarget atomically admits an HTTP request to its selected application
// target. It must be used instead of GetTarget followed by TrackInFlight: an
// accepted snapshot transition may otherwise report an old target at zero in
// the gap between selection and registration.
func (s *Service) AcquireTarget(ctx context.Context, domainName string) (*domain.ProxyTarget, func(), error) {
	s.requestAcquireMu.Lock()
	defer s.requestAcquireMu.Unlock()

	target, err := s.GetTarget(ctx, domainName)
	if err != nil {
		return nil, func() {}, err
	}
	if target.Registry {
		// Registry requests have dedicated accounting and never participate in an
		// application target's drain state.
		return target, func() {}, nil
	}
	s.notifyAcquireTargetSelected()
	return target, s.TrackInFlight(inFlightKey(target)), nil
}

func inFlightKey(target *domain.ProxyTarget) string {
	if target != nil && target.TargetKey != "" {
		return string(target.TargetKey)
	}
	if target != nil {
		return target.ContainerID
	}
	return ""
}

func (s *Service) notifyAcquireTargetSelected() {
	if s.acquireTargetSelected != nil {
		s.acquireTargetSelected()
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

// ObserveAcceptedRouteSnapshot coordinates edge-only drain reporting after a
// snapshot consumer has atomically installed a strictly newer routing view.
// Registry targets are intentionally absent: registry request accounting is
// separate and must never complete an application target drain.
func (s *Service) ObserveAcceptedRouteSnapshot(previous *domain.RouteTargetSnapshot, current domain.RouteTargetSnapshot) {
	if previous == nil || s.drainReporter == nil {
		return
	}

	// Admission and transition detection share one critical section. Therefore a
	// request selected from the retired view is registered before startEdgeDrain
	// can observe zero, while later requests must resolve from the new view.
	s.requestAcquireMu.Lock()
	defer s.requestAcquireMu.Unlock()

	// Invalidate all cached routes before scheduling reports. New requests fetch
	// from the already-installed current snapshot rather than retaining an old
	// target while its drain is being acknowledged.
	s.mu.Lock()
	if current.Generation >= s.latestGeneration {
		s.latestGeneration = current.Generation
		s.targets = make(map[string]*domain.ProxyTarget)
		s.targetGenerations = make(map[string]domain.RouteTargetGeneration)
	}
	s.mu.Unlock()

	currentTargets := make(map[string]domain.RouteTargetKey, len(current.Entries))
	for _, entry := range current.Entries {
		if entry.Ready() || entry.Draining() {
			currentTargets[entry.CanonicalDomain] = entry.TargetKey
		}
	}
	for _, old := range previous.Entries {
		if !old.Ready() || currentTargets[old.CanonicalDomain] == old.TargetKey {
			continue
		}
		s.startEdgeDrain(old.CanonicalDomain, current.Generation, old.TargetKey)
	}
}

// Close cancels pending edge reporting timers and retry calls. It is a no-op
// for the monolith, whose local drain waiter remains independent.
func (s *Service) Close() {
	if s.drainCancel != nil {
		s.drainCancel()
	}
}

func (s *Service) startEdgeDrain(routeDomain string, generation domain.RouteTargetGeneration, key domain.RouteTargetKey) {
	identity := edgeDrainIdentity{domain: routeDomain, generation: generation, key: key}
	s.drainMu.Lock()
	if _, exists := s.pendingDrains[identity]; exists {
		s.drainMu.Unlock()
		return
	}
	if _, completed := s.completedDrains[identity]; completed {
		s.drainMu.Unlock()
		return
	}
	if len(s.pendingDrains) >= maxPendingEdgeDrains {
		s.drainMu.Unlock()
		return
	}
	drain := &edgeDrain{state: domain.RouteDrainState{
		CanonicalDomain: routeDomain, TransitionGeneration: generation, OldTargetKey: key,
	}}
	s.pendingDrains[identity] = drain
	s.drainMu.Unlock()

	s.inFlightMu.Lock()
	inFlight := s.inFlight[string(key)]
	s.inFlightMu.Unlock()
	if inFlight == 0 {
		s.completeEdgeDrain(identity, 0, domain.RouteDrainTimeoutReasonNone)
		return
	}
	if s.config.EdgeDrainTimeout > 0 {
		go s.waitForEdgeDrainTimeout(identity, s.config.EdgeDrainTimeout)
	}
}

func (s *Service) waitForEdgeDrainTimeout(identity edgeDrainIdentity, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.drainCtx.Done():
		return
	case <-timer.C:
	}
	s.inFlightMu.Lock()
	remaining := s.inFlight[string(identity.key)]
	s.inFlightMu.Unlock()
	if remaining > 0 {
		s.completeEdgeDrain(identity, remaining, domain.RouteDrainTimeoutReasonEdge)
	}
}

func (s *Service) completeEdgeDrain(identity edgeDrainIdentity, inFlight int, reason domain.RouteDrainTimeoutReason) {
	s.drainMu.Lock()
	drain, exists := s.pendingDrains[identity]
	if !exists || drain.reported {
		s.drainMu.Unlock()
		return
	}
	drain.reported = true
	if inFlight > 0 {
		drain.state.InFlight = uint64(inFlight)
	} else {
		drain.state.InFlight = 0
	}
	drain.state.TimeoutReason = reason
	drain.state.AcknowledgedAt = time.Now().UTC()
	state := drain.state
	s.drainMu.Unlock()
	go s.reportEdgeDrain(identity, state)
}

func (s *Service) reportEdgeDrain(identity edgeDrainIdentity, state domain.RouteDrainState) {
	// Control's ledger makes retries idempotent. Keep retry work bounded and
	// detached from request completion paths; each call has a deadline so Close
	// reliably releases any blocked transport call.
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(s.drainCtx, 5*time.Second)
		err := s.drainReporter.ReportDrainState(ctx, state)
		cancel()
		if err == nil {
			s.drainMu.Lock()
			if drain := s.pendingDrains[identity]; drain != nil && drain.reported {
				delete(s.pendingDrains, identity)
				s.completedDrains[identity] = struct{}{}
				s.completedDrainOrder = append(s.completedDrainOrder, identity)
				for len(s.completedDrainOrder) > maxPendingEdgeDrains {
					old := s.completedDrainOrder[0]
					s.completedDrainOrder = s.completedDrainOrder[1:]
					delete(s.completedDrains, old)
				}
			}
			s.drainMu.Unlock()
			return
		}
		if s.drainCtx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-s.drainCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	// Keep the reporter active after bounded transport retries. A later
	// transition/retry can retry it; it is never falsely considered terminal.
	s.drainMu.Lock()
	if drain := s.pendingDrains[identity]; drain != nil {
		drain.reported = false
	}
	s.drainMu.Unlock()
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
	var once sync.Once
	return func() {
		once.Do(func() {
			s.inFlightMu.Lock()
			if s.inFlight[targetKey] > 1 {
				s.inFlight[targetKey]--
			} else {
				delete(s.inFlight, targetKey)
			}
			remaining := s.inFlight[targetKey]
			s.inFlightMu.Unlock()
			if remaining == 0 {
				s.completeDrainsForTarget(domain.RouteTargetKey(targetKey))
			}
		})
	}
}

func (s *Service) completeDrainsForTarget(key domain.RouteTargetKey) {
	s.drainMu.Lock()
	identities := make([]edgeDrainIdentity, 0)
	for identity, drain := range s.pendingDrains {
		if identity.key == key && !drain.reported {
			identities = append(identities, identity)
		}
	}
	s.drainMu.Unlock()
	for _, identity := range identities {
		s.completeEdgeDrain(identity, 0, domain.RouteDrainTimeoutReasonNone)
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
