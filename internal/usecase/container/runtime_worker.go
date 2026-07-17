package container

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	runtimeWorkerTargetAliasPrefix      = "gordon-target-"
	runtimeWorkerCompletedResultLimit   = 1024
	runtimeWorkerPolicyDeniedEventLimit = 256
)

var errRuntimeSelfUpdateUnavailable = errors.New("local runtime self-update adapter is not available")

// runtimeWorkerService is the subset of Service used by the local runtime worker adapter.
type runtimeWorkerService interface {
	Deploy(ctx context.Context, route domain.Route) (*domain.Container, error)
	Restart(ctx context.Context, domainName string, withAttachments bool) error
	ReconcileRemovedRoute(ctx context.Context, domainName string) (*domain.CleanupReport, error)
	SyncContainers(ctx context.Context) error
	AutoStart(ctx context.Context, routes []domain.Route) error
	List(ctx context.Context) map[string]*domain.Container
	ListRoutesWithDetails(ctx context.Context) []domain.RouteInfo
	ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error)
}

type runtimePolicySetter interface {
	SetRuntimePolicy(RuntimePolicy)
}

type runtimeVolumeLister interface {
	ListVolumes(context.Context) ([]*domain.VolumeInfo, error)
}

type runtimePolicyDeniedEventPublisher interface {
	PublishRuntimePolicyDeniedEvent(context.Context, domain.RuntimePolicyDeniedEvent) error
}

// RuntimeWorker adapts local container.Service behavior to runtime intent commands.
type RuntimeWorker struct {
	service runtimeWorkerService
	policy  RuntimePolicy
	now     func() time.Time

	mu                      sync.Mutex
	completedByDedupeKey    map[string]domain.RuntimeCommandResult
	completedDedupeKeyOrder []string
	inFlightByDedupeKey     map[string]*runtimeWorkerInFlight
	policyDeniedEvents      []domain.RuntimePolicyDeniedEvent
}

// NewRuntimeWorker creates a local runtime worker around the existing container service.
func NewRuntimeWorker(service runtimeWorkerService) *RuntimeWorker {
	return NewRuntimeWorkerWithPolicy(service, NewRuntimePolicy(RuntimePolicyModeObserve))
}

type runtimeWorkerInFlight struct {
	done   chan struct{}
	result domain.RuntimeCommandResult
	err    error
}

// NewRuntimeWorkerWithPolicy creates a local runtime worker with explicit policy mode.
func NewRuntimeWorkerWithPolicy(service runtimeWorkerService, policy RuntimePolicy) *RuntimeWorker {
	policy = policy.normalize()
	if setter, ok := service.(runtimePolicySetter); ok {
		setter.SetRuntimePolicy(policy)
	}
	return &RuntimeWorker{service: service, policy: policy, now: time.Now, completedByDedupeKey: make(map[string]domain.RuntimeCommandResult), inFlightByDedupeKey: make(map[string]*runtimeWorkerInFlight)}
}

// PolicyDeniedEvents returns policy findings recorded by this worker in observe or enforce mode.
func (w *RuntimeWorker) PolicyDeniedEvents() []domain.RuntimePolicyDeniedEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	events := make([]domain.RuntimePolicyDeniedEvent, len(w.policyDeniedEvents))
	copy(events, w.policyDeniedEvents)
	return events
}

func (w *RuntimeWorker) DeployRoute(ctx context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return w.runWithPolicy(ctx, command.RuntimeCommandIdentity, "deploy_route", func() error { return w.policy.CheckDeployRoute(command) }, "", func() error {
		deployCtx := ctx
		if command.InternalDeploy {
			deployCtx = domain.WithInternalDeploy(ctx)
		}
		_, err := w.service.Deploy(deployCtx, domain.Route{Domain: command.Domain, Image: command.Image, Env: command.Env})
		return err
	})
}

func (w *RuntimeWorker) RestartRoute(ctx context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return w.runWithPolicy(ctx, command.RuntimeCommandIdentity, "restart_route", func() error { return w.policy.CheckRestartRoute(command) }, "", func() error {
		return w.service.Restart(ctx, command.Domain, command.WithAttachments)
	})
}

func (w *RuntimeWorker) RemoveRoute(ctx context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return w.runWithPolicy(ctx, command.RuntimeCommandIdentity, "remove_route", func() error { return w.policy.CheckRemoveRoute(command) }, "", func() error {
		_, err := w.service.ReconcileRemovedRoute(ctx, command.Domain)
		return err
	})
}

