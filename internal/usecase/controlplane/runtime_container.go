package controlplane

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

// RuntimeCommandContainerService adapts only deploy commands to the legacy
// ContainerService port. It lets existing event handlers run in control mode
// without exposing a local runtime socket. Methods that would require runtime
// inspection are deliberately unavailable in the split control process.
type RuntimeCommandContainerService struct{ runtime RouteCommander }

type componentEventDedupeContextKey struct{}

type eventRouteCommander interface {
	DeployRouteForEvent(context.Context, domain.Route, string) (domain.RuntimeCommandResult, error)
}

func withComponentEventDedupeKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, componentEventDedupeContextKey{}, key)
}

func NewRuntimeCommandContainerService(runtime RouteCommander) *RuntimeCommandContainerService {
	return &RuntimeCommandContainerService{runtime: runtime}
}

func (s *RuntimeCommandContainerService) Deploy(ctx context.Context, route domain.Route) (*domain.Container, error) {
	if s == nil || s.runtime == nil {
		return nil, fmt.Errorf("runtime command facade unavailable")
	}
	if eventKey, ok := ctx.Value(componentEventDedupeContextKey{}).(string); ok && eventKey != "" {
		if eventRuntime, supported := s.runtime.(eventRouteCommander); supported {
			if _, err := eventRuntime.DeployRouteForEvent(ctx, route, eventKey); err != nil {
				return nil, err
			}
			return &domain.Container{Name: route.Domain}, nil
		}
	}
	if _, err := s.runtime.DeployRoute(ctx, route); err != nil {
		return nil, err
	}
	return &domain.Container{Name: route.Domain}, nil
}
func (s *RuntimeCommandContainerService) Stop(context.Context, string) error {
	return unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) Remove(context.Context, string, bool) error {
	return unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) ReconcileRemovedRoute(context.Context, string) (*domain.CleanupReport, error) {
	return nil, unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) Get(context.Context, string) (*domain.Container, bool) {
	return nil, false
}
func (s *RuntimeCommandContainerService) Restart(context.Context, string, bool) error {
	return unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) List(context.Context) map[string]*domain.Container {
	return map[string]*domain.Container{}
}
func (s *RuntimeCommandContainerService) ListRoutesWithDetails(context.Context) []domain.RouteInfo {
	return nil
}
func (s *RuntimeCommandContainerService) ListAttachments(context.Context, string) []domain.Attachment {
	return nil
}
func (s *RuntimeCommandContainerService) ListOrphanedAttachments(context.Context) ([]domain.CleanupAttachment, error) {
	return nil, unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) CleanupOrphanedAttachments(context.Context, string, bool) (*domain.CleanupReport, error) {
	return nil, unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	return nil, unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) HealthCheck(context.Context) map[string]bool {
	return map[string]bool{}
}
func (s *RuntimeCommandContainerService) SyncContainers(context.Context) error {
	return unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) UpdateAttachments(map[string][]string) {}
func (s *RuntimeCommandContainerService) AutoStart(context.Context, []domain.Route) error {
	return unsupportedRuntimeContainerOperation()
}
func (s *RuntimeCommandContainerService) Shutdown(context.Context) error {
	return unsupportedRuntimeContainerOperation()
}

func unsupportedRuntimeContainerOperation() error {
	return fmt.Errorf("operation is unavailable through the control runtime-command facade")
}

var _ in.ContainerService = (*RuntimeCommandContainerService)(nil)
