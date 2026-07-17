package edgesnapshot

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	defaultProducerEdgeAlias = "gordon-edge"
	producerRetryBackoff     = 100 * time.Millisecond
	producerMaxBackoff       = 5 * time.Second
)

// RegistryTarget is the control-owned split-network contract for the registry.
// It deliberately contains a network alias rather than a localhost address or
// a runtime/container identifier.
type RegistryTarget struct {
	Domain   string
	Alias    string
	Port     int
	Scheme   string
	Protocol domain.RouteTargetProtocol
}

// ProducerOptions are control-owned, already-sanitized routing additions. The
// runtime subscriber never supplies these values.
type ProducerOptions struct {
	EdgeAlias string
	Registry  *RegistryTarget
	External  []domain.RouteTargetEntry
}

// Producer translates a narrow runtime-state subscription into immutable edge
// route snapshots. It has no runtime adapter or socket dependency.
type Producer struct {
	subscriber out.RuntimeStateSubscriber
	hub        *SnapshotHub
	edgeAlias  string
	registry   *RegistryTarget
	external   []domain.RouteTargetEntry

	mu         sync.Mutex
	started    bool
	generation uint64
}

// NewProducer validates control-owned routing contracts before accepting
// runtime state. A missing subscriber or hub is always a startup error.
func NewProducer(subscriber out.RuntimeStateSubscriber, hub *SnapshotHub, options ProducerOptions) (*Producer, error) {
	if subscriber == nil {
		return nil, fmt.Errorf("runtime state subscriber is required")
	}
	if hub == nil {
		return nil, fmt.Errorf("route snapshot hub is required")
	}
	edgeAlias := strings.TrimSpace(options.EdgeAlias)
	if edgeAlias == "" {
		edgeAlias = defaultProducerEdgeAlias
	}
	if !safeSplitAlias(edgeAlias) {
		return nil, fmt.Errorf("edge alias must be a non-loopback alias")
	}
	if options.Registry != nil {
		if err := validateRegistryTarget(*options.Registry); err != nil {
			return nil, err
		}
	}
	external := make([]domain.RouteTargetEntry, len(options.External))
	externalDomains := make(map[string]struct{}, len(options.External))
	for index, entry := range options.External {
		if err := entry.ValidateSplitReachability(); err != nil {
			return nil, fmt.Errorf("external target %d: %w", index, err)
		}
		if entry.UpstreamHost == "" || entry.Attachment != domain.RouteTargetAttachmentNotRequired {
			return nil, fmt.Errorf("external target %d must be an external route entry", index)
		}
		if _, exists := externalDomains[entry.CanonicalDomain]; exists {
			return nil, fmt.Errorf("duplicate external target domain %q", entry.CanonicalDomain)
		}
		externalDomains[entry.CanonicalDomain] = struct{}{}
		external[index] = entry
	}
	if options.Registry != nil {
		registry, err := registryRouteEntry(*options.Registry, 1)
		if err != nil {
			return nil, err
		}
		if _, exists := externalDomains[registry.CanonicalDomain]; exists {
			return nil, fmt.Errorf("external target duplicates registry domain %q", registry.CanonicalDomain)
		}
	}
	return &Producer{subscriber: subscriber, hub: hub, edgeAlias: edgeAlias, registry: cloneRegistryTarget(options.Registry), external: external}, nil
}

// Start waits for a validated initial runtime snapshot before returning. Thus a
// split control process cannot expose an empty hub indefinitely. Subsequent
// subscription closures/errors are retried with bounded backoff.
func (p *Producer) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return fmt.Errorf("route snapshot producer already started")
	}
	p.started = true
	p.mu.Unlock()

	updates, err := p.waitForInitial(ctx)
	if err != nil {
		return err
	}
	go p.run(ctx, updates)
	return nil
}

