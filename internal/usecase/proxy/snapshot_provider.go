package proxy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// LocalSnapshotProvider derives a routing-only snapshot from the local config
// and managed container state. It deliberately never publishes container IDs,
// image metadata, labels, environment, or runtime paths.
type LocalSnapshotProvider struct {
	runtime      out.ContainerRuntime
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	config       Config
	inContainer  func() bool

	mu                         sync.Mutex
	last                       domain.RouteTargetSnapshot
	hasLast                    bool
	containerTargetKeys        map[string]containerTargetKeyAssociation
	currentContainerTargetKeys map[string]containerTargetKeyAssociation
	preparedDrains             map[string]struct{}
}

type containerTargetKeyAssociation struct {
	key    domain.RouteTargetKey
	domain string
}

var _ out.RouteSnapshotProvider = (*LocalSnapshotProvider)(nil)

// NewLocalSnapshotProvider creates the monolith snapshot source. Loopback
// endpoints are intentional here; split-edge reachability is validated later.
func NewLocalSnapshotProvider(runtime out.ContainerRuntime, containerSvc in.ContainerService, configSvc in.ConfigService, config Config) *LocalSnapshotProvider {
	return &LocalSnapshotProvider{
		runtime: runtime, containerSvc: containerSvc, configSvc: configSvc, config: config,
		inContainer:                runningInContainer,
		containerTargetKeys:        make(map[string]containerTargetKeyAssociation),
		currentContainerTargetKeys: make(map[string]containerTargetKeyAssociation),
		preparedDrains:             make(map[string]struct{}),
	}
}

// CurrentSnapshot returns a validated, independent snapshot. Generation changes
// only when routing-significant content changes.
func (p *LocalSnapshotProvider) CurrentSnapshot(ctx context.Context) (domain.RouteTargetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.RouteTargetSnapshot{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	candidate, containerTargetKeys, err := p.buildSnapshot(ctx, p.nextGeneration())
	if err != nil {
		return domain.RouteTargetSnapshot{}, err
	}
	if err := candidate.Validate(); err != nil {
		return domain.RouteTargetSnapshot{}, fmt.Errorf("validate local route snapshot: %w", err)
	}
	// This map is deliberately private: it bridges the container lifecycle's
	// legacy ID to the edge's opaque target key without publishing IDs.
	p.mergeContainerTargetKeys(containerTargetKeys)
	if p.hasLast && sameRoutingContent(candidate, p.last) {
		return p.last.Clone(), nil
	}
	p.last = candidate.Clone()
	p.hasLast = true
	return candidate.Clone(), nil
}

// UpdateConfig applies proxy configuration used by future snapshots.
func (p *LocalSnapshotProvider) UpdateConfig(config Config) {
	p.mu.Lock()
	p.config = config
	p.mu.Unlock()
}

func (p *LocalSnapshotProvider) nextGeneration() domain.RouteTargetGeneration {
	if !p.hasLast {
		return 1
	}
	return p.last.Generation + 1
}

func (p *LocalSnapshotProvider) buildSnapshot(ctx context.Context, generation domain.RouteTargetGeneration) (domain.RouteTargetSnapshot, map[string]containerTargetKeyAssociation, error) {
	if p.configSvc == nil {
		return domain.RouteTargetSnapshot{}, nil, fmt.Errorf("local snapshot provider config service is required")
	}
	routes := p.configSvc.GetRoutes(ctx)
	if err := ctx.Err(); err != nil {
		return domain.RouteTargetSnapshot{}, nil, err
	}
	externalRoutes := p.configSvc.GetExternalRoutes()
	if err := ctx.Err(); err != nil {
		return domain.RouteTargetSnapshot{}, nil, err
	}

	managed, err := canonicalManagedRoutes(routes)
	if err != nil {
		return domain.RouteTargetSnapshot{}, nil, err
	}
	external, err := canonicalExternalRoutes(externalRoutes)
	if err != nil {
		return domain.RouteTargetSnapshot{}, nil, err
	}

	registryDomain, registryEnabled := domain.CanonicalRouteDomain(p.config.RegistryDomain)
	entries, containerTargetKeys, err := p.snapshotEntries(ctx, managed, external, registryDomain, registryEnabled, generation)
	if err != nil {
		return domain.RouteTargetSnapshot{}, nil, err
	}

	snapshot := domain.RouteTargetSnapshot{Generation: generation, Entries: entries}
	if registryEnabled {
		registry, err := registrySnapshotEntry(registryDomain, p.config.RegistryPort, generation)
		if err != nil {
			return domain.RouteTargetSnapshot{}, nil, err
		}
		snapshot.RegistryForwardingTarget = &registry
	}
	return snapshot, containerTargetKeys, nil
}

// TargetKeyForContainer returns the private lifecycle-to-edge association.
// It never exposes container IDs through a snapshot.
func (p *LocalSnapshotProvider) TargetKeyForContainer(containerID string) (domain.RouteTargetKey, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	association, ok := p.containerTargetKeys[containerID]
	return association.key, ok
}

// PrepareDrain pins a current lifecycle association before traffic switches.
// Invariant: refreshes prune every non-current association unless it is pinned
// here; WaitForNoInFlight or CancelDrain always releases the pin. This keeps
// removed-route IDs bounded without evicting an old target during its drain.
func (p *LocalSnapshotProvider) PrepareDrain(containerID string) bool {
	if p == nil || containerID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, found := p.containerTargetKeys[containerID]; found {
		p.preparedDrains[containerID] = struct{}{}
		return true
	}
	return false
}

// ReleaseTargetKeyForContainer drops a prepared lifecycle association after
// its drain wait succeeds, times out, or is cancelled. A currently routed ID
// is never removed, so a delayed release cannot remove a newer association.
func (p *LocalSnapshotProvider) ReleaseTargetKeyForContainer(containerID string) {
	if p == nil || containerID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.preparedDrains, containerID)
	if _, current := p.currentContainerTargetKeys[containerID]; !current {
		delete(p.containerTargetKeys, containerID)
	}
}

