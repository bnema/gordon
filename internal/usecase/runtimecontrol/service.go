package runtimecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
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
	generation        atomic.Uint64
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
	version := desiredStateVersion([]domain.Route{route})
	identity := s.desiredIdentity("deploy", version)
	return s.runtime.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: route.Domain, Image: route.Image, RouteVersion: version, Env: route.Env, InternalDeploy: true})
}

func (s *Service) RestartRoute(ctx context.Context, domainName string, withAttachments bool) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	return s.runtime.RestartRoute(ctx, domain.RestartRouteCommand{RuntimeCommandIdentity: s.imperativeIdentity("restart", domainName), Domain: domainName, WithAttachments: withAttachments})
}

func (s *Service) RemoveRoute(ctx context.Context, domainName string, force bool) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	return s.runtime.RemoveRoute(ctx, domain.RemoveRouteCommand{RuntimeCommandIdentity: s.imperativeIdentity("remove", domainName), Domain: domainName, Force: force})
}

func (s *Service) ReconcileConfiguredRoutes(ctx context.Context, reason string) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	if s.configSvc == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("config service unavailable")
	}
	routes := s.configSvc.GetRoutes(ctx)
	version := desiredStateVersion(routes)
	identity := s.desiredIdentity("reconcile", version)
	return s.runtime.Reconcile(ctx, domain.ReconcileRuntimeCommand{RuntimeCommandIdentity: identity, Reason: reason, ExpectedRouteCount: len(routes), DesiredStateVersion: version, DesiredRoutes: routes})
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

func (s *Service) desiredIdentity(kind, version string) domain.RuntimeCommandIdentity {
	return s.identity(kind + ":" + version)
}

func (s *Service) imperativeIdentity(kind, subject string) domain.RuntimeCommandIdentity {
	cleanSubject := strings.TrimSpace(subject)
	if cleanSubject == "" {
		cleanSubject = "all"
	}
	generation := s.generation.Add(1)
	return s.identityWithGeneration(fmt.Sprintf("%s:%s:%d", kind, cleanSubject, generation), generation)
}

func (s *Service) identity(key string) domain.RuntimeCommandIdentity {
	return s.identityWithGeneration(key, s.generation.Add(1))
}

func (s *Service) identityWithGeneration(key string, generation uint64) domain.RuntimeCommandIdentity {
	now := s.now().UTC()
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID("runtime-control:" + key), IdempotencyKey: key, Generation: generation, SourceComponentID: s.sourceComponentID, RequestedAt: now}
}

func desiredStateVersion(routes []domain.Route) string {
	type desiredRoute struct {
		Domain string   `json:"domain"`
		Image  string   `json:"image"`
		HTTPS  bool     `json:"https"`
		Env    []string `json:"env"`
	}
	canonical := make([]desiredRoute, len(routes))
	for i, route := range routes {
		env := append([]string(nil), route.Env...)
		sort.Strings(env)
		canonical[i] = desiredRoute{Domain: strings.TrimSpace(route.Domain), Image: strings.TrimSpace(route.Image), HTTPS: route.HTTPS, Env: env}
	}
	sort.Slice(canonical, func(i, j int) bool {
		left, right := canonical[i], canonical[j]
		if left.Domain != right.Domain {
			return left.Domain < right.Domain
		}
		if left.Image != right.Image {
			return left.Image < right.Image
		}
		if left.HTTPS != right.HTTPS {
			return !left.HTTPS
		}
		return strings.Join(left.Env, "\x00") < strings.Join(right.Env, "\x00")
	})
	payload, _ := json.Marshal(canonical)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