func (w *RuntimeWorker) Reconcile(ctx context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return w.runWithPolicy(ctx, command.RuntimeCommandIdentity, "reconcile", func() error { return w.policy.CheckReconcile(command) }, "", func() error {
		if err := w.service.SyncContainers(ctx); err != nil {
			return err
		}
		return w.service.AutoStart(ctx, command.DesiredRoutes)
	})
}

// SelfUpdate validates policy intent but does not perform unmanaged local runtime mutations.
func (w *RuntimeWorker) SelfUpdate(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return w.runWithPolicy(ctx, command.RuntimeCommandIdentity, "self_update", func() error { return w.policy.CheckSelfUpdate(command) }, command.PolicyDecisionID, func() error {
		return errRuntimeSelfUpdateUnavailable
	})
}

// Snapshot returns a sanitized actual runtime state snapshot from the existing service views.
func (w *RuntimeWorker) Snapshot(ctx context.Context, generation uint64, stateVersion, sourceComponentID string) (domain.RuntimeActualStateSnapshot, error) {
	containers := w.service.List(ctx)
	routes := w.service.ListRoutesWithDetails(ctx)
	networks, err := w.service.ListNetworks(ctx)
	if err != nil {
		return domain.RuntimeActualStateSnapshot{}, fmt.Errorf("list runtime networks: %w", err)
	}
	networkAliases := runtimeNetworkAliases(networks)
	snapshot := domain.RuntimeActualStateSnapshot{
		Generation:        generation,
		StateVersion:      stateVersion,
		SourceComponentID: sourceComponentID,
		ObservedAt:        w.now(),
		Routes:            buildRuntimeRouteStates(generation, routes, containers, networkAliases),
		Containers:        buildRuntimeContainerStates(generation, containers),
		Networks:          buildRuntimeNetworkStates(generation, networks),
		Volumes:           w.buildRuntimeVolumeStates(ctx, generation),
		EdgeAttachments:   buildRuntimeEdgeAttachmentStates(generation, routes, containers, networkAliases, sourceComponentID),
	}
	if err := snapshot.Validate(); err != nil {
		return domain.RuntimeActualStateSnapshot{}, err
	}
	return snapshot, nil
}

func (w *RuntimeWorker) runWithPolicy(ctx context.Context, identity domain.RuntimeCommandIdentity, kind string, checkPolicy func() error, decisionID string, op func() error) (domain.RuntimeCommandResult, error) {
	return w.runOnce(ctx, identity, kind, func() error {
		if policyErr := checkPolicy(); policyErr != nil {
			w.recordPolicyDenied(ctx, policyErr, decisionID)
			if w.policy.Enforced() {
				return policyErr
			}
		}
		return op()
	})
}

func (w *RuntimeWorker) runOnce(ctx context.Context, identity domain.RuntimeCommandIdentity, kind string, op func() error) (domain.RuntimeCommandResult, error) {
	key := identity.DedupeKey(kind)

	w.mu.Lock()
	if result, ok := w.completedByDedupeKey[key]; ok {
		w.mu.Unlock()
		return result, nil
	}
	if inFlight, ok := w.inFlightByDedupeKey[key]; ok {
		w.mu.Unlock()
		select {
		case <-inFlight.done:
			return inFlight.result, inFlight.err
		case <-ctx.Done():
			return w.failedResult(identity, ctx.Err()), nil
		}
	}
	inFlight := &runtimeWorkerInFlight{done: make(chan struct{})}
	w.inFlightByDedupeKey[key] = inFlight
	w.mu.Unlock()

	result, cacheResult := w.run(identity, op)

	w.mu.Lock()
	if cacheResult {
		w.rememberCompletedResultLocked(key, result)
	}
	delete(w.inFlightByDedupeKey, key)
	inFlight.result = result
	inFlight.err = nil
	close(inFlight.done)
	w.mu.Unlock()

	return result, nil
}

func (w *RuntimeWorker) run(identity domain.RuntimeCommandIdentity, op func() error) (domain.RuntimeCommandResult, bool) {
	result := w.baseResult(identity)
	if err := op(); err != nil {
		if errors.Is(err, errRuntimeSelfUpdateUnavailable) {
			result.CompletedAt = result.StartedAt
		} else {
			result.CompletedAt = w.now()
		}
		result.Status = statusForError(err)
		result.Error = sanitizeRuntimeCommandError(err)
		return result, errors.Is(err, ErrRuntimePolicyDenied)
	}
	result.CompletedAt = w.now()
	result.Status = domain.RuntimeCommandStatusSucceeded
	return result, true
}