func (p *LocalSnapshotProvider) snapshotEntries(
	ctx context.Context,
	managed []localManagedRoute,
	external map[string]string,
	registryDomain string,
	registryEnabled bool,
	generation domain.RouteTargetGeneration,
) ([]domain.RouteTargetEntry, map[string]containerTargetKeyAssociation, error) {
	entries := make([]domain.RouteTargetEntry, 0, len(managed)+len(external))
	containerTargetKeys := make(map[string]containerTargetKeyAssociation, len(managed))
	for _, route := range managed {
		if registryEnabled && route.Domain == registryDomain {
			continue
		}
		if _, externalOverride := external[route.Domain]; externalOverride {
			continue
		}
		entry, containerID, err := p.managedSnapshotEntry(ctx, route, generation)
		if err != nil {
			return nil, nil, err
		}
		if containerID != "" && !entry.Unavailable() {
			containerTargetKeys[containerID] = containerTargetKeyAssociation{key: entry.TargetKey, domain: route.Domain}
		}
		entries = append(entries, entry)
	}
	for domainName, targetAddr := range external {
		if registryEnabled && domainName == registryDomain {
			continue
		}
		entry, err := externalUnavailableOnError(ctx, domainName, targetAddr, generation)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CanonicalDomain < entries[j].CanonicalDomain })
	return entries, containerTargetKeys, nil
}

// mergeContainerTargetKeys bounds lifecycle associations to current targets,
// explicitly prepared drains, and one legacy immediately preceding replacement.
// The legacy slot preserves callers that begin a drain just after refresh; new
// lifecycle callers pin before switching traffic via PrepareDrain. A removed
// route has no replacement, so all of its unprepared IDs are pruned immediately.
func (p *LocalSnapshotProvider) mergeContainerTargetKeys(current map[string]containerTargetKeyAssociation) {
	previousCurrentByDomain := make(map[string]string, len(p.currentContainerTargetKeys))
	for containerID, association := range p.currentContainerTargetKeys {
		previousCurrentByDomain[association.domain] = containerID
	}
	currentByDomain := make(map[string]string, len(current))
	for containerID, association := range current {
		currentByDomain[association.domain] = containerID
	}
	for containerID, association := range p.containerTargetKeys {
		if _, isCurrent := current[containerID]; isCurrent {
			continue
		}
		if _, prepared := p.preparedDrains[containerID]; prepared {
			continue
		}
		previousID := previousCurrentByDomain[association.domain]
		currentID, replaced := currentByDomain[association.domain]
		if replaced && previousID == containerID && currentID != containerID {
			continue
		}
		delete(p.containerTargetKeys, containerID)
	}
	maps.Copy(p.containerTargetKeys, current)
	p.currentContainerTargetKeys = current
}

