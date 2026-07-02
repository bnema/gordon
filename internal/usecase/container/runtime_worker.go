package container

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const runtimeWorkerTargetAliasPrefix = "gordon-target-"

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

// RuntimeWorker adapts local container.Service behavior to runtime intent commands.
type RuntimeWorker struct {
	service runtimeWorkerService
	policy  RuntimePolicy
	now     func() time.Time
}

// NewRuntimeWorker creates a local runtime worker around the existing container service.
func NewRuntimeWorker(service runtimeWorkerService) *RuntimeWorker {
	return NewRuntimeWorkerWithPolicy(service, NewRuntimePolicy(RuntimePolicyModeObserve))
}

// NewRuntimeWorkerWithPolicy creates a local runtime worker with explicit policy mode.
func NewRuntimeWorkerWithPolicy(service runtimeWorkerService, policy RuntimePolicy) *RuntimeWorker {
	policy = policy.normalize()
	if setter, ok := service.(runtimePolicySetter); ok {
		setter.SetRuntimePolicy(policy)
	}
	return &RuntimeWorker{service: service, policy: policy, now: time.Now}
}

func (w *RuntimeWorker) DeployRoute(ctx context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := w.policyResult(command.RuntimeCommandIdentity, w.policy.CheckDeployRoute(command)); denied {
		return result, nil
	}
	deployCtx := ctx
	if command.InternalDeploy {
		deployCtx = domain.WithInternalDeploy(ctx)
	}
	return w.run(command.RuntimeCommandIdentity, func() error {
		_, err := w.service.Deploy(deployCtx, domain.Route{Domain: command.Domain, Image: command.Image, Env: command.Env})
		return err
	})
}

func (w *RuntimeWorker) RestartRoute(ctx context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := w.policyResult(command.RuntimeCommandIdentity, w.policy.CheckRestartRoute(command)); denied {
		return result, nil
	}
	return w.run(command.RuntimeCommandIdentity, func() error {
		return w.service.Restart(ctx, command.Domain, command.WithAttachments)
	})
}

func (w *RuntimeWorker) RemoveRoute(ctx context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := w.policyResult(command.RuntimeCommandIdentity, w.policy.CheckRemoveRoute(command)); denied {
		return result, nil
	}
	return w.run(command.RuntimeCommandIdentity, func() error {
		_, err := w.service.ReconcileRemovedRoute(ctx, command.Domain)
		return err
	})
}

func (w *RuntimeWorker) Reconcile(ctx context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := w.policyResult(command.RuntimeCommandIdentity, w.policy.CheckReconcile(command)); denied {
		return result, nil
	}
	return w.run(command.RuntimeCommandIdentity, func() error {
		if err := w.service.SyncContainers(ctx); err != nil {
			return err
		}
		return w.service.AutoStart(ctx, command.DesiredRoutes)
	})
}

// SelfUpdate validates policy intent but does not perform unmanaged local runtime mutations.
func (w *RuntimeWorker) SelfUpdate(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return w.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := w.policyResult(command.RuntimeCommandIdentity, w.policy.CheckSelfUpdate(command)); denied {
		return result, nil
	}
	result := w.baseResult(command.RuntimeCommandIdentity)
	result.CompletedAt = result.StartedAt
	result.Status = domain.RuntimeCommandStatusDenied
	result.Error = &domain.RuntimeCommandError{Code: "self_update_unavailable", Message: "local runtime self-update adapter is not available", Retryable: false}
	return result, nil
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
		EdgeAttachments:   buildRuntimeEdgeAttachmentStates(generation, routes, containers, networkAliases, sourceComponentID),
	}
	if err := snapshot.Validate(); err != nil {
		return domain.RuntimeActualStateSnapshot{}, err
	}
	return snapshot, nil
}

func (w *RuntimeWorker) run(identity domain.RuntimeCommandIdentity, op func() error) (domain.RuntimeCommandResult, error) {
	result := w.baseResult(identity)
	if err := op(); err != nil {
		result.CompletedAt = w.now()
		result.Status = statusForError(err)
		result.Error = sanitizeRuntimeCommandError(err)
		return result, nil
	}
	result.CompletedAt = w.now()
	result.Status = domain.RuntimeCommandStatusSucceeded
	return result, nil
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

func (w *RuntimeWorker) policyResult(identity domain.RuntimeCommandIdentity, err error) (domain.RuntimeCommandResult, bool) {
	if err == nil || !w.policy.Enforced() {
		return domain.RuntimeCommandResult{}, false
	}
	return w.failedResult(identity, err), true
}

func statusForError(err error) domain.RuntimeCommandStatus {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrRuntimePolicyDenied) {
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
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "runtime command failed"
	}
	for _, marker := range []string{"SECRET=", "TOKEN=", "PASSWORD=", "unix://", "/var/run/", "/run/"} {
		if strings.Contains(strings.ToUpper(msg), marker) || strings.Contains(msg, marker) {
			return "runtime command failed"
		}
	}
	return msg
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
		port := routeTargetPort(c)
		if port == 0 {
			states = append(states, unavailableRouteState(generation, route.Domain, "no target port"))
			continue
		}
		alias := targetAliasForDomain(route.Domain)
		if !runtimeNetworkHasTarget(networkAliases, route.Network, alias, c.Name) {
			states = append(states, unavailableRouteState(generation, route.Domain, "no target alias"))
			continue
		}
		states = append(states, domain.RuntimeRouteState{Domain: route.Domain, Generation: generation, ContainerAlias: alias, EdgeTargetAlias: alias, TargetPort: port, Scheme: routeScheme(c), Protocol: routeProtocol(c), Status: domain.RouteTargetStatusReady, UnavailableReason: domain.RouteTargetUnavailableReasonNone, BackingContainerName: c.Name})
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

func routeScheme(c *domain.Container) string {
	if c.Labels[domain.LabelProxyProtocol] == string(domain.RouteTargetProtocolH2C) {
		return "http"
	}
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
		if trimmed == "" || strings.EqualFold(trimmed, "localhost") || strings.HasPrefix(trimmed, "127.") || trimmed == "::1" {
			continue
		}
		aliases = append(aliases, trimmed)
	}
	return aliases
}

var _ runtimeWorkerService = (*Service)(nil)