func (p *Producer) waitForInitial(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	backoff := producerRetryBackoff
	for {
		updates, err := p.subscriber.SubscribeRuntimeState(ctx)
		if err == nil && updates != nil {
		subscription:
			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case snapshot, open := <-updates:
					if !open {
						break subscription
					}
					// Invalid runtime state is not a control routing conflict; wait for
					// the next valid state rather than making startup transiently fail.
					if snapshot.Validate() != nil {
						continue
					}
					published, publishErr := p.publish(snapshot)
					if publishErr != nil {
						return nil, publishErr
					}
					if published {
						return updates, nil
					}
				}
			}
		}
		if err := waitForRetry(ctx, backoff); err != nil {
			return nil, err
		}
		backoff = nextProducerBackoff(backoff)
	}
}

func (p *Producer) run(ctx context.Context, updates <-chan domain.RuntimeActualStateSnapshot) {
	backoff := producerRetryBackoff
	for {
		subscriptionClosed := false
		for !subscriptionClosed {
			select {
			case <-ctx.Done():
				return
			case snapshot, open := <-updates:
				if !open {
					subscriptionClosed = true
					continue
				}
				// Later malformed runtime views are fail-closed: they are never
				// published over the last known-good snapshot.
				_, _ = p.publish(snapshot)
			}
		}
		if err := waitForRetry(ctx, backoff); err != nil {
			return
		}
		next, err := p.subscriber.SubscribeRuntimeState(ctx)
		if err != nil || next == nil {
			backoff = nextProducerBackoff(backoff)
			continue
		}
		updates = next
		backoff = producerRetryBackoff
	}
}

func (p *Producer) publish(runtimeSnapshot domain.RuntimeActualStateSnapshot) (bool, error) {
	if err := runtimeSnapshot.Validate(); err != nil {
		return false, fmt.Errorf("invalid runtime snapshot: %w", err)
	}
	p.mu.Lock()
	if runtimeSnapshot.Generation <= p.generation {
		p.mu.Unlock()
		return false, nil
	}
	p.mu.Unlock()

	snapshot, err := p.snapshotFromRuntime(runtimeSnapshot)
	if err != nil {
		return false, err
	}
	if err := p.hub.Publish(snapshot); err != nil {
		return false, err
	}
	p.mu.Lock()
	p.generation = runtimeSnapshot.Generation
	p.mu.Unlock()
	return true, nil
}

func (p *Producer) snapshotFromRuntime(runtimeSnapshot domain.RuntimeActualStateSnapshot) (domain.RouteTargetSnapshot, error) {
	entries := make([]domain.RouteTargetEntry, 0, len(runtimeSnapshot.Routes)+len(p.external))
	seen := make(map[string]struct{}, len(runtimeSnapshot.Routes)+len(p.external)+1)
	for _, route := range runtimeSnapshot.Routes {
		entry, err := p.entryFromRoute(route, runtimeSnapshot.EdgeAttachments, domain.RouteTargetGeneration(runtimeSnapshot.Generation))
		if err != nil {
			return domain.RouteTargetSnapshot{}, err
		}
		if _, exists := seen[entry.CanonicalDomain]; exists {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("duplicate runtime route domain %q", entry.CanonicalDomain)
		}
		seen[entry.CanonicalDomain] = struct{}{}
		entries = append(entries, entry)
	}
	for _, entry := range p.external {
		entry.Generation = domain.RouteTargetGeneration(runtimeSnapshot.Generation)
		if _, exists := seen[entry.CanonicalDomain]; exists {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("external target duplicates route domain %q", entry.CanonicalDomain)
		}
		seen[entry.CanonicalDomain] = struct{}{}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CanonicalDomain < entries[j].CanonicalDomain })

	snapshot := domain.RouteTargetSnapshot{Generation: domain.RouteTargetGeneration(runtimeSnapshot.Generation), Entries: entries}
	if p.registry != nil {
		registry, err := registryRouteEntry(*p.registry, snapshot.Generation)
		if err != nil {
			return domain.RouteTargetSnapshot{}, err
		}
		if _, exists := seen[registry.CanonicalDomain]; exists {
			return domain.RouteTargetSnapshot{}, fmt.Errorf("registry target duplicates route domain %q", registry.CanonicalDomain)
		}
		snapshot.RegistryForwardingTarget = &registry
	}
	return snapshot, nil
}