func (p *LocalSnapshotProvider) managedSnapshotEntry(ctx context.Context, route localManagedRoute, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, string, error) {
	entry, containerID, err := p.managedEntry(ctx, route, generation)
	if err == nil || ctx.Err() != nil {
		return entry, containerID, err
	}
	entry, entryErr := domain.NewUnavailableRouteTargetEntry(route.Domain, domain.RouteTargetUnavailableReasonDeployment, generation)
	if entryErr == nil {
		logRouteUnavailable(ctx, route.Domain, domain.RouteTargetUnavailableReasonDeployment)
	}
	return entry, containerID, entryErr
}

func externalUnavailableOnError(ctx context.Context, domainName, targetAddr string, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, error) {
	entry, err := ResolveExternalRouteTarget(domainName, targetAddr, generation)
	if err == nil || ctx.Err() != nil {
		return entry, err
	}
	reason := domain.RouteTargetUnavailableReasonNoTarget
	if errors.Is(err, domain.ErrSSRFBlocked) {
		reason = domain.RouteTargetUnavailableReasonPolicyBlocked
	}
	entry, entryErr := domain.NewUnavailableRouteTargetEntry(domainName, reason, generation)
	if entryErr == nil {
		logRouteUnavailable(ctx, domainName, reason)
	}
	return entry, entryErr
}

type localManagedRoute struct {
	Domain string
	Image  string
}

func canonicalManagedRoutes(routes []domain.Route) ([]localManagedRoute, error) {
	byDomain := make(map[string]string, len(routes))
	for _, route := range routes {
		domainName, ok := domain.CanonicalRouteDomain(route.Domain)
		if !ok {
			return nil, fmt.Errorf("%w: configured route domain %q", domain.ErrRouteDomainInvalid, route.Domain)
		}
		if _, exists := byDomain[domainName]; exists {
			return nil, fmt.Errorf("duplicate configured route domain %q", domainName)
		}
		byDomain[domainName] = route.Image
	}
	result := make([]localManagedRoute, 0, len(byDomain))
	for domainName, image := range byDomain {
		result = append(result, localManagedRoute{Domain: domainName, Image: image})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Domain < result[j].Domain })
	return result, nil
}

func canonicalExternalRoutes(routes map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(routes))
	for rawDomain, target := range routes {
		domainName, ok := domain.CanonicalRouteDomain(rawDomain)
		if !ok {
			return nil, fmt.Errorf("%w: configured external route domain %q", domain.ErrRouteDomainInvalid, rawDomain)
		}
		if _, exists := result[domainName]; exists {
			return nil, fmt.Errorf("duplicate configured external route domain %q", domainName)
		}
		result[domainName] = target
	}
	return result, nil
}

