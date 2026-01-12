package proxy

import (
	"context"

	"github.com/bnema/zerowrap"

	"gordon/internal/domain"
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