func (w *RuntimeWorker) baseResult(identity domain.RuntimeCommandIdentity) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{CommandID: identity.ID, IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, Status: domain.RuntimeCommandStatusRunning, StartedAt: w.now()}
}

func (w *RuntimeWorker) failedResult(identity domain.RuntimeCommandIdentity, err error) domain.RuntimeCommandResult {
	result := w.baseResult(identity)
	result.CompletedAt = result.StartedAt
	result.Status = statusForError(err)
	result.Error = sanitizeRuntimeCommandError(err)
	return result
}

func (w *RuntimeWorker) recordPolicyDenied(ctx context.Context, err error, decisionID string) {
	event, ok := RuntimePolicyDeniedEventFromError(err, decisionID)
	if !ok {
		return
	}
	event.OccurredAt = w.now()
	w.mu.Lock()
	w.policyDeniedEvents = append(w.policyDeniedEvents, event)
	if len(w.policyDeniedEvents) > runtimeWorkerPolicyDeniedEventLimit {
		copy(w.policyDeniedEvents, w.policyDeniedEvents[len(w.policyDeniedEvents)-runtimeWorkerPolicyDeniedEventLimit:])
		w.policyDeniedEvents = w.policyDeniedEvents[:runtimeWorkerPolicyDeniedEventLimit]
	}
	w.mu.Unlock()
	if publisher, ok := w.service.(runtimePolicyDeniedEventPublisher); ok {
		_ = publisher.PublishRuntimePolicyDeniedEvent(ctx, event)
	}
}

func (w *RuntimeWorker) rememberCompletedResultLocked(key string, result domain.RuntimeCommandResult) {
	if _, exists := w.completedByDedupeKey[key]; !exists {
		w.completedDedupeKeyOrder = append(w.completedDedupeKeyOrder, key)
	}
	w.completedByDedupeKey[key] = result
	for len(w.completedDedupeKeyOrder) > runtimeWorkerCompletedResultLimit {
		oldest := w.completedDedupeKeyOrder[0]
		copy(w.completedDedupeKeyOrder, w.completedDedupeKeyOrder[1:])
		w.completedDedupeKeyOrder = w.completedDedupeKeyOrder[:len(w.completedDedupeKeyOrder)-1]
		delete(w.completedByDedupeKey, oldest)
	}
}

func statusForError(err error) domain.RuntimeCommandStatus {
	if errors.Is(err, ErrRuntimePolicyDenied) || errors.Is(err, errRuntimeSelfUpdateUnavailable) {
		return domain.RuntimeCommandStatusDenied
	}
	return domain.RuntimeCommandStatusFailed
}

func sanitizeRuntimeCommandError(err error) *domain.RuntimeCommandError {
	if err == nil {
		return nil
	}
	code := "runtime_command_failed"
	var policyDenied RuntimePolicyDeniedError
	if errors.As(err, &policyDenied) {
		code = formatPolicyReason(policyDenied.Reason)
	} else if errors.Is(err, errRuntimeSelfUpdateUnavailable) {
		code = "self_update_unavailable"
	} else if errors.Is(err, context.Canceled) {
		code = "context_canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "context_deadline_exceeded"
	} else if errors.Is(err, domain.ErrInvalidRuntimeCommand) {
		code = "invalid_runtime_command"
	}
	return &domain.RuntimeCommandError{Code: code, Message: sanitizeRuntimeErrorMessage(err), Retryable: code == "context_deadline_exceeded"}
}

func sanitizeRuntimeErrorMessage(err error) string {
	switch {
	case err == nil:
		return "runtime command failed"
	case errors.Is(err, errRuntimeSelfUpdateUnavailable):
		return "runtime self-update is unavailable"
	case errors.Is(err, context.Canceled):
		return "context canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context deadline exceeded"
	case errors.Is(err, domain.ErrInvalidRuntimeCommand):
		return "invalid runtime command"
	}

	var policyDenied RuntimePolicyDeniedError
	if errors.As(err, &policyDenied) {
		if message := strings.TrimSpace(policyDenied.Message); message != "" {
			return message
		}
		return "runtime policy denied"
	}
	return "runtime command failed"
}