func (p *LocalSnapshotProvider) managedEntry(ctx context.Context, route localManagedRoute, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, string, error) {
	if p.containerSvc == nil || p.runtime == nil {
		return domain.RouteTargetEntry{}, "", fmt.Errorf("local snapshot provider managed-route dependencies are required")
	}
	container, found := p.containerSvc.Get(ctx, route.Domain)
	if err := ctx.Err(); err != nil {
		return domain.RouteTargetEntry{}, "", err
	}
	if !found || container == nil {
		entry, err := domain.NewUnavailableRouteTargetEntry(route.Domain, domain.RouteTargetUnavailableReasonNoTarget, generation)
		return entry, "", err
	}
	if container.Status != "" && !strings.EqualFold(container.Status, string(domain.ContainerStatusRunning)) {
		entry, err := domain.NewUnavailableRouteTargetEntry(route.Domain, domain.RouteTargetUnavailableReasonStarting, generation)
		return entry, "", err
	}
	image := container.Image
	if image == "" {
		image = route.Image
	}
	metadata, err := resolveTargetMetadata(ctx, p.runtime, image)
	if err != nil {
		return domain.RouteTargetEntry{}, container.ID, err
	}
	protocol := domain.RouteTargetProtocolHTTP1
	if metadata.Protocol == string(domain.RouteTargetProtocolH2C) {
		protocol = domain.RouteTargetProtocolH2C
	}
	if p.inContainer() {
		// The deployment installs this stable alias on the route network. It is
		// independent of the backing container identity during replacement.
		entry, err := domain.NewManagedReadyRouteTargetEntry(route.Domain, targetAlias(route.Domain), metadata.Port, "http", protocol, generation, container.ID)
		return entry, container.ID, err
	}
	hostPort, err := p.runtime.GetContainerPort(ctx, container.ID, metadata.Port)
	if err != nil {
		return domain.RouteTargetEntry{}, container.ID, fmt.Errorf("get host port mapping: %w", err)
	}
	entry, err := domain.NewManagedReadyRouteTargetEntry(route.Domain, "localhost", hostPort, "http", protocol, generation, container.ID)
	return entry, container.ID, err
}

func logRouteUnavailable(ctx context.Context, domainName string, reason domain.RouteTargetUnavailableReason) {
	log := zerowrap.FromCtx(ctx)
	log.Warn().Str("domain", domainName).Str("reason", string(reason)).Msg("route target unavailable")
}

func targetAlias(domainName string) string {
	return "gordon-target-" + strings.ReplaceAll(strings.ToLower(domainName), ".", "-")
}

func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") || strings.Contains(content, "containerd") || strings.Contains(content, "podman") {
			return true
		}
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST") != "" || os.Getenv("DOCKER_CONTAINER") != "" || os.Getenv("container") != ""
}

// ResolveExternalRouteTarget pins and validates an external endpoint before it
// is placed in a routing snapshot. The configured host remains the upstream
// Host header while the resolved public address is used for dialing.
func ResolveExternalRouteTarget(domainName, targetAddr string, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, error) {
	host, portText, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return domain.RouteTargetEntry{}, fmt.Errorf("invalid external route target: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return domain.RouteTargetEntry{}, fmt.Errorf("invalid port in external route %q", targetAddr)
	}
	resolvedHost, err := ResolveAndValidateHost(host)
	if err != nil {
		return domain.RouteTargetEntry{}, err
	}
	// External entries always retain the configured endpoint host. For an IP
	// literal this is equivalent to the dial host; for DNS it preserves virtual
	// host routing while the resolved address remains pinned for dialing.
	return domain.NewExternalReadyRouteTargetEntry(domainName, resolvedHost, host, port, "http", domain.RouteTargetProtocolHTTP1, generation)
}

func registrySnapshotEntry(registryDomain string, registryPort int, generation domain.RouteTargetGeneration) (domain.RouteTargetEntry, error) {
	if registryPort < 1 || registryPort > 65535 {
		return domain.RouteTargetEntry{}, fmt.Errorf("invalid registry forwarding port %d", registryPort)
	}
	return domain.NewReadyRouteTargetEntry(registryDomain, "localhost", registryPort, "http", domain.RouteTargetProtocolHTTP1, generation)
}

func sameRoutingContent(left, right domain.RouteTargetSnapshot) bool {
	left = left.Clone()
	right = right.Clone()
	left.Generation = 0
	right.Generation = 0
	for index := range left.Entries {
		left.Entries[index].Generation = 0
	}
	for index := range right.Entries {
		right.Entries[index].Generation = 0
	}
	if left.RegistryForwardingTarget != nil {
		left.RegistryForwardingTarget.Generation = 0
	}
	if right.RegistryForwardingTarget != nil {
		right.RegistryForwardingTarget.Generation = 0
	}
	return fmt.Sprintf("%#v", left) == fmt.Sprintf("%#v", right)
}
