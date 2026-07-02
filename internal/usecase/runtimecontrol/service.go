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
	sourceComponentID string
	now               func() time.Time
}

func NewService(configSvc in.ConfigService, runtime out.RuntimeCommandClient, sourceComponentID string) *Service {
	if strings.TrimSpace(sourceComponentID) == "" {
		sourceComponentID = "gordon-control"
	}
	return &Service{configSvc: configSvc, runtime: runtime, sourceComponentID: sourceComponentID, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) DeployRoute(ctx context.Context, route domain.Route) (domain.RuntimeCommandResult, error) {
	if s.runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime command client unavailable")
	}
	identity := s.identity("deploy", route.Domain)
	return s.runtime.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: route.Domain, Image: route.Image, RouteVersion: identity.IdempotencyKey, Env: route.Env})
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

func (s *Service) identity(kind, subject string) domain.RuntimeCommandIdentity {
	now := s.now().UTC()
	cleanSubject := strings.TrimSpace(subject)
	if cleanSubject == "" {
		cleanSubject = "all"
	}
	key := fmt.Sprintf("%s:%s:%d", kind, cleanSubject, now.UnixNano())
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID("runtime-control:" + key), IdempotencyKey: key, Generation: uint64(now.UnixNano()), SourceComponentID: s.sourceComponentID, RequestedAt: now}
}