func buildRuntimeContainerStates(generation uint64, containers map[string]*domain.Container) []domain.RuntimeContainerState {
	states := make([]domain.RuntimeContainerState, 0, len(containers))
	for _, c := range containers {
		if c == nil {
			continue
		}
		states = append(states, domain.RuntimeContainerState{Name: c.Name, Alias: targetAliasForDomain(c.Labels[domain.LabelDomain]), Image: c.Image, ImageID: c.ImageID, Status: normalizeContainerStatus(c.Status), StartedAt: c.Created, Labels: domain.SanitizeRuntimeStateLabels(c.Labels), Generation: generation})
	}
	return states
}

func (w *RuntimeWorker) buildRuntimeVolumeStates(ctx context.Context, generation uint64) []domain.RuntimeVolumeState {
	volumeLister, ok := w.service.(runtimeVolumeLister)
	if !ok {
		return nil
	}
	volumes, err := volumeLister.ListVolumes(ctx)
	if err != nil {
		return nil
	}
	states := make([]domain.RuntimeVolumeState, 0, len(volumes))
	for _, volume := range volumes {
		if volume == nil || !safeRuntimeVolumeName(volume.Name) {
			continue
		}
		states = append(states, domain.RuntimeVolumeState{Name: volume.Name, AttachedTo: sanitizedAliases(volume.Containers), Generation: generation})
	}
	return states
}

func buildRuntimeNetworkStates(generation uint64, networks []*domain.NetworkInfo) []domain.RuntimeNetworkState {
	states := make([]domain.RuntimeNetworkState, 0, len(networks))
	for _, n := range networks {
		if n == nil {
			continue
		}
		states = append(states, domain.RuntimeNetworkState{Name: n.Name, Driver: n.Driver, Aliases: sanitizedAliases(n.Containers), Generation: generation})
	}
	return states
}

func buildRuntimeRouteStates(generation uint64, routes []domain.RouteInfo, containers map[string]*domain.Container, networkAliases map[string]map[string]struct{}) []domain.RuntimeRouteState {
	states := make([]domain.RuntimeRouteState, 0, len(routes))
	for _, route := range routes {
		c := containerForRoute(route, containers)
		if c == nil || normalizeContainerStatus(c.Status) != domain.ContainerStatusRunning {
			states = append(states, unavailableRouteState(generation, route.Domain, route.ContainerStatus))
			continue
		}
		state, ready := runtimeReadyRouteState(generation, route.Domain, c)
		if !ready {
			states = append(states, unavailableRouteState(generation, route.Domain, "no target port"))
			continue
		}
		if !runtimeNetworkHasTarget(networkAliases, route.Network, state.EdgeTargetAlias, c.Name) {
			states = append(states, unavailableRouteState(generation, route.Domain, "no target alias"))
			continue
		}
		states = append(states, state)
	}
	return states
}

func buildRuntimeEdgeAttachmentStates(generation uint64, routes []domain.RouteInfo, containers map[string]*domain.Container, networkAliases map[string]map[string]struct{}, source string) []domain.RuntimeEdgeNetworkAttachmentState {
	states := make([]domain.RuntimeEdgeNetworkAttachmentState, 0, len(routes))
	for _, route := range routes {
		c := containerForRoute(route, containers)
		if c == nil || normalizeContainerStatus(c.Status) != domain.ContainerStatusRunning {
			continue
		}
		port := routeTargetPort(c)
		if port == 0 {
			continue
		}
		alias := targetAliasForDomain(route.Domain)
		if !runtimeNetworkHasTarget(networkAliases, route.Network, alias, c.Name) {
			continue
		}
		states = append(states, domain.RuntimeEdgeNetworkAttachmentState{RouteDomain: route.Domain, NetworkName: route.Network, EdgeAlias: "gordon-edge", RuntimeAlias: alias, TargetAlias: alias, TargetPort: port, Attached: true, Generation: generation, SourceComponent: source})
	}
	return states
}

func containerForRoute(route domain.RouteInfo, containers map[string]*domain.Container) *domain.Container {
	if route.ContainerID != "" {
		if c := containers[route.ContainerID]; c != nil {
			return c
		}
	}
	for _, c := range containers {
		if c != nil && c.Labels[domain.LabelDomain] == route.Domain {
			return c
		}
	}
	return nil
}

