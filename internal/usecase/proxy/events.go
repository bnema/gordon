package proxy

import (
	"context"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// TargetInvalidator defines the interface for invalidating proxy targets.
type TargetInvalidator interface {
	InvalidateTarget(ctx context.Context, domainName string)
}

// ContainerDeployedHandler handles container.deployed events to invalidate proxy cache.
type ContainerDeployedHandler struct {
	invalidator TargetInvalidator
	ctx         context.Context
}

// NewContainerDeployedHandler creates a new ContainerDeployedHandler.
func NewContainerDeployedHandler(ctx context.Context, invalidator TargetInvalidator) *ContainerDeployedHandler {
	return &ContainerDeployedHandler{
		invalidator: invalidator,
		ctx:         ctx,
	}
}

// Handle handles a container.deployed event by invalidating the proxy cache.
func (h *ContainerDeployedHandler) Handle(event domain.Event) error {
	ctx := zerowrap.CtxWithFields(h.ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ContainerDeployedHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	// Get domain from event
	domainName := event.Route
	if domainName == "" {
		// Try to get from payload
		if payload, ok := event.Data.(*domain.ContainerEventPayload); ok {
			domainName = payload.Domain
		}
	}

	if domainName == "" {
		log.Debug().Msg("no domain in container deployed event, skipping cache invalidation")
		return nil
	}

	log.Debug().Str("domain", domainName).Msg("invalidating proxy cache for deployed container")
	h.invalidator.InvalidateTarget(ctx, domainName)

	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ContainerDeployedHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventContainerDeployed
}

// TargetRefresher defines the interface for refreshing all proxy targets.
type TargetRefresher interface {
	RefreshTargets(ctx context.Context) error
}

// ConfigReloadProxyHandler handles config.reload events to clear the proxy target cache.
// When routes are removed during a config reload, cached targets for those routes
// become stale. This handler clears all cached targets so they are re-resolved.
type ConfigReloadProxyHandler struct {
	refresher TargetRefresher
	ctx       context.Context
}

// NewConfigReloadProxyHandler creates a new ConfigReloadProxyHandler.
func NewConfigReloadProxyHandler(ctx context.Context, refresher TargetRefresher) *ConfigReloadProxyHandler {
	return &ConfigReloadProxyHandler{
		refresher: refresher,
		ctx:       ctx,
	}
}

// Handle clears all cached proxy targets so they are re-resolved from current state.
func (h *ConfigReloadProxyHandler) Handle(event domain.Event) error {
	ctx := zerowrap.CtxWithFields(h.ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ConfigReloadProxyHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	log.Debug().Msg("clearing proxy target cache after config reload")
	return h.refresher.RefreshTargets(ctx)
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ConfigReloadProxyHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventConfigReload
}
