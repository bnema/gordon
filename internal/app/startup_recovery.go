package app

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
)

// startupConfigService defines the route configuration needed during startup
// recovery.
type startupConfigService interface {
	GetRoutes(ctx context.Context) []domain.Route
}

// startupContainerService defines the container lifecycle operations needed
// during startup recovery.
type startupContainerService interface {
	SyncContainers(ctx context.Context) error
	AutoStart(ctx context.Context, routes []domain.Route) error
	StartMonitor(ctx context.Context)
}

// syncAndRecoverConfiguredRoutes performs best-effort startup recovery for
// configured routes after listeners are ready.
func syncAndRecoverConfiguredRoutes(
	ctx context.Context,
	configSvc startupConfigService,
	containerSvc startupContainerService,
	log zerowrap.Logger,
) {
	defer containerSvc.StartMonitor(ctx)

	if err := containerSvc.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to sync existing containers")
	}

	routes := configSvc.GetRoutes(ctx)
	if err := containerSvc.AutoStart(domain.WithInternalDeploy(ctx), routes); err != nil {
		log.Warn().Err(err).Msg("failed to auto-start configured routes")
	}
}