// runtimeReadyRouteState is the one managed route-state construction shared by
// snapshot production and the runtime drain registry. The backing name is
// private runtime material; callers must derive an opaque TargetKey before it
// crosses a component boundary.
func runtimeReadyRouteState(generation uint64, routeDomain string, c *domain.Container) (domain.RuntimeRouteState, bool) {
	canonicalDomain, ok := domain.CanonicalRouteDomain(routeDomain)
	if !ok || c == nil || normalizeContainerStatus(c.Status) != domain.ContainerStatusRunning {
		return domain.RuntimeRouteState{}, false
	}
	port := routeTargetPort(c)
	if port == 0 || strings.TrimSpace(c.Name) == "" {
		return domain.RuntimeRouteState{}, false
	}
	alias := targetAliasForDomain(canonicalDomain)
	return domain.RuntimeRouteState{
		Domain:               canonicalDomain,
		Generation:           generation,
		ContainerAlias:       alias,
		EdgeTargetAlias:      alias,
		TargetPort:           port,
		Scheme:               routeScheme(),
		Protocol:             routeProtocol(c),
		Status:               domain.RouteTargetStatusReady,
		UnavailableReason:    domain.RouteTargetUnavailableReasonNone,
		BackingContainerName: c.Name,
	}, true
}

func unavailableRouteState(generation uint64, routeDomain, status string) domain.RuntimeRouteState {
	reason := domain.RouteTargetUnavailableReasonNoTarget
	if strings.Contains(strings.ToLower(status), "start") {
		reason = domain.RouteTargetUnavailableReasonStarting
	}
	return domain.RuntimeRouteState{Domain: routeDomain, Generation: generation, Status: domain.RouteTargetStatusUnavailable, UnavailableReason: reason}
}

func routeTargetPort(c *domain.Container) int {
	if raw := c.Labels[domain.LabelProxyPort]; raw != "" {
		if port, err := strconv.Atoi(raw); err == nil && port > 0 && port <= 65535 {
			return port
		}
	}
	if len(c.Ports) > 0 && c.Ports[0] > 0 && c.Ports[0] <= 65535 {
		return c.Ports[0]
	}
	return 0
}

func routeScheme() string {
	return "http"
}

func routeProtocol(c *domain.Container) domain.RouteTargetProtocol {
	if c.Labels[domain.LabelProxyProtocol] == string(domain.RouteTargetProtocolH2C) {
		return domain.RouteTargetProtocolH2C
	}
	return domain.RouteTargetProtocolHTTP1
}

func normalizeContainerStatus(status string) domain.ContainerStatus {
	switch domain.ContainerStatus(strings.ToLower(status)) {
	case domain.ContainerStatusRunning, domain.ContainerStatusStopped, domain.ContainerStatusCreated, domain.ContainerStatusExited, domain.ContainerStatusPaused:
		return domain.ContainerStatus(strings.ToLower(status))
	default:
		return domain.ContainerStatusUnknown
	}
}

func targetAliasForDomain(domainName string) string {
	return runtimeWorkerTargetAliasPrefix + strings.ReplaceAll(strings.ToLower(domainName), ".", "-")
}

func routeTargetAliases(domainName string) []string {
	return []string{targetAliasForDomain(domainName)}
}

func runtimeNetworkAliases(networks []*domain.NetworkInfo) map[string]map[string]struct{} {
	aliases := make(map[string]map[string]struct{}, len(networks))
	for _, network := range networks {
		if network == nil {
			continue
		}
		for _, alias := range sanitizedAliases(network.Containers) {
			if aliases[network.Name] == nil {
				aliases[network.Name] = make(map[string]struct{})
			}
			aliases[network.Name][alias] = struct{}{}
		}
	}
	return aliases
}

func runtimeNetworkHasTarget(networkAliases map[string]map[string]struct{}, networkName, alias, containerName string) bool {
	if networkAliases == nil || networkName == "" {
		return false
	}
	for _, candidate := range []string{alias, containerName} {
		if candidate == "" {
			continue
		}
		if _, ok := networkAliases[networkName][candidate]; ok {
			return true
		}
	}
	return false
}

func sanitizedAliases(values []string) []string {
	aliases := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || strings.EqualFold(trimmed, "localhost") || strings.HasPrefix(trimmed, "127.") || trimmed == "::1" || strings.ContainsAny(trimmed, `/\\`) {
			continue
		}
		aliases = append(aliases, trimmed)
	}
	return aliases
}

func safeRuntimeVolumeName(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" && !strings.ContainsAny(trimmed, `/\\`) && !strings.Contains(trimmed, "://") && !isRuntimeSocketMount(trimmed)
}

var _ runtimeWorkerService = (*Service)(nil)
