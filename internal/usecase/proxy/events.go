package proxy

import (
	"context"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

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
func (h *ConfigReloadProxyHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
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
