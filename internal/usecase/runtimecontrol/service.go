package runtimecontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Service coordinates control-plane requests into narrow runtime intent commands.
type Service struct {
	configSvc         in.ConfigService
	runtime           out.RuntimeCommandClient
	stateSubscriber   out.RuntimeStateSubscriber
	sourceComponentID string
	now               func() time.Time
}

func NewService(configSvc in.ConfigService, runtime out.RuntimeCommandClient, sourceComponentID string) *Service {
	if strings.TrimSpace(sourceComponentID) == "" {
		sourceComponentID = "gordon-control"
	}
	var subscriber out.RuntimeStateSubscriber
	if runtimeSubscriber, ok := runtime.(out.RuntimeStateSubscriber); ok {
		subscriber = runtimeSubscriber
	}
	return &Service{configSvc: configSvc, runtime: runtime, stateSubscriber: subscriber, sourceComponentID: sourceComponentID, now: func() time.Time { return time.Now().UTC() }}
}

func NewServiceWithStateSubscriber(configSvc in.ConfigService, runtime out.RuntimeCommandClient, subscriber out.RuntimeStateSubscriber, sourceComponentID string) *Service {
	svc := NewService(configSvc, runtime, sourceComponentID)
	svc.stateSubscriber = subscriber
	return svc
}

func (s *Service) DeployRoute(ctx context.Context, route domain.Route) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	identity := s.identity("deploy", route.Domain)
	return s.runtime.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: route.Domain, Image: route.Image, RouteVersion: identity.IdempotencyKey, Env: route.Env, InternalDeploy: true})
}

func (s *Service) RestartRoute(ctx context.Context, domainName string, withAttachments bool) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	return s.runtime.RestartRoute(ctx, domain.RestartRouteCommand{RuntimeCommandIdentity: s.identity("restart", domainName), Domain: domainName, WithAttachments: withAttachments})
}

func (s *Service) RemoveRoute(ctx context.Context, domainName string, force bool) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	return s.runtime.RemoveRoute(ctx, domain.RemoveRouteCommand{RuntimeCommandIdentity: s.identity("remove", domainName), Domain: domainName, Force: force})
}

func (s *Service) ReconcileConfiguredRoutes(ctx context.Context, reason string) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	if s.configSvc == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("config service unavailable")
	}
	routes := s.configSvc.GetRoutes(ctx)
	identity := s.identity("reconcile", reason)
	return s.runtime.Reconcile(ctx, domain.ReconcileRuntimeCommand{RuntimeCommandIdentity: identity, Reason: reason, ExpectedRouteCount: len(routes), DesiredStateVersion: identity.IdempotencyKey, DesiredRoutes: routes})
}

func (s *Service) RouteStatuses(ctx context.Context, routes []domain.Route) (map[string]string, error) {
	if s.stateSubscriber == nil {
		return nil, fmt.Errorf("runtime state subscriber unavailable")
	}
	stateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshots, err := s.stateSubscriber.SubscribeRuntimeState(stateCtx)
	if err != nil {
		return nil, err
	}
	select {
	case <-stateCtx.Done():
		return nil, fmt.Errorf("runtime state unavailable: %w", stateCtx.Err())
	case snapshot, ok := <-snapshots:
		if !ok {
			return nil, fmt.Errorf("runtime state unavailable")
		}
		return routeStatusesFromSnapshot(routes, snapshot), nil
	}
}

func routeStatusesFromSnapshot(routes []domain.Route, snapshot domain.RuntimeActualStateSnapshot) map[string]string {
	statuses := make(map[string]string, len(routes))
	for _, route := range routes {
		statuses[route.Domain] = string(domain.ContainerStatusUnknown)
	}
	for _, container := range snapshot.Containers {
		routeDomain := container.Labels[domain.LabelRoute]
		if _, ok := statuses[routeDomain]; ok && container.Status != "" {
			statuses[routeDomain] = string(container.Status)
		}
	}
	for _, routeState := range snapshot.Routes {
		if _, ok := statuses[routeState.Domain]; !ok || statuses[routeState.Domain] != string(domain.ContainerStatusUnknown) {
			continue
		}
		statuses[routeState.Domain] = statusFromRouteState(routeState.Status)
	}
	return statuses
}

func statusFromRouteState(status domain.RouteTargetStatus) string {
	switch status {
	case domain.RouteTargetStatusReady, domain.RouteTargetStatusDraining:
		return string(domain.ContainerStatusRunning)
	default:
		return string(domain.ContainerStatusUnknown)
	}
}

func (s *Service) identity(kind, subject string) domain.RuntimeCommandIdentity {
	now := s.now().UTC()
	cleanSubject := strings.TrimSpace(subject)
	if cleanSubject == "" {
		cleanSubject = "all"
	}
	key := fmt.Sprintf("%s:%s:%d", kind, cleanSubject, now.UnixNano())
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID("runtime-control:" + key), IdempotencyKey: key, Generation: uint64(now.UnixNano()), SourceComponentID: s.sourceComponentID, RequestedAt: now}
}