func (p *Producer) entryFromRoute(route domain.RuntimeRouteState, attachments []domain.RuntimeEdgeNetworkAttachmentState, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, error) {
	if route.Status == domain.RouteTargetStatusUnavailable {
		return domain.NewUnavailableRouteTargetEntry(route.Domain, route.UnavailableReason, generation)
	}
	if route.Status != domain.RouteTargetStatusReady && route.Status != domain.RouteTargetStatusDraining {
		return domain.RouteTargetEntry{}, fmt.Errorf("unsupported route status %q", route.Status)
	}
	if !p.matchingAttachment(route, attachments) || route.EdgeTargetAlias != managedTargetAlias(route.Domain) || !safeSplitAlias(route.EdgeTargetAlias) || strings.TrimSpace(route.BackingContainerName) == "" {
		return domain.NewUnavailableRouteTargetEntry(route.Domain, domain.RouteTargetUnavailableReasonNoTarget, generation)
	}
	// BackingContainerName and RouteVersion are only private key material. The
	// constructor hashes them and no resulting entry retains either value.
	identity := route.BackingContainerName + "\x00" + route.RouteVersion
	entry, err := domain.NewManagedReadyRouteTargetEntry(route.Domain, route.EdgeTargetAlias, route.TargetPort, route.Scheme, route.Protocol, generation, identity)
	if err != nil {
		return domain.RouteTargetEntry{}, err
	}
	if route.Status == domain.RouteTargetStatusDraining {
		entry.Status = domain.RouteTargetStatusDraining
		entry.UnavailableReason = domain.RouteTargetUnavailableReasonDraining
	}
	return entry, nil
}

func (p *Producer) matchingAttachment(route domain.RuntimeRouteState, attachments []domain.RuntimeEdgeNetworkAttachmentState) bool {
	canonicalDomain, ok := domain.CanonicalRouteDomain(route.Domain)
	if !ok {
		return false
	}
	for _, attachment := range attachments {
		attachmentDomain, domainOK := domain.CanonicalRouteDomain(attachment.RouteDomain)
		if attachment.Attached && domainOK && attachmentDomain == canonicalDomain && attachment.EdgeAlias == p.edgeAlias && attachment.TargetAlias == route.EdgeTargetAlias && attachment.TargetPort == route.TargetPort {
			return true
		}
	}
	return false
}

func managedTargetAlias(routeDomain string) string {
	canonicalDomain, ok := domain.CanonicalRouteDomain(routeDomain)
	if !ok {
		return ""
	}
	return "gordon-target-" + strings.ReplaceAll(canonicalDomain, ".", "-")
}

func registryRouteEntry(target RegistryTarget, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, error) {
	scheme := target.Scheme
	if scheme == "" {
		scheme = "http"
	}
	protocol := target.Protocol
	if protocol == "" {
		protocol = domain.RouteTargetProtocolHTTP1
	}
	return domain.NewReadyRouteTargetEntry(target.Domain, target.Alias, target.Port, scheme, protocol, generation)
}

func validateRegistryTarget(target RegistryTarget) error {
	if !safeSplitAlias(target.Alias) {
		return fmt.Errorf("registry alias must be a non-loopback alias")
	}
	_, err := registryRouteEntry(target, 1)
	if err != nil {
		return fmt.Errorf("invalid registry target: %w", err)
	}
	return nil
}

func safeSplitAlias(alias string) bool {
	alias = strings.TrimSpace(strings.ToLower(alias))
	if alias == "" || alias == "localhost" || strings.HasSuffix(alias, ".localhost") || net.ParseIP(alias) != nil {
		return false
	}
	// Split aliases are intentionally controlled aliases, not an arbitrary
	// runtime container name or host endpoint.
	return strings.HasPrefix(alias, "gordon-") && !strings.ContainsAny(alias, "/\\@?#:")
}

func cloneRegistryTarget(target *RegistryTarget) *RegistryTarget {
	if target == nil {
		return nil
	}
	clone := *target
	return &clone
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextProducerBackoff(backoff time.Duration) time.Duration {
	if backoff >= producerMaxBackoff/2 {
		return producerMaxBackoff
	}
	return backoff * 2
}
